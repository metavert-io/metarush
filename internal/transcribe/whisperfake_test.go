package transcribe

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The whisper.cpp fakes: this test binary, re-executed, plays whisper-server
// (when started with its arguments) and whisper-cli (when asked its version
// or for JSON), so the kept-loaded path can be tested without whisper.cpp.
const (
	// fakeWhisperEnv is the fake whisper-server's mode: "ok" answers; "crash"
	// dies on the first clip; "hang" never answers a clip; "noload" exits at
	// once like a model that does not load; "impostor" serves another folder,
	// so its proof is wrong; "taken-once" finds its port taken the first time.
	fakeWhisperEnv = "METARUSH_TEST_FAKE_WHISPER_SERVER"
	// fakeWhisperDirEnv is a folder where the fake whisper-server writes its
	// arguments (args.json), its process ID (pid) and what it was sent.
	fakeWhisperDirEnv = "METARUSH_TEST_FAKE_WHISPER_DIR"
	// fakeWhisperCLIEnv makes the binary a fake whisper-cli of that version.
	fakeWhisperCLIEnv = "METARUSH_TEST_FAKE_WHISPER_CLI"
)

// fakeWhisperPrograms runs a fake and exits when this process is one.
func fakeWhisperPrograms() {
	args := os.Args[1:]
	if mode := os.Getenv(fakeWhisperEnv); mode != "" && slices.Contains(args, "--request-path") {
		fakeWhisperServer(mode, args)
		os.Exit(0)
	}
	if v := os.Getenv(fakeWhisperCLIEnv); v != "" && (slices.Equal(args, []string{"--version"}) || slices.Contains(args, "-ojf")) {
		fakeWhisperCLI(v, args)
		os.Exit(0)
	}
}

// fakeVerbose is what the fake whisper-server answers for a clip: words that
// say how many bytes were sent and in which language, so a test sees its own
// clip. fakeCLI is the same transcript as whisper-cli writes it.
func fakeVerbose(size int64, language string) []byte {
	b, _ := json.Marshal(map[string]any{"task": "transcribe", "language": "english", "duration": 1.5, "segments": []any{
		map[string]any{"id": 0, "text": fmt.Sprintf(" Clip of %d bytes in %s.", size, language), "start": 0.0, "end": 1.5, "words": []any{
			map[string]any{"word": " Clip", "start": 0.1, "end": 0.41000000000000003, "t_dtw": -1, "probability": 0.98765432},
			map[string]any{"word": " of", "start": 0.41000000000000003, "end": 0.5, "t_dtw": -1, "probability": 0.5},
			map[string]any{"word": fmt.Sprintf(" %d", size), "start": 0.5, "end": 0.9, "t_dtw": -1, "probability": 0.1234567},
			map[string]any{"word": " bytes", "start": -0.01, "end": 1.0, "t_dtw": -1, "probability": 0.9},
			map[string]any{"word": " in", "start": 1.0, "end": 1.1, "t_dtw": -1, "probability": 0.9},
			map[string]any{"word": " " + language, "start": 1.1, "end": 1.4, "t_dtw": -1, "probability": 0.9},
			map[string]any{"word": ".", "start": 1.4, "end": 1.5, "t_dtw": -1, "probability": 0.99},
		}},
	}})
	return b
}

func fakeCLI(size int64, language string) []byte {
	tok := func(text string, from, to int, p float64) map[string]any {
		t := map[string]any{"text": text, "p": p, "t_dtw": -1}
		if from >= 0 {
			t["offsets"] = map[string]any{"from": from, "to": to}
		}
		return t
	}
	b, _ := json.Marshal(map[string]any{"result": map[string]any{"language": "en"}, "transcription": []any{
		map[string]any{"offsets": map[string]any{"from": 0, "to": 1500}, "text": fmt.Sprintf(" Clip of %d bytes in %s.", size, language), "tokens": []any{
			tok("[_BEG_]", 0, 0, 0.7), tok(" Clip", 100, 410, 0.987654), tok(" of", 410, 500, 0.5), tok(fmt.Sprintf(" %d", size), 500, 900, 0.123457),
			tok(" bytes", -1, -1, 0.9), tok(" in", 1000, 1100, 0.9), tok(" "+language, 1100, 1400, 0.9), tok(".", 1400, 1500, 0.99), tok("[_TT_75]", 1500, 1500, 0.5),
		}},
	}})
	return b
}

func fakeWhisperServer(mode string, args []string) {
	arg := func(name string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == name {
				return args[i+1]
			}
		}
		return ""
	}
	dir := os.Getenv(fakeWhisperDirEnv)
	if dir != "" {
		b, _ := json.Marshal(args)
		_ = os.WriteFile(filepath.Join(dir, "args.json"), b, 0o600)
		_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	switch mode {
	case "noload":
		fmt.Fprintln(os.Stderr, "error: failed to initialize whisper context")
		os.Exit(3)
	case "taken-once":
		marker := filepath.Join(dir, "taken")
		if _, err := os.Stat(marker); err != nil {
			_ = os.WriteFile(marker, nil, 0o600)
			fmt.Fprintf(os.Stderr, "\ncouldn't bind to server socket: hostname=127.0.0.1 port=%s\n\n", arg("--port"))
			os.Exit(1)
		}
	}
	path, public := arg("--request-path"), arg("--public")
	if mode == "impostor" {
		public = os.TempDir()
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path+"/health", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"status":"ok"}`) })
	mux.HandleFunc(path+"/inference", func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "crash":
			os.Exit(3)
		case "hang":
			select {}
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, `{"error":"no 'file' field in the request"}`, http.StatusBadRequest)
			return
		}
		n, _ := io.Copy(io.Discard, f)
		if dir != "" {
			_ = os.WriteFile(filepath.Join(dir, "sent"), []byte(fmt.Sprintf("%d %s %s", n, r.FormValue("language"), r.FormValue("response_format"))), 0o600)
		}
		_, _ = w.Write(fakeVerbose(n, r.FormValue("language")))
	})
	mux.Handle("/", http.FileServer(http.Dir(public)))
	ln, err := net.Listen("tcp", "127.0.0.1:"+arg("--port"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "couldn't bind to server socket")
		os.Exit(1)
	}
	_ = http.Serve(ln, mux)
}

func fakeWhisperCLI(version string, args []string) {
	if slices.Equal(args, []string{"--version"}) {
		fmt.Printf("whisper.cpp version: %s\n", version)
		return
	}
	var wav, prefix, language string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-f":
			wav = args[i+1]
		case "-of":
			prefix = args[i+1]
		case "-l":
			language = args[i+1]
		}
	}
	st, err := os.Stat(wav)
	if err != nil || !strings.HasSuffix(wav, ".wav") {
		fmt.Fprintln(os.Stderr, "error: failed to read audio")
		os.Exit(2)
	}
	_ = os.WriteFile(prefix+".json", fakeCLI(st.Size(), language), 0o600)
}
