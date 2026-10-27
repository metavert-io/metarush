package transcribe

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// whisper.cpp kept loaded (D121). A typical whisper.cpp install is one static
// whisper-cli program with no library MetaRush could load, so the model stays
// loaded in whisper-server, whisper.cpp's own HTTP server from the same build.
// The worker never talks to it directly: it starts the model server
// (`metarush transcribe-model-server --engine whisper.cpp`) with the same
// JSON-line protocol, idle stop, memory cap, crash rule and fallback as
// Parakeet's (client.go), and the model server runs whisper-server in its own
// process group, so stopping the model server stops it, and so does the worker
// going away (the model server's input ends).
//
// whisper-server listens on 127.0.0.1 only, on a free port, under a random
// path: it answers nothing else, so a web page can't use it although it sends
// CORS headers. It serves one file, from a private folder: a random word that
// proves, before each clip is sent, that the program answering is this
// whisper-server (another account can't stand in for it). It decodes with the
// settings whisper-cli runs with (whisperServerArgs), and its verbose JSON is
// turned back into the JSON whisper-cli writes (cliFromVerbose), so the same
// parser makes the same transcript (TestWhisperResidentMatchesCLI).

// EngineWhisper is the model server engine that keeps whisper.cpp loaded.
const EngineWhisper = "whisper.cpp"

// whisperResidentVersions are the whisper.cpp versions whose whisper-server
// MetaRush checked against whisper-cli: the same decoding settings with the
// arguments below, the same samples, the same words, and a decoder whose
// random generator starts over for every clip (before 1.9.5 it carried over
// from one request to the next, so a loaded model could write the same audio
// down differently the second time). Add a version only after comparing its
// examples/server/server.cpp with examples/cli/cli.cpp and running
// TestWhisperResidentMatchesCLI (-tags voiceeval).
var whisperResidentVersions = map[string]bool{"1.9.5": true}

// whisperServerFor returns the model server that keeps a whisper model loaded
// (not started until its first clip), or ErrResidentUnavailable saying why
// whisper-cli runs for each clip instead.
func whisperServerFor(t *tools.Set, model string, threads int) (*modelServer, error) {
	if testing.Testing() && !allowServerInTests {
		return nil, fmt.Errorf("%w: not under go test", ErrResidentUnavailable)
	}
	if t == nil || !t.Has(tools.Self) {
		return nil, fmt.Errorf("%w: cannot find this MetaRush binary", ErrResidentUnavailable)
	}
	if !t.Has(tools.WhisperServer) {
		return nil, fmt.Errorf("%w: whisper-server isn't installed next to whisper-cli", ErrResidentUnavailable)
	}
	if v := t.Version(context.Background(), tools.Whisper); !whisperResidentVersions[v] {
		return nil, fmt.Errorf("%w: whisper.cpp %s is a version MetaRush hasn't checked for this (it knows 1.9.5)", ErrResidentUnavailable, v)
	}
	st, err := os.Stat(model)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrResidentUnavailable, err)
	}
	server := t.Path(tools.WhisperServer)
	key := fmt.Sprintf("%s|%s|%s|%s|%d", EngineWhisper, t.Path(tools.Self), server, model, threads)
	return registerServer(key, func() *modelServer {
		return &modelServer{t: t, kind: EngineWhisper, engine: EngineWhisper, modelBytes: st.Size(),
			args: []string{ServerCommand, "--engine", EngineWhisper, "--server", server, "--model", model, "--threads", strconv.Itoa(threads)}}
	}), nil
}

// decodeResident transcribes the clip with the model kept loaded and returns
// the JSON whisper-cli would have written. Anything that goes wrong with the
// loaded model (it stopped, it answered with an error) is
// ErrResidentUnavailable: whisper-cli then runs for this clip.
func (w *whisperCPP) decodeResident(ctx context.Context, wav, language string, threads int) ([]byte, error) {
	s, err := whisperServerFor(w.t, w.model, threads)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(wav)
	if err != nil {
		return nil, err
	}
	rep, err := s.call(ctx, serverRequest{WAV: abs, Language: language})
	if err != nil {
		return nil, err
	}
	if rep.Error != "" {
		return nil, fmt.Errorf("%w: %s", ErrResidentUnavailable, rep.Error)
	}
	if len(rep.Results) != 1 {
		return nil, fmt.Errorf("%w: the model server returned %d results for one clip", ErrResidentUnavailable, len(rep.Results))
	}
	return []byte(rep.Results[0]), nil
}

