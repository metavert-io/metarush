package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/tools"
)

// ResidentIdle is how long the model server stays up after its last batch.
var ResidentIdle = 10 * time.Minute

// ErrResidentUnavailable means the model cannot be kept in memory here (no C
// library, an unknown library version, no model server, or the server
// stopped); the transcription then runs the CLI.
var ErrResidentUnavailable = errors.New("the transcription model cannot be kept in memory on this machine")

// ServerCommand is the hidden subcommand that runs the model server.
const ServerCommand = "transcribe-model-server"

// serverEnv marks a model-server child for test binaries (their TestMain
// serves instead of running tests); the real binary dispatches on ServerCommand.
const serverEnv = "METARUSH_TRANSCRIBE_MODEL_SERVER"

// allowServerInTests lets a package's own tests start model servers (their
// TestMain re-executes the test binary as the server). Elsewhere under go test
// the binary is a test binary, so the CLI path is used.
var allowServerInTests = false

// modelServer is the worker's handle on one model server process.
type modelServer struct {
	t    *tools.Set
	args []string
	// kind is the model it keeps loaded ("parakeet", EngineWhisper) and engine
	// the engine its hello must name ("" for Parakeet, which came first).
	kind, engine string
	// modelBytes is the model file's size (whisper.cpp): the memory cap is at
	// least twice that, so a large model is not restarted for being large.
	modelBytes int64

	mu       sync.Mutex // one batch at a time
	run      *serverRun
	idle     *time.Timer
	crashes  int
	disabled error
	batches  int
	starts   int
	peak     int64 // the largest peak memory a server of this kind reported
}

type serverRun struct {
	stdin  *os.File
	lines  chan string
	done   chan struct{}
	err    error
	cancel context.CancelFunc
	id     int64
	hello  serverHello
	since  time.Time
}

var (
	serversMu sync.Mutex
	servers   = map[string]*modelServer{}
)

// serverFor returns the model server for a model folder and thread count (not
// started until its first batch).
func serverFor(t *tools.Set, lib, dir string, threads int) (*modelServer, error) {
	if testing.Testing() && !allowServerInTests {
		return nil, fmt.Errorf("%w: not under go test", ErrResidentUnavailable)
	}
	if t == nil || !t.Has(tools.Self) {
		return nil, fmt.Errorf("%w: cannot find this MetaRush binary", ErrResidentUnavailable)
	}
	if err := residentSupported(lib); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s|%s|%s|%d", t.Path(tools.Self), lib, dir, threads)
	return registerServer(key, func() *modelServer {
		return &modelServer{t: t, kind: "parakeet", args: []string{ServerCommand, "--library", lib, "--model", dir, "--threads", strconv.Itoa(threads)}}
	}), nil
}

// registerServer returns the model server for key, made by mk the first time.
func registerServer(key string, mk func() *modelServer) *modelServer {
	serversMu.Lock()
	defer serversMu.Unlock()
	s := servers[key]
	if s == nil {
		s = mk()
		servers[key] = s
	}
	return s
}

// decode sends one batch (sample ranges of a 16 kHz WAV) and returns each
// chunk's sherpa-onnx JSON line. Canceling ctx stops the server (the batch
// cannot be interrupted otherwise); it starts again for the next batch.
func (s *modelServer) decode(ctx context.Context, wav string, ranges [][2]int) ([]string, error) {
	rep, err := s.call(ctx, serverRequest{WAV: wav, Chunks: ranges})
	if err != nil {
		return nil, err
	}
	if rep.Error != "" {
		return nil, errors.New(rep.Error)
	}
	if len(rep.Results) != len(ranges) {
		return nil, fmt.Errorf("the model server returned %d results for %d chunks", len(rep.Results), len(ranges))
	}
	return rep.Results, nil
}

// call sends one request (its ID is set here) and returns the server's reply,
// one request at a time. A server that stops before replying is a crash
// (ErrResidentUnavailable); canceling ctx ends the server.
func (s *modelServer) call(ctx context.Context, req serverRequest) (serverReply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disabled != nil {
		return serverReply{}, s.disabled
	}
	if s.idle != nil {
		s.idle.Stop()
	}
	defer s.armIdle()
	if s.run == nil {
		r, err := s.start()
		if err != nil {
			return serverReply{}, err
		}
		s.run = r
	}
	r := s.run
	r.id++
	req.ID = r.id
	b, _ := json.Marshal(req)
	if _, err := r.stdin.Write(append(b, '\n')); err != nil {
		return serverReply{}, s.crashed(r)
	}
	for {
		select {
		case l := <-r.lines:
			var rep serverReply
			if json.Unmarshal([]byte(l), &rep) != nil || rep.ID != r.id {
				continue // not a reply to this request
			}
			s.batches++
			s.crashes = 0
			s.peak = max(s.peak, rep.MaxRSS)
			if rep.MaxRSS > s.memoryCap() {
				s.stop() // its memory goes back to the system; the next batch starts a fresh one
			}
			return rep, nil
		case <-r.done:
			return serverReply{}, s.crashed(r)
		case <-ctx.Done():
			s.run = nil // the batch cannot be interrupted: end the server now
			r.cancel()
			<-r.done
			_ = r.stdin.Close()
			return serverReply{}, ctx.Err()
		}
	}
}

