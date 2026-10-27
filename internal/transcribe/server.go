package transcribe

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/metavert-io/metarush/internal/procio"
	"github.com/metavert-io/metarush/internal/voice"
)

// The model server keeps the Parakeet model loaded in a process of its own
// (D93). The worker starts it (`metarush transcribe-model-server`, hidden) on
// the first transcription, sends it one batch at a time and stops it when it
// has been idle for ResidentIdle or has grown past MaxServerMemory, so the
// memory ONNX Runtime keeps between batches always goes back to the system,
// and a crash inside the C library ends only the server (the transcription
// then runs the CLI as before).
//
// Protocol: JSON lines. The server's first line is a serverHello; then each
// request line gets exactly one reply line with the same id. End of input
// stops the server.

// serverProtocol is the protocol version; a server of another version is
// stopped and the CLI is used.
const serverProtocol = 1

// MaxServerMemory is the peak memory after which the worker restarts the
// model server before its next batch. One batch of 16 chunks of 30 s peaks at
// about 5 GB in a fresh server, as in one CLI process; ONNX Runtime keeps what
// it allocated, and batches of other shapes add to it (measured: 8.9 GB after
// a 31-minute file). An eighth of the machine's memory, between 4.5 GB (a
// fresh server for every full batch: the CLI's peak) and 8 GB.
var MaxServerMemory = serverMemoryCap(physicalMemory())

func serverMemoryCap(ram int64) int64 {
	const lo, hi = 9 << 29, 8 << 30 // 4.5 GB, 8 GB
	return min(hi, max(lo, ram/8))
}

type serverHello struct {
	Protocol int `json:"protocol"`
	// Engine is the model the server keeps loaded: "" (Parakeet) or EngineWhisper.
	Engine  string  `json:"engine,omitempty"`
	Ready   bool    `json:"ready"`
	Version string  `json:"version,omitempty"`
	Load    float64 `json:"loadSeconds,omitempty"`
	Error   string  `json:"error,omitempty"`
}

type serverRequest struct {
	ID     int64    `json:"id"`
	WAV    string   `json:"wav"`
	Chunks [][2]int `json:"chunks"` // sample ranges [lo, hi) of the 16 kHz WAV (Parakeet)
	// Language is whisper.cpp's language for the whole WAV ("auto" detects it).
	Language string `json:"language,omitempty"`
}

type serverReply struct {
	ID      int64    `json:"id"`
	Results []string `json:"results,omitempty"`
	Error   string   `json:"error,omitempty"`
	MaxRSS  int64    `json:"maxRss"` // the server's peak memory so far, bytes
	Seconds float64  `json:"seconds"`
}

// ServeOptions configure a model server.
type ServeOptions struct {
	// Engine is "" (Parakeet through the sherpa-onnx C library) or
	// EngineWhisper (whisper.cpp through whisper-server, D121).
	Engine  string
	Library string // the sherpa-onnx C library (Parakeet)
	Server  string // whisper-server (whisper.cpp)
	Model   string // the Parakeet model folder, or the whisper model file
	Threads int
}

// ServeStdio runs the model server on this process's standard input and a
// private copy of its standard output (`metarush transcribe-model-server`).
func ServeStdio(o ServeOptions) error {
	out, err := procio.ProtocolStdout()
	if err != nil {
		return err
	}
	defer out.Close()
	return ServeModel(o, os.Stdin, out)
}

// ParseServeArgs reads the server's arguments (--engine, --library, --server,
// --model, --threads), for test binaries that serve instead of running tests.
func ParseServeArgs(args []string) (ServeOptions, error) {
	var o ServeOptions
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--engine":
			o.Engine = args[i+1]
		case "--library":
			o.Library = args[i+1]
		case "--server":
			o.Server = args[i+1]
		case "--model":
			o.Model = args[i+1]
		case "--threads":
			fmt.Sscan(args[i+1], &o.Threads)
		default:
			continue
		}
		i++
	}
	return o, o.check()
}

func (o ServeOptions) check() error {
	switch o.Engine {
	case "":
		if o.Library == "" || o.Model == "" {
			return errors.New("--library and --model are required")
		}
	case EngineWhisper:
		if o.Server == "" || o.Model == "" {
			return errors.New("--server and --model are required")
		}
	default:
		return fmt.Errorf("unknown engine %q", o.Engine)
	}
	return nil
}

// ServerChild reports whether this process was started as a model server
// (test binaries check it in TestMain).
func ServerChild() bool { return os.Getenv(serverEnv) == "1" }

// ServeModel loads the model and answers requests from in on out until in
// ends. It is the whole of `metarush transcribe-model-server`.
func ServeModel(o ServeOptions, in io.Reader, out io.Writer) error {
	if err := o.check(); err != nil {
		return err
	}
	if o.Engine == EngineWhisper {
		return serveWhisper(o, in, out)
	}
	enc := json.NewEncoder(out)
	start := time.Now()
	m, err := openLocal(o.Library, o.Model, max(1, o.Threads))
	if err != nil {
		_ = enc.Encode(serverHello{Protocol: serverProtocol, Error: err.Error()})
		return err
	}
	defer m.close()
	if err := enc.Encode(serverHello{Protocol: serverProtocol, Ready: true, Version: libraryVersion(), Load: time.Since(start).Seconds()}); err != nil {
		return err
	}
	var cached struct {
		path string
		size int64
		mod  time.Time
		pcm  []float32
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var req serverRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(serverReply{Error: "bad request: " + err.Error(), MaxRSS: maxRSS()})
			continue
		}
		t0 := time.Now()
		rep := serverReply{ID: req.ID}
		st, err := os.Stat(req.WAV)
		if err == nil && (cached.path != req.WAV || cached.size != st.Size() || !cached.mod.Equal(st.ModTime())) {
			cached.pcm, cached.path = nil, ""
			var pcm []float32
			if pcm, err = voice.ReadWAV(req.WAV); err == nil {
				cached.path, cached.size, cached.mod, cached.pcm = req.WAV, st.Size(), st.ModTime(), pcm
			}
		}
		if err == nil {
			rep.Results, err = decodeRanges(m, cached.pcm, req.Chunks)
		}
		if err != nil {
			rep.Error = err.Error()
		}
		rep.MaxRSS, rep.Seconds = maxRSS(), time.Since(t0).Seconds()
		if err := enc.Encode(rep); err != nil {
			return err
		}
	}
	return sc.Err()
}

func decodeRanges(m *localModel, pcm []float32, ranges [][2]int) ([]string, error) {
	if len(ranges) > batchSize {
		return nil, fmt.Errorf("at most %d chunks per batch, got %d", batchSize, len(ranges))
	}
	chunks := make([][]float32, len(ranges))
	for i, r := range ranges {
		if r[0] < 0 || r[1] < r[0] || r[1] > len(pcm) {
			return nil, errors.New("chunk outside the audio")
		}
		chunks[i] = pcm[r[0]:r[1]]
	}
	return m.decode(chunks)
}