// whisperServerArgs are whisper-server's arguments: where it listens, and the
// decoding settings whisper-cli uses with MetaRush's flags. whisper-server's
// own defaults differ (best of 2 without beam search, lines cut at 60
// characters, a second language pass for its reply), so they are all spelled
// out: beam search 5 and best of 5 (whisper-cli's defaults), no carried text
// (-mc 0, as MetaRush runs whisper-cli), lines never cut (a length no line
// reaches: whisper-cli doesn't cut them), no language probabilities, and
// DTW token times as whisper-cli gets them (D244).
func whisperServerArgs(model string, port int, path, public string, threads int) []string {
	return append([]string{"-m", model, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--request-path", path, "--public", public,
		"-t", strconv.Itoa(threads), "-bs", "5", "-bo", "5", "-mc", "0", "-ml", "1000000000", "-nlp"}, dtwArgs(model)...)
}

// whisperProc is whisper-server as the model server runs it.
type whisperProc struct {
	origin    string // http://127.0.0.1:<port>
	base      string // origin + the random path every request goes under
	proof     string // the private file's name under origin
	nonce     string // and what it says
	public    string // the private folder it serves
	client    *http.Client
	pid       int
	done      chan struct{}
	err       error
	cancel    context.CancelFunc
	startedAt time.Time
}

// errPortTaken means another program took the port before whisper-server
// could listen on it (it loads the model first): try another port.
var errPortTaken = errors.New("the port was taken")

// startWhisper starts whisper-server and waits until it has loaded the model
// and proved it is the one answering.
func startWhisper(o ServeOptions) (*whisperProc, error) {
	ts := tools.New(config.Tools{WhisperServer: o.Server})
	if !ts.Has(tools.WhisperServer) {
		return nil, fmt.Errorf("whisper-server is not at %s", o.Server)
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var w *whisperProc
		if w, err = launchWhisper(ts, o); err == nil {
			return w, nil
		}
		if !errors.Is(err, errPortTaken) {
			break
		}
	}
	return nil, err
}

// whisperDirPrefix names whisper-server's private folders in the temp folder:
// metarush-whisper-<model server's process ID>-<random>. A model server that
// is stopped hard (a canceled transcription kills its process group) can't
// remove its own, so each new one removes those of model servers that are gone.
const whisperDirPrefix = "metarush-whisper-"

// removeOrphanWhisperDirs removes the private folders of model servers that no
// longer run. Folders without a process ID in their name (older builds) are
// left alone: their model server may still be running.
func removeOrphanWhisperDirs() {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), whisperDirPrefix+"*-*"))
	for _, d := range dirs {
		rest := strings.TrimPrefix(filepath.Base(d), whisperDirPrefix)
		pidText, _, ok := strings.Cut(rest, "-")
		pid, err := strconv.Atoi(pidText)
		if !ok || err != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			_ = os.RemoveAll(d)
		}
	}
}

func launchWhisper(ts *tools.Set, o ServeOptions) (*whisperProc, error) {
	removeOrphanWhisperDirs()
	public, err := os.MkdirTemp("", whisperDirPrefix+strconv.Itoa(os.Getpid())+"-") // 0700
	if err != nil {
		return nil, err
	}
	w := &whisperProc{public: public, proof: randomHex(16) + ".txt", nonce: randomHex(16), done: make(chan struct{}), startedAt: time.Now(),
		client: &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}}
	if err := os.WriteFile(filepath.Join(public, w.proof), []byte(w.nonce), 0o600); err != nil {
		_ = os.RemoveAll(public)
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		_ = os.RemoveAll(public)
		return nil, err
	}
	path := "/" + randomHex(16)
	w.origin = "http://127.0.0.1:" + strconv.Itoa(port)
	w.base = w.origin + path
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	pid := make(chan int, 1)
	go func() {
		_, err := ts.Run(ctx, tools.Spec{Tool: tools.WhisperServer, Args: whisperServerArgs(o.Model, port, path, public, max(1, o.Threads)),
			Stdout: io.Discard, Background: true, SameGroup: true, OnStart: func(p int) { pid <- p }})
		w.err = err
		close(w.done)
	}()
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case p := <-pid:
			w.pid = p
			continue
		case <-w.done:
			w.close()
			var ef *tools.ErrFailed
			if errors.As(w.err, &ef) && strings.Contains(ef.Stderr, "couldn't bind") {
				return nil, errPortTaken
			}
			return nil, fmt.Errorf("whisper-server did not start: %v", w.err)
		case <-deadline.C:
			w.close()
			return nil, errors.New("whisper-server did not load the model within 3 minutes")
		case <-tick.C:
		}
		if w.healthy() {
			break
		}
	}
	if w.pid == 0 {
		select {
		case p := <-pid: // sent when the program started, before it could answer
			w.pid = p
		default:
		}
	}
	if err := w.verify(); err != nil {
		w.close()
		return nil, err
	}
	return w, nil
}

