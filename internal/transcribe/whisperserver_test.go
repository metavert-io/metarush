package transcribe

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// whisper-cli and whisper-server 1.9.5 transcribing the same clips (an
// English one, and a Chinese one where whisper splits many characters between
// tokens): the server's reply, turned into whisper-cli's JSON, parses into the
// very same transcript.
func TestCLIFromVerboseMatchesWhisperCLI(t *testing.T) {
	for _, name := range []string{"jfk", "zh"} {
		cli, err := os.ReadFile(filepath.Join("testdata", "whisper", name+".cli.json"))
		if err != nil {
			t.Fatal(err)
		}
		verbose, err := os.ReadFile(filepath.Join("testdata", "whisper", name+".server.json"))
		if err != nil {
			t.Fatal(err)
		}
		want, err := parseWhisperJSON(cli, false)
		if err != nil {
			t.Fatal(err)
		}
		b, err := cliFromVerbose(verbose)
		if err != nil {
			t.Fatal(err)
		}
		got, err := parseWhisperJSON(b, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(want.Segments) == 0 || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: the server's transcript differs:\n got %+v\nwant %+v", name, got, want)
		}
		// The Chinese clip has letters split between tokens (more token ids
		// than words in the server's reply), joined the same way by both.
		var v struct {
			Segments []struct {
				Tokens []int `json:"tokens"`
				Words  []any `json:"words"`
			} `json:"segments"`
		}
		_ = json.Unmarshal(verbose, &v)
		ids, words := 0, 0
		for _, s := range v.Segments {
			ids, words = ids+len(s.Tokens), words+len(s.Words)
		}
		if words < 20 || (name == "zh" && ids-words < 10) {
			t.Fatalf("%s: %d tokens in %d words", name, ids, words)
		}
	}
	// Times in whole milliseconds, a token's times only when both are known,
	// probabilities as whisper-cli prints them, the language as its code.
	b, _ := cliFromVerbose(fakeVerbose(42, "auto"))
	var out wcOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	tk := out.Transcription[0].Tokens
	if out.Result.Language != "en" || out.Transcription[0].Offsets.To != 1500 || tk[0].Offsets.To != 410 || tk[0].P != 0.987654 || tk[2].P != 0.123457 ||
		tk[3].Offsets.From != 0 || tk[3].Offsets.To != 0 {
		t.Fatalf("converted: %+v", out)
	}
	if whisperLangCode("cantonese") != "yue" || whisperLangCode("klingon") != "klingon" {
		t.Fatal("language codes")
	}
}

// whisper-server decodes with whisper-cli's settings, listens on loopback
// only, under a random path, with a private folder.
func TestWhisperServerArgs(t *testing.T) {
	a := whisperServerArgs("/m/ggml-large-v3-turbo.bin", 4961, "/abc", "/tmp/p", 6)
	s := strings.Join(a, " ")
	for _, want := range []string{"-m /m/ggml-large-v3-turbo.bin", "--host 127.0.0.1", "--port 4961", "--request-path /abc", "--public /tmp/p", "-t 6",
		"-bs 5", "-bo 5", "-mc 0", "-ml 1000000000", "-nlp"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
}

// fakeWhisper is a whisper.cpp provider whose whisper-cli and whisper-server
// are this test binary (whisperfake_test.go), with a model file of its own.
type fakeWhisper struct {
	p   *whisperCPP
	dir string // what the fake whisper-server wrote
	wav string
}

func newFakeWhisper(t *testing.T, mode, version string) *fakeWhisper {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot find the test binary")
	}
	dir := t.TempDir()
	t.Setenv(fakeWhisperEnv, mode)
	t.Setenv(fakeWhisperDirEnv, dir)
	t.Setenv(fakeWhisperCLIEnv, version)
	model := filepath.Join(t.TempDir(), "ggml-fake.bin")
	if err := os.WriteFile(model, []byte("not a real model"), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := tools.New(config.Tools{WhisperCLI: self, WhisperServer: self})
	p, _ := newWhisperCPP(ts, model, true).(*whisperCPP)
	if p == nil {
		t.Fatal("no whisper.cpp provider")
	}
	t.Cleanup(ReleaseResident)
	return &fakeWhisper{p: p, dir: dir, wav: writeWAV(t, 8000)}
}

// writeWAV writes a 16 kHz mono 16-bit WAV of n samples of silence.
func writeWAV(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, 44+2*n)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*n))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*n))
	// A tone, not silence: words over true silence are dropped (D244).
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(6000*math.Sin(2*math.Pi*220*float64(i)/16000))))
	}
	p := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *fakeWhisper) transcribe(t *testing.T, language string) *Transcript {
	t.Helper()
	tr, err := f.p.Transcribe(context.Background(), f.wav, t.TempDir(), language)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func (f *fakeWhisper) server(t *testing.T) *modelServer {
	t.Helper()
	s, err := whisperServerFor(f.p.t, f.p.model, whisperThreads())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fakeWhisper) pid(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b))
	return pid
}

