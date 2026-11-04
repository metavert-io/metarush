package macapp

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The update test (D478): MetaRush.app's updates with the real Sparkle, headless.
// Two test bundles — 0.1.0 installed, 0.2.0 offered — carry the stand-in
// metarush (desktop/mac/Tests/MetaRushCoreTests/Fixtures); a key made now, and
// never kept, signs the 0.2.0 archive; the feed this package writes is served on
// a loopback port; AppHarness applies the update as a person pressing Install
// would; then the steps after an update run from the new bundle against a
// temporary home. A wrong signature and a changed archive are refused, and an
// older release isn't offered. No real network: the feed and the archive are
// this test's.
//
// Sparkle's installer runs as a launchd job of the person's own session and
// keeps its preferences through cfprefsd, which don't see a test's HOME: the
// test bundles share one identifier made for this test only, and what Sparkle
// leaves under it (its preferences and an empty cache folder) is removed
// before and after.

const testBundleID = "io.metarush.app-update-test"

var (
	harnessOnce sync.Once
	harnessPath string
	harnessErr  error
	errNoSwift  = errors.New("swift not installed")
)

// harness builds AppHarness once (or uses METARUSH_APP_HARNESS).
func harness(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("MetaRush.app is a Mac app")
	}
	if testing.Short() {
		t.Skip("builds the app's harness")
	}
	harnessOnce.Do(func() {
		if p := os.Getenv("METARUSH_APP_HARNESS"); p != "" {
			harnessPath = p
			return
		}
		swift, err := exec.LookPath("swift")
		if err != nil {
			harnessErr = errNoSwift
			return
		}
		pkg, _ := filepath.Abs(filepath.Join("..", "..", "desktop", "mac"))
		if out, err := exec.Command(swift, "build", "--package-path", pkg, "--product", "AppHarness").CombinedOutput(); err != nil {
			harnessErr = fmt.Errorf("swift build: %v\n%s", err, lastLines(out, 30))
			return
		}
		out, err := exec.Command(swift, "build", "--package-path", pkg, "--product", "AppHarness", "--show-bin-path").Output()
		if err != nil {
			harnessErr = err
			return
		}
		harnessPath = filepath.Join(strings.TrimSpace(string(out)), "AppHarness")
	})
	if errors.Is(harnessErr, errNoSwift) {
		t.Skip("swift not installed")
	}
	if harnessErr != nil {
		t.Fatal(harnessErr)
	}
	return harnessPath
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

const fixturesDir = "../../desktop/mac/Tests/MetaRushCoreTests/Fixtures"