// healthy reports whether whisper-server answers on its path (it listens only
// once the model is loaded).
func (w *whisperProc) healthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, w.base+"/health", nil)
	resp, err := w.client.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// verify checks that the program answering on the port is this whisper-server:
// only it can read the private file whose word it must return.
func (w *whisperProc) verify() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, w.origin+"/"+w.proof, nil)
	resp, err := w.client.Do(req)
	if err == nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK && string(b) == w.nonce {
			return nil
		}
	}
	return errors.New("another program is answering at whisper-server's address")
}

// transcribe sends one WAV and returns the JSON whisper-cli would have written.
func (w *whisperProc) transcribe(wav, language string) ([]byte, error) {
	if language == "" {
		language = "auto"
	}
	if err := w.verify(); err != nil {
		return nil, err
	}
	f, err := os.Open(wav)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// The form: the reply format, the language and the file, sent with its
	// length (the WAV streams from disk).
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	_ = mw.WriteField("response_format", "verbose_json")
	_ = mw.WriteField("language", language)
	if _, err := mw.CreateFormFile("file", "audio.wav"); err != nil {
		return nil, err
	}
	n := form.Len()
	_ = mw.Close()
	head, tail := form.Bytes()[:n], form.Bytes()[n:]
	req, err := http.NewRequest(http.MethodPost, w.base+"/inference", io.MultiReader(bytes.NewReader(head), f, bytes.NewReader(tail)))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(head)) + st.Size() + int64(len(tail))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whisper-server answered %s: %s", resp.Status, strings.TrimSpace(string(b[:min(len(b), 300)])))
	}
	return cliFromVerbose(b)
}

// exited reports whether whisper-server has stopped, waiting up to d.
func (w *whisperProc) exited(d time.Duration) bool {
	select {
	case <-w.done:
		return true
	case <-time.After(d):
		return false
	}
}

// close stops whisper-server and removes its private folder.
func (w *whisperProc) close() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
	}
	_ = os.RemoveAll(w.public)
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// serveWhisper is the model server for whisper.cpp: it starts whisper-server,
// says hello, then sends each requested WAV to it, one at a time, until its
// input ends (whisper-server is stopped with it). If whisper-server stops,
// the model server ends without a reply, so the worker counts a crash and
// runs whisper-cli for that clip.
func serveWhisper(o ServeOptions, in io.Reader, out io.Writer) error {
	enc := json.NewEncoder(out)
	hello := serverHello{Protocol: serverProtocol, Engine: EngineWhisper}
	start := time.Now()
	w, err := startWhisper(o)
	if err != nil {
		hello.Error = err.Error()
		_ = enc.Encode(hello)
		return err
	}
	defer w.close()
	hello.Ready, hello.Load = true, time.Since(start).Seconds()
	if err := enc.Encode(hello); err != nil {
		return err
	}
	lines := make(chan []byte)
	go func() {
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			lines <- append([]byte(nil), sc.Bytes()...)
		}
		close(lines)
	}()
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return nil
			}
			var req serverRequest
			if err := json.Unmarshal(l, &req); err != nil {
				_ = enc.Encode(serverReply{Error: "bad request: " + err.Error()})
				continue
			}
			t0 := time.Now()
			rep := serverReply{ID: req.ID}
			res, err := w.transcribe(req.WAV, req.Language)
			if err != nil && w.exited(2*time.Second) {
				return fmt.Errorf("whisper-server stopped: %v", w.err)
			}
			if err != nil {
				rep.Error = err.Error()
			} else {
				rep.Results = []string{string(res)}
			}
			rep.MaxRSS, rep.Seconds = processPeakMemory(w.pid), time.Since(t0).Seconds()
			if err := enc.Encode(rep); err != nil {
				return err
			}
		case <-w.done:
			return fmt.Errorf("whisper-server stopped: %v", w.err)
		}
	}
}

// verboseOut is the part of whisper-server's verbose JSON reply MetaRush reads.
type verboseOut struct {
	Language string `json:"language"`
	Segments []struct {
		Text  string   `json:"text"`
		Start *float64 `json:"start"`
		End   *float64 `json:"end"`
		Words []struct {
			Word        string   `json:"word"`
			Start       *float64 `json:"start"`
			End         *float64 `json:"end"`
			Probability float64  `json:"probability"`
			TDTW        *int     `json:"t_dtw"`
		} `json:"words"`
	} `json:"segments"`
}