// memoryCap is the peak memory after which the server is restarted before its
// next request: MaxServerMemory, and at least twice the model file.
func (s *modelServer) memoryCap() int64 { return max(MaxServerMemory, 2*s.modelBytes) }

// start launches the server and waits for its hello (s.mu held).
func (s *modelServer) start() (*serverRun, error) {
	// A real pipe (not io.Pipe): the child reads it directly, so waiting for
	// the process never waits for a copy of its input, and writing to a server
	// that died fails instead of blocking.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrResidentUnavailable, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &serverRun{stdin: pw, lines: make(chan string, 4), done: make(chan struct{}), cancel: cancel, since: time.Now()}
	s.starts++
	go func() {
		_, err := s.t.Run(ctx, tools.Spec{Tool: tools.Self, Args: s.args, Env: []string{serverEnv + "=1"}, Stdin: pr, MaxLine: 256 << 20, Background: true,
			OnStart: func(int) { _ = pr.Close() }, // the child has its own copy
			OnLine: func(l string) {
				select {
				case r.lines <- l:
				case <-ctx.Done():
				}
			}})
		r.err = err
		_ = pr.Close()
		close(r.done)
	}()
	fail := func(err error) (*serverRun, error) {
		cancel()
		_ = pw.Close()
		<-r.done
		return nil, err
	}
	timeout := time.NewTimer(3 * time.Minute)
	defer timeout.Stop()
	for {
		select {
		case l := <-r.lines:
			if json.Unmarshal([]byte(l), &r.hello) != nil {
				continue
			}
			switch {
			case r.hello.Protocol != serverProtocol:
				return fail(fmt.Errorf("%w: the model server speaks protocol %d, not %d", ErrResidentUnavailable, r.hello.Protocol, serverProtocol))
			case r.hello.Engine != s.engine:
				return fail(fmt.Errorf("%w: the model server keeps another model loaded (%q, not %q)", ErrResidentUnavailable, r.hello.Engine, s.engine))
			case !r.hello.Ready:
				s.disabled = fmt.Errorf("%w: %s", ErrResidentUnavailable, r.hello.Error)
				return fail(s.disabled)
			}
			return r, nil
		case <-r.done:
			cancel()
			_ = pw.Close()
			s.crashes++
			if s.crashes >= 2 {
				s.disabled = fmt.Errorf("%w: the model server did not start (%v)", ErrResidentUnavailable, r.err)
			}
			return nil, fmt.Errorf("%w: the model server did not start (%v)", ErrResidentUnavailable, r.err)
		case <-timeout.C:
			return fail(fmt.Errorf("%w: the model server did not start within 3 minutes", ErrResidentUnavailable))
		}
	}
}

// crashed records a server that stopped in the middle of a batch (s.mu held).
// Two in a row turn the resident model off for this process: the CLI runs
// each batch in a process of its own.
func (s *modelServer) crashed(r *serverRun) error {
	r.cancel()
	<-r.done
	_ = r.stdin.Close()
	s.run = nil
	s.crashes++
	err := fmt.Errorf("%w: the model server stopped (%v)", ErrResidentUnavailable, r.err)
	if s.crashes >= 2 {
		s.disabled = err
	}
	return err
}

// stop ends the server: end of input first (it frees the model and exits),
// then a kill if it does not exit within 10 s (s.mu held).
func (s *modelServer) stop() {
	r := s.run
	if r == nil {
		return
	}
	s.run = nil
	_ = r.stdin.Close()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		r.cancel()
		<-r.done
	}
	r.cancel()
}

// armIdle stops the server after ResidentIdle without batches (s.mu held).
func (s *modelServer) armIdle() {
	if s.run == nil {
		return
	}
	s.idle = time.AfterFunc(ResidentIdle, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.stop()
	})
}

// ReleaseResident stops every model server of this process (tests, the
// benchmark, a daemon shutting down).
func ReleaseResident() {
	serversMu.Lock()
	all := make([]*modelServer, 0, len(servers))
	for _, s := range servers {
		all = append(all, s)
	}
	serversMu.Unlock()
	for _, s := range all {
		s.mu.Lock()
		if s.idle != nil {
			s.idle.Stop()
		}
		s.stop()
		s.mu.Unlock()
	}
}

// ResidentState describes this process's model servers for status output.
type ResidentState struct {
	// Kind is the model it keeps loaded: "parakeet" or "whisper.cpp".
	Kind     string    `json:"kind"`
	Running  bool      `json:"running"`
	Since    time.Time `json:"since,omitempty"`
	Batches  int       `json:"batches"`
	Starts   int       `json:"starts"`
	Version  string    `json:"version,omitempty"`
	Disabled string    `json:"disabled,omitempty"`
	// PeakMemory is the largest peak memory (bytes) a server reported.
	PeakMemory int64 `json:"peakMemory"`
}

// ResidentStates lists this process's model servers.
func ResidentStates() []ResidentState {
	serversMu.Lock()
	defer serversMu.Unlock()
	out := []ResidentState{}
	for _, s := range servers {
		s.mu.Lock()
		st := ResidentState{Kind: s.kind, Running: s.run != nil, Batches: s.batches, Starts: s.starts, PeakMemory: s.peak}
		if s.run != nil {
			st.Since, st.Version = s.run.since, s.run.hello.Version
		}
		if s.disabled != nil {
			st.Disabled = s.disabled.Error()
		}
		s.mu.Unlock()
		out = append(out, st)
	}
	return out
}