// testBundle makes a MetaRush.app of a version: its program the stand-in,
// answering as a build of that version (version-1 or version-2), the steps
// after an update answered, MetaRush running the older build until its
// restart.
func testBundle(t *testing.T, dir, short, publicKey string) string {
	t.Helper()
	app := filepath.Join(dir, "MetaRush.app")
	helpers := filepath.Join(app, "Contents", "Helpers")
	for _, d := range []string{filepath.Join(app, "Contents", "MacOS"), helpers, filepath.Join(app, "Contents", "Resources")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, b []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, b, mode); err != nil {
			t.Fatal(err)
		}
	}
	fixture := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	write(filepath.Join(app, "Contents", "MacOS", "MetaRush"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	write(filepath.Join(helpers, "metarush"), fixture("fake-metarush.sh"), 0o755)
	versionFixture := map[string]string{"0.1.0": "version-1.json", "0.2.0": "version-2.json"}[short]
	for name, f := range map[string]string{"version": versionFixture, "service-status": "service-status-old.json", "skills-dry": "skills-dry-there.json",
		"skills-install": "skills-install.json", "renderer-install": "renderer-install.json", "migrate": "migrate.json", "service-restart": "service-restart.json"} {
		write(filepath.Join(helpers, name+".json"), fixture(f), 0o644)
	}
	write(filepath.Join(helpers, "after-restart.json"), fixture("service-status-new.json"), 0o644)
	write(filepath.Join(helpers, "service-restart.sh"), []byte(`cp "$D/after-restart.json" "$D/service-status.json"`+"\n"), 0o644)
	bundleVersion, err := BundleVersion(short)
	if err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>MetaRush</string>
<key>CFBundleIdentifier</key><string>` + testBundleID + `</string>
<key>CFBundleName</key><string>MetaRush</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>` + short + `</string>
<key>CFBundleVersion</key><string>` + bundleVersion + `</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>SUPublicEDKey</key><string>` + publicKey + `</string>
</dict></plist>
`
	write(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644)
	return app
}

// zipApp archives a bundle the way scripts/mac-app.sh does.
func zipApp(t *testing.T, app, archive string) {
	t.Helper()
	if out, err := exec.Command("ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", app, archive).CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v %s", err, out)
	}
}

// sparkleLeftovers removes what Sparkle keeps under the test's bundle
// identifier in the person's own Library.
func sparkleLeftovers(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		return
	}
	_ = exec.Command("defaults", "delete", testBundleID).Run()
	_ = os.Remove(filepath.Join(u.HomeDir, "Library", "Preferences", testBundleID+".plist"))
	_ = os.RemoveAll(filepath.Join(u.HomeDir, "Library", "Caches", testBundleID))
}

// result is the harness's last JSON line.
type result map[string]any

func runHarness(t *testing.T, h, userHome string, args ...string) (result, []result, int) {
	t.Helper()
	cmd := exec.Command(h, args...)
	cmd.Env = append(os.Environ(), "HOME="+userHome, "CFFIXED_USER_HOME="+userHome)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	var all []result
	sc := bufio.NewScanner(&stdout)
	for sc.Scan() {
		var r result
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			all = append(all, r)
		}
	}
	if len(all) == 0 {
		t.Fatalf("%v: no answer (exit %d)\n%s", args, code, stderr.String())
	}
	return all[len(all)-1], all, code
}

func plistValue(t *testing.T, app, key string) string {
	t.Helper()
	out, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :"+key, filepath.Join(app, "Contents", "Info.plist")).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestUpdateWithSparkle(t *testing.T) {
	h := harness(t)
	sparkleLeftovers(t)
	t.Cleanup(func() { sparkleLeftovers(t) })

	pub, priv, err := ed25519.GenerateKey(rand.Reader) // made now, never kept
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.StdEncoding.EncodeToString(pub)
	dir := t.TempDir()
	userHome := filepath.Join(dir, "user")
	if err := os.MkdirAll(userHome, 0o755); err != nil {
		t.Fatal(err)
	}
	newApp := testBundle(t, filepath.Join(dir, "build"), "0.2.0", publicKey)
	archive := filepath.Join(dir, "MetaRush-0.2.0.zip")
	zipApp(t, newApp, archive)
	good, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}

	// The feed and the archive, from this test only: what each request gets is set per case.
	var mu sync.Mutex
	var feed, zip []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/appcast.xml":
			_, _ = w.Write(feed)
		case "/MetaRush-0.2.0.zip":
			_, _ = w.Write(zip)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	offer := func(short string, body []byte, signedBy ed25519.PrivateKey) {
		t.Helper()
		bv, _ := BundleVersion(short)
		b, err := Feed{Title: "MetaRush", Items: []Item{{Title: "MetaRush " + short, ShortVersion: short, Version: bv, MinimumSystem: "13.0",
			Published: time.Now(), URL: srv.URL + "/MetaRush-0.2.0.zip", Length: int64(len(body)),
			EdSignature: base64.StdEncoding.EncodeToString(ed25519.Sign(signedBy, good)), NotesHTML: Notes(string(changelog), short)}}}.XML()
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		feed, zip = b, body
		mu.Unlock()
	}
	installed := func(name string) string {
		t.Helper()
		return testBundle(t, filepath.Join(dir, name), "0.1.0", publicKey)
	}

	t.Run("an update applies and finishes", func(t *testing.T) {
		offer("0.2.0", good, priv)
		app := installed("installed")
		last, _, code := runHarness(t, h, userHome, "apply-update", app, srv.URL+"/appcast.xml")
		if code != 0 || last["installed"] != true {
			t.Fatalf("the update: exit %d, %v", code, last)
		}
		if v := plistValue(t, app, "CFBundleShortVersionString"); v != "0.2.0" {
			t.Fatalf("the installed app says %s", v)
		}
		if b, _ := os.ReadFile(filepath.Join(app, "Contents", "Helpers", "version.json")); !bytes.Contains(b, []byte(`"0.2.0"`)) {
			t.Fatalf("the new app's metarush isn't the new build: %s", b)
		}

		// The steps after an update, from the new bundle's own program, against a temporary home.
		home := filepath.Join(dir, "home")
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		last, all, code := runHarness(t, h, userHome, "finish", app, "--home", home)
		if code != 0 || last["finished"] != true || last["version"] != "0.2.0" || last["commit"] != "bbbbbbb" {
			t.Fatalf("finishing: exit %d, %v", code, all)
		}
		steps, _ := os.ReadFile(filepath.Join(home, "stand-in-steps.log"))
		want := "version\nservice-status\nskills-dry\nskills-install\nrenderer-install\nmigrate\nservice-status\nservice-restart\nservice-status\n"
		if string(steps) != want {
			t.Errorf("the steps against the home:\n%s\nwant\n%s", steps, want)
		}
		calls, _ := os.ReadFile(filepath.Join(app, "Contents", "Helpers", "calls.log"))
		for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
			if !strings.HasSuffix(line, "--json --home "+home) {
				t.Errorf("a step without the home: %q", line)
			}
		}
		done := map[string]string{}
		for _, r := range all {
			if s, ok := r["step"].(string); ok {
				done[s], _ = r["state"].(string)
			}
		}
		for _, s := range []string{"skills", "renderer", "records", "restart", "check"} {
			if done[s] != "done" {
				t.Errorf("step %s: %q (%v)", s, done[s], all)
			}
		}
	})

	t.Run("a wrong signature is refused", func(t *testing.T) {
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		offer("0.2.0", good, other)
		app := installed("wrong-key")
		last, _, code := runHarness(t, h, userHome, "apply-update", app, srv.URL+"/appcast.xml")
		if code != 2 || last["installed"] == true {
			t.Fatalf("a wrong signature: exit %d, %v", code, last)
		}
		if !strings.Contains(fmt.Sprint(last["message"]), "signed") {
			t.Errorf("the refusal: %v", last)
		}
		if v := plistValue(t, app, "CFBundleShortVersionString"); v != "0.1.0" {
			t.Fatalf("the app changed to %s", v)
		}
	})

	t.Run("a changed archive is refused", func(t *testing.T) {
		changed := append([]byte(nil), good...)
		changed[len(changed)/2] ^= 0xff
		offer("0.2.0", changed, priv)
		app := installed("changed")
		last, _, code := runHarness(t, h, userHome, "apply-update", app, srv.URL+"/appcast.xml")
		if code == 0 || last["installed"] == true {
			t.Fatalf("a changed archive: exit %d, %v", code, last)
		}
		if v := plistValue(t, app, "CFBundleShortVersionString"); v != "0.1.0" {
			t.Fatalf("the app changed to %s", v)
		}
	})

	t.Run("an older release isn't offered", func(t *testing.T) {
		offer("0.1.0", good, priv)
		app := testBundle(t, filepath.Join(dir, "newer"), "0.2.0", publicKey)
		last, _, code := runHarness(t, h, userHome, "apply-update", app, srv.URL+"/appcast.xml")
		if code != 3 || last["notFound"] != true {
			t.Fatalf("an older release: exit %d, %v", code, last)
		}
	})
}

// The real Sparkle orders bundle versions as the port in macapp_test.go does,
// so TestBundleVersionsOrderLikeMetaRush holds for the real thing too.
func TestSparkleOrdersBundleVersionsLikeThePort(t *testing.T) {
	h := harness(t)
	args := []string{"compare"}
	var want []int
	for i, a := range orderingCases {
		for _, b := range orderingCases[i+1:] {
			ba, _ := BundleVersion(a)
			bb, _ := BundleVersion(b)
			args = append(args, ba, bb)
			want = append(want, sparkleCompare(ba, bb))
		}
	}
	last, _, code := runHarness(t, h, t.TempDir(), args...)
	got, _ := last["orders"].([]any)
	if code != 0 || len(got) != len(want) {
		t.Fatalf("compare: exit %d, %v", code, last)
	}
	for i := range want {
		if int(got[i].(float64)) != want[i] {
			t.Errorf("%s vs %s: Sparkle %v, the port %d", args[1+2*i], args[2+2*i], got[i], want[i])
		}
	}
}
