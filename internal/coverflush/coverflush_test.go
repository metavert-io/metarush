package coverflush_test

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/coverflush"
)

// A coverage build that is asked to stop writes its counters at once, even
// when it never gets to exit by itself (D195).
func TestACoverageBuildWritesItsCountersWhenAskedToStop(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "prog")
	// The program's own main package is instrumented too: without it a
	// coverage build sets nothing up (see the package comment).
	build := exec.Command("go", "build", "-cover", "-covermode=atomic", "-coverpkg=github.com/metavert-io/metarush/internal/coverflush/...", "-o", bin, "./testdata/prog")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -cover: %v\n%s", err, out)
	}
	cov := filepath.Join(dir, "cov")
	if err := os.Mkdir(cov, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+cov)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, _ := bufio.NewReader(out).ReadString('\n'); strings.TrimSpace(line) != "ready" {
		t.Fatalf("the program said %q", line)
	}
	counters := func() []string {
		m, _ := filepath.Glob(filepath.Join(cov, "covcounters.*"))
		return m
	}
	if len(counters()) != 0 {
		t.Fatal("counters were written before anything asked")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(counters()) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if len(counters()) == 0 {
		t.Fatal("no counters after SIGTERM")
	}
	if meta, _ := filepath.Glob(filepath.Join(cov, "covmeta.*")); len(meta) == 0 {
		t.Fatal("no meta-data file")
	}
	// It still runs: the program's own handling of the signal is unchanged.
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the program stopped: %v", err)
	}
}

// Outside a coverage build nothing is written and nothing is watched.
func TestNothingHappensOutsideACoverageBuild(t *testing.T) {
	t.Setenv("GOCOVERDIR", "")
	coverflush.Flush()
	coverflush.OnSignal(syscall.SIGTERM)()
}
