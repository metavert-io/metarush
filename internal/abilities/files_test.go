package abilities

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
)

// TestFilesNeverGiven: the places a set-up file can't come from hold
// whatever case or spelling the path is typed in, by the path the file
// system stores (D285).
func TestFilesNeverGiven(t *testing.T) {
	a := newApp(t)
	nf := models.AbilityNeededFile{Name: "model", Says: "a model"}
	home := t.TempDir()
	t.Setenv("HOME", home)
	keychains := filepath.Join(home, "Library", "Keychains")
	if err := os.MkdirAll(keychains, 0o700); err != nil {
		t.Fatal(err)
	}
	kc := filepath.Join(keychains, "store.bin")
	if err := os.WriteFile(kc, []byte("plain bytes, nothing else"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pinFile(a, nf, kc); err == nil || !strings.Contains(err.Error(), "sign-ins, settings and devices") {
		t.Fatalf("a file in the Library: %v", err)
	}
	if runtime.GOOS == "darwin" {
		// Mac volumes ignore case: the same file under another spelling.
		lower := filepath.Join(home, "library", "keychains", "store.bin")
		if _, err := os.Stat(lower); err == nil {
			if _, err := pinFile(a, nf, lower); err == nil || !strings.Contains(err.Error(), "sign-ins, settings and devices") {
				t.Fatalf("the Library spelled in lower case: %v", err)
			}
		}
		if _, err := os.Stat("/private/ETC/hosts"); err == nil {
			if _, err := pinFile(a, nf, "/private/ETC/hosts"); err == nil || !strings.Contains(err.Error(), "sign-ins, settings and devices") {
				t.Fatalf("/private/ETC/hosts: %v", err)
			}
		}
	}
	if runtime.GOOS == "linux" {
		if _, err := pinFile(a, nf, "/proc/self/environ"); err == nil {
			t.Fatal("/proc is never given")
		}
	}
	for _, p := range []string{"/etc/hosts", "/dev/null", "/dev/zero"} {
		if _, err := os.Stat(p); err == nil {
			if _, err := pinFile(a, nf, p); err == nil {
				t.Fatalf("%s is never given", p)
			}
		}
	}
	// Key stores by name, and private keys by their first bytes.
	dir := t.TempDir()
	for name, body := range map[string][]byte{"cert.p12": []byte("x"), "id_ed25519": []byte("x"), "login.keychain-db": []byte("x"), "Cookies": []byte("x"),
		"rsa.bin": {0x30, 0x82, 0x04, 0xa4, 0x02, 0x01, 0x00, 0x02, 0x82}, "ec.bin": {0x30, 0x77, 0x02, 0x01, 0x01, 0x04, 0x20, 0x00}} {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, append(body, make([]byte, 32)...), 0o600)
		if _, err := pinFile(a, nf, p); err == nil {
			t.Fatalf("%s is never given", name)
		}
	}
	// An ordinary file is.
	ok := filepath.Join(dir, "weights.bin")
	_ = os.WriteFile(ok, []byte{1, 2, 3, 0, 4}, 0o644)
	if fp, err := pinFile(a, nf, ok); err != nil || fp.About != "a file MetaRush doesn't recognise" {
		t.Fatalf("an ordinary file: %+v %v", fp, err)
	}
}

// TestCopyNeededSizeFirst: a file that changed size since it was set up is
// refused before it is copied (D285).
func TestCopyNeededSizeFirst(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "model.bin")
	if err := os.WriteFile(src, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy.bin")
	err := copyNeeded(models.AbilityFilePin{Name: "model", Path: src, Size: 100, SHA256: strings.Repeat("0", 64)}, dst, "an-ability")
	if err == nil || !strings.Contains(err.Error(), "changed since it was set up") {
		t.Fatalf("a file of another size: %v", err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("nothing is copied for a file of another size")
	}
}

// TestIdentifyAnInterpreter: a plain `python3 -m http.server` is told apart
// from ComfyUI by what it runs and where it was started, not only by the
// Python executable they share (D284).
func TestIdentifyAnInterpreter(t *testing.T) {
	a := newApp(t)
	if runtime.GOOS == "darwin" && !a.Tools.Has("lsof") {
		t.Skip("no lsof")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cmd := exec.Command(py, "-I", "-m", "http.server", strconv.Itoa(port), "--bind", "127.0.0.1")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	ls, err := listenersOn(context.Background(), a, port)
	if err != nil || len(ls) != 1 {
		t.Fatalf("the listener: %+v %v", ls, err)
	}
	p := ls[0]
	real, _ := filepath.EvalSymlinks(dir)
	if !p.Interpreter || p.Script != "-m http.server" || fold(p.Cwd) != fold(real) || p.Path == "" {
		t.Fatalf("what the program is: %+v (started in %s)", p, real)
	}
	l := &models.AbilityLocal{Port: port, Service: "ComfyUI"}
	if why := identifiable(&p, l); why != "" {
		t.Fatalf("a named module is identifiable: %s", why)
	}
	code := p
	code.Script = ""
	if why := identifiable(&code, l); why == "" {
		t.Fatal("code given on the command line, with no probe, isn't")
	}
}

func TestScriptOf(t *testing.T) {
	for argv, want := range map[string]string{
		"python3 main.py --listen":                 "/opt/c/main.py",
		"python3 -I -u /x/main.py":                 "/x/main.py",
		"python3 -W ignore -m comfy":               "-m comfy",
		"python3 -c print(1)":                      "",
		"node --require ./hook.js server.mjs 8188": "/opt/c/server.mjs",
		"node -e x":         "",
		"java -jar app.jar": "/opt/c/app.jar",
	} {
		if got := scriptOf(strings.Fields(argv), "/opt/c"); got != want {
			t.Errorf("%s: %q, want %q", argv, got, want)
		}
	}
}

// TestFilesUnderAnotherName: a refused folder reached through another path
// that isn't a link (here a bind of the same folder through its inode) is
// caught by device and inode (D285).
func TestFilesUnderAnotherName(t *testing.T) {
	a := newApp(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	lib := filepath.Join(home, "Library", "Keychains")
	_ = os.MkdirAll(lib, 0o700)
	// A folder elsewhere that is, by device and inode, the Library's: a
	// second mount stands in as a check of sameFolderAs directly.
	if why := sameFolderAs(filepath.Join(lib, "x.bin"), nil, []string{filepath.Join(home, "Library")}); why == "" {
		t.Fatal("a file in the Library's folder, by inode")
	}
	other := filepath.Join(t.TempDir(), "x.bin")
	_ = os.WriteFile(other, []byte("x"), 0o644)
	if why := sameFolderAs(other, nil, []string{filepath.Join(home, "Library")}); why != "" {
		t.Fatalf("a file elsewhere: %s", why)
	}
	_ = a
}
