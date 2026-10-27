package transcribe

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// fakeServerEnv makes the re-executed test binary a fake model server
// ("crash": says hello, then dies on the first batch; "echo": answers every
// batch with one empty result per chunk).
const fakeServerEnv = "METARUSH_TEST_FAKE_MODEL_SERVER"

// TestMain lets this package's tests start model servers: the server child is
// this test binary, re-executed, which serves instead of running the tests.
// It also plays whisper-server and whisper-cli for the whisper.cpp tests
// (whisperfake_test.go).
func TestMain(m *testing.M) {
	fakeWhisperPrograms()
	if ServerChild() {
		if mode := os.Getenv(fakeServerEnv); mode != "" {
			fakeServer(mode)
		}
		o, err := ParseServeArgs(os.Args[1:])
		if err == nil {
			err = ServeStdio(o)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	allowServerInTests = true
	os.Exit(m.Run())
}

func fakeServer(mode string) {
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(serverHello{Protocol: serverProtocol, Ready: true, Version: "fake"})
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var req serverRequest
		_ = json.Unmarshal(sc.Bytes(), &req)
		switch mode {
		case "crash":
			os.Exit(3)
		case "hang":
			select {} // a batch that never finishes
		}
		res := make([]string, len(req.Chunks))
		for i := range res {
			res[i] = `{"text":"","timestamps":[],"durations":[],"tokens":[],"ys_log_probs":[]}`
		}
		_ = enc.Encode(serverReply{ID: req.ID, Results: res, MaxRSS: 1 << 20})
	}
	os.Exit(0)
}