// cliFromVerbose turns whisper-server's verbose JSON into the JSON whisper-cli
// writes with -ojf, field by field as whisper-cli prints them: times in whole
// milliseconds (the server prints seconds), a token's times only when both are
// known, its probability with six significant digits (whisper-cli prints
// floats that way), the language as its code. Both programs join the pieces
// of a letter split between tokens the same way; the server leaves out
// whisper's special tokens, which the parser skips anyway.
func cliFromVerbose(b []byte) ([]byte, error) {
	var v verboseOut
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("parse whisper-server JSON: %w", err)
	}
	ms := func(p *float64) int {
		if p == nil {
			return 0
		}
		return int(math.Round(*p * 1000))
	}
	var out wcOut
	out.Result.Language = whisperLangCode(v.Language)
	out.Transcription = []wcSegment{}
	for _, s := range v.Segments {
		seg := wcSegment{Text: s.Text, Tokens: []wcToken{}}
		seg.Offsets.From, seg.Offsets.To = ms(s.Start), ms(s.End)
		for _, w := range s.Words {
			tk := wcToken{Text: w.Word, P: cliFloat(w.Probability), TDTW: -1}
			if w.TDTW != nil {
				tk.TDTW = *w.TDTW
			}
			if w.Start != nil && w.End != nil && *w.Start >= 0 && *w.End >= 0 {
				tk.Offsets.From, tk.Offsets.To = ms(w.Start), ms(w.End)
			}
			seg.Tokens = append(seg.Tokens, tk)
		}
		out.Transcription = append(out.Transcription, seg)
	}
	return json.Marshal(out)
}

// cliFloat is v as whisper-cli prints a float (C++ streams: six significant digits).
func cliFloat(v float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 6, 64), 64)
	return f
}

// whisperLangCode turns whisper's language name ("english") into its code
// ("en"), as whisper-cli prints it; anything else is kept.
func whisperLangCode(name string) string {
	if c, ok := whisperLangs[name]; ok {
		return c
	}
	return name
}

// whisperLangs is whisper.cpp's language table (src/whisper.cpp g_lang): the
// names whisper-server prints and the codes whisper-cli prints.
var whisperLangs = map[string]string{
	"english": "en", "chinese": "zh", "german": "de", "spanish": "es", "russian": "ru", "korean": "ko",
	"french": "fr", "japanese": "ja", "portuguese": "pt", "turkish": "tr", "polish": "pl", "catalan": "ca",
	"dutch": "nl", "arabic": "ar", "swedish": "sv", "italian": "it", "indonesian": "id", "hindi": "hi",
	"finnish": "fi", "vietnamese": "vi", "hebrew": "he", "ukrainian": "uk", "greek": "el", "malay": "ms",
	"czech": "cs", "romanian": "ro", "danish": "da", "hungarian": "hu", "tamil": "ta", "norwegian": "no",
	"thai": "th", "urdu": "ur", "croatian": "hr", "bulgarian": "bg", "lithuanian": "lt", "latin": "la",
	"maori": "mi", "malayalam": "ml", "welsh": "cy", "slovak": "sk", "telugu": "te", "persian": "fa",
	"latvian": "lv", "bengali": "bn", "serbian": "sr", "azerbaijani": "az", "slovenian": "sl", "kannada": "kn",
	"estonian": "et", "macedonian": "mk", "breton": "br", "basque": "eu", "icelandic": "is", "armenian": "hy",
	"nepali": "ne", "mongolian": "mn", "bosnian": "bs", "kazakh": "kk", "albanian": "sq", "swahili": "sw",
	"galician": "gl", "marathi": "mr", "punjabi": "pa", "sinhala": "si", "khmer": "km", "shona": "sn",
	"yoruba": "yo", "somali": "so", "afrikaans": "af", "occitan": "oc", "georgian": "ka", "belarusian": "be",
	"tajik": "tg", "sindhi": "sd", "gujarati": "gu", "amharic": "am", "yiddish": "yi", "lao": "lo",
	"uzbek": "uz", "faroese": "fo", "haitian creole": "ht", "pashto": "ps", "turkmen": "tk", "nynorsk": "nn",
	"maltese": "mt", "sanskrit": "sa", "luxembourgish": "lb", "myanmar": "my", "tibetan": "bo", "tagalog": "tl",
	"malagasy": "mg", "assamese": "as", "tatar": "tt", "hawaiian": "haw", "lingala": "ln", "hausa": "ha",
	"bashkir": "ba", "javanese": "jw", "sundanese": "su", "cantonese": "yue",
}