// gone waits until a process has ended.
func gone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// One whisper-server answers clip after clip with what whisper-cli would have
// written, stops when idle and starts again for the next clip.
func TestWhisperServerKeepsTheModelLoaded(t *testing.T) {
	old := ResidentIdle
	// Long enough that a busy computer (CI runs four test binaries at once)
	// never lets it stop between two clips, short enough to watch it stop.
	ResidentIdle = 3 * time.Second
	t.Cleanup(func() { ResidentIdle = old })
	f := newFakeWhisper(t, "ok", "1.9.5")
	if f.p.Mode() != ModeResident {
		t.Fatalf("mode %s", f.p.Mode())
	}
	first := f.transcribe(t, "fr")
	second := f.transcribe(t, "fr")
	if first.Engine != ModeResident || second.Engine != ModeResident || f.p.Fallback() != "" {
		t.Fatalf("engines %s %s (%s)", first.Engine, second.Engine, f.p.Fallback())
	}
	s := f.server(t)
	if s.starts != 1 || s.batches != 2 || !running(s) {
		t.Fatalf("starts %d batches %d running %v", s.starts, s.batches, running(s))
	}
	sent, _ := os.ReadFile(filepath.Join(f.dir, "sent"))
	st, _ := os.Stat(f.wav)
	if string(sent) != strconv.FormatInt(st.Size(), 10)+" fr verbose_json" {
		t.Fatalf("sent %q", sent)
	}
	// The same transcript whisper-cli gives.
	f.p.resident = false
	cli := f.transcribe(t, "fr")
	f.p.resident = true
	if cli.Engine != ModePerBatch {
		t.Fatalf("cli engine %s", cli.Engine)
	}
	first.Engine, cli.Engine = "", ""
	if !reflect.DeepEqual(first, cli) || len(first.Segments) != 1 || len(first.Segments[0].Words) != 6 {
		t.Fatalf("loaded model %+v\nwhisper-cli %+v", first, cli)
	}
	// whisper-server got whisper-cli's settings, loopback only, a random path.
	var args []string
	b, _ := os.ReadFile(filepath.Join(f.dir, "args.json"))
	_ = json.Unmarshal(b, &args)
	i := slices.Index(args, "--request-path")
	if i < 0 || len(args[i+1]) != 33 || !slices.Contains(args, "-nlp") || args[slices.Index(args, "--host")+1] != "127.0.0.1" {
		t.Fatalf("args %v", args)
	}
	pid := f.pid(t)
	deadline := time.Now().Add(ResidentIdle + 15*time.Second)
	for running(s) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if running(s) || !gone(pid) {
		t.Fatal("the idle model server and its whisper-server were not stopped")
	}
	if tr := f.transcribe(t, "fr"); tr.Engine != ModeResident || s.starts != 2 {
		t.Fatalf("after idle: %s (starts %d)", tr.Engine, s.starts)
	}
	if st := ResidentStates(); !slices.ContainsFunc(st, func(r ResidentState) bool { return r.Kind == EngineWhisper && r.Batches == 3 }) {
		t.Fatalf("states %+v", st)
	}
}

// A whisper-server that dies in the middle of a clip means whisper-cli for
// that clip; after two in a row the model is not kept loaded any more.
func TestWhisperServerCrashMeansWhisperCLI(t *testing.T) {
	f := newFakeWhisper(t, "crash", "1.9.5")
	for i := 0; i < 2; i++ {
		if tr := f.transcribe(t, "auto"); tr.Engine != ModePerBatch || !strings.Contains(f.p.Fallback(), "stopped") {
			t.Fatalf("crash %d: %s (%s)", i, tr.Engine, f.p.Fallback())
		}
	}
	if f.p.Mode() != ModePerBatch {
		t.Fatal("two crashes in a row should turn the loaded model off")
	}
}

// A model that does not load, or another program answering at the address,
// turns the loaded model off at once: whisper-cli makes the transcript.
func TestWhisperServerThatCannotStartMeansWhisperCLI(t *testing.T) {
	for mode, says := range map[string]string{"noload": "failed to initialize whisper context", "impostor": "another program is answering"} {
		t.Run(mode, func(t *testing.T) {
			f := newFakeWhisper(t, mode, "1.9.5")
			if tr := f.transcribe(t, "auto"); tr.Engine != ModePerBatch || !strings.Contains(f.p.Fallback(), says) {
				t.Fatalf("%s (%s)", tr.Engine, f.p.Fallback())
			}
			if f.p.Mode() != ModePerBatch {
				t.Fatal("still resident")
			}
		})
	}
}

// A port another program took while the model loaded: another port.
func TestWhisperServerTriesAnotherPort(t *testing.T) {
	f := newFakeWhisper(t, "taken-once", "1.9.5")
	if tr := f.transcribe(t, "auto"); tr.Engine != ModeResident {
		t.Fatalf("%s (%s)", tr.Engine, f.p.Fallback())
	}
}

// A whisper-server past the memory cap is restarted before the next clip.
func TestWhisperServerRestartsAboveTheMemoryCap(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no memory reading here")
	}
	old := MaxServerMemory
	MaxServerMemory = 1 << 10
	t.Cleanup(func() { MaxServerMemory = old })
	f := newFakeWhisper(t, "ok", "1.9.5")
	f.transcribe(t, "auto")
	s := f.server(t)
	if running(s) || s.peak < 1<<20 {
		t.Fatalf("over the cap it must stop after the clip (peak %d)", s.peak)
	}
	if tr := f.transcribe(t, "auto"); tr.Engine != ModeResident || s.starts != 2 {
		t.Fatalf("next clip: %s (starts %d)", tr.Engine, s.starts)
	}
}

// Canceling a transcription ends the model server and its whisper-server at
// once; so does the model server's input ending (the worker going away).
func TestWhisperServerNeverOutlivesTheModelServer(t *testing.T) {
	f := newFakeWhisper(t, "hang", "1.9.5")
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := f.p.Transcribe(ctx, f.wav, t.TempDir(), "auto"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if !gone(f.pid(t)) {
		t.Fatal("whisper-server outlived a canceled transcription")
	}
	g := newFakeWhisper(t, "ok", "1.9.5")
	g.transcribe(t, "auto")
	pid := g.pid(t)
	ReleaseResident() // closes the model server's input
	if !gone(pid) {
		t.Fatal("whisper-server outlived its model server")
	}
}

// A model server stopped hard (a canceled transcription kills its process
// group) can't remove whisper-server's private folder; the next one removes
// the folders of model servers that are gone, and only those.
func TestWhisperFoldersOfStoppedServersAreRemoved(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	folder := func(name string) string {
		d := filepath.Join(tmp, name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "proof.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return d
	}
	stopped := folder(whisperDirPrefix + strconv.Itoa(1<<22) + "-123") // no such process
	mine := folder(whisperDirPrefix + strconv.Itoa(os.Getpid()) + "-456")
	running := folder(whisperDirPrefix + "1-789")   // process 1 always runs
	older := folder(whisperDirPrefix + "996492109") // an older build's: no process ID in the name
	removeOrphanWhisperDirs()
	if _, err := os.Stat(stopped); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the folder of a model server that is gone is still there: %v", err)
	}
	for _, d := range []string{mine, running, older} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s was removed: %v", filepath.Base(d), err)
		}
	}
	// A whole run with a canceled transcription leaves nothing behind.
	f := newFakeWhisper(t, "hang", "1.9.5")
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := f.p.Transcribe(ctx, f.wav, t.TempDir(), "auto"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	g := newFakeWhisper(t, "ok", "1.9.5")
	g.transcribe(t, "auto")
	ReleaseResident()
	if !gone(g.pid(t)) {
		t.Fatal("whisper-server still runs")
	}
	left, _ := filepath.Glob(filepath.Join(tmp, whisperDirPrefix+"*"))
	for _, d := range left {
		if d != mine && d != running && d != older {
			t.Errorf("left behind: %s", filepath.Base(d))
		}
	}
}

// whisper-cli runs for every clip when the model can't stay loaded: no
// whisper-server, a version MetaRush hasn't checked, or resident off.
func TestWhisperWithoutTheServer(t *testing.T) {
	f := newFakeWhisper(t, "ok", "1.8.2")
	if f.p.Mode() != ModePerBatch {
		t.Fatal("an unchecked version must not keep the model loaded")
	}
	if tr := f.transcribe(t, "auto"); tr.Engine != ModePerBatch || !strings.Contains(f.p.Fallback(), "1.8.2") {
		t.Fatalf("%s (%s)", tr.Engine, f.p.Fallback())
	}
	self, _ := os.Executable()
	ts := tools.New(config.Tools{WhisperCLI: self})
	if ts.Has(tools.WhisperServer) && filepath.Dir(ts.Path(tools.WhisperServer)) != filepath.Dir(self) {
		t.Fatalf("whisper-server from elsewhere: %s", ts.Path(tools.WhisperServer))
	}
	p, _ := newWhisperCPP(ts, f.p.model, true).(*whisperCPP)
	if !ts.Has(tools.WhisperServer) && p.Mode() != ModePerBatch {
		t.Fatal("no whisper-server: whisper-cli per clip")
	}
	off, _ := newWhisperCPP(f.p.t, f.p.model, false).(*whisperCPP)
	if tr, err := off.Transcribe(context.Background(), f.wav, t.TempDir(), "auto"); err != nil || tr.Engine != ModePerBatch || off.Fallback() != "" {
		t.Fatalf("resident off: %v %+v", err, tr)
	}
}

// The peak memory of a process is read from the system (whisper-server's, for
// the memory cap).
func TestProcessPeakMemory(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("not read here")
	}
	if m := processPeakMemory(os.Getpid()); m < 1<<20 || m > 1<<40 {
		t.Fatalf("peak memory %d", m)
	}
	if processPeakMemory(-1) != 0 {
		t.Fatal("no process, no memory")
	}
}
