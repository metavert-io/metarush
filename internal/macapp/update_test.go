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
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The update test (D478): MetaRush.app's updates with the real Sparkle, headless.
// Two test bundles — 0.1.0 installed, 0.2.0 offered — with the Info.plist the
// app ships (desktop/mac/Info.plist: SURequireSignedFeed and
// SUVerifyUpdateBeforeExtraction among its settings), signed ad hoc, carry the
// stand-in metarush (desktop/mac/Tests/MetaRushCoreTests/Fixtures); a key made
// now, and never kept, signs the 0.2.0 archive and the feed this package
// writes, served on a loopback port; AppHarness applies the update as a person
// pressing Install would; then the steps after an update run from the new
// bundle against a temporary home. An unsigned or changed feed is refused
// before any download; a wrong signature before anything is unpacked (the
// app's code signature is no way around it); a changed archive on its
// signature; an older release isn't offered. No real network: the feed and the
// archive are this test's.
//
// Sparkle's installer runs as a launchd job of the person's own session and
// keeps its preferences through cfprefsd, which don't see a test's HOME: the
// test bundles share one identifier made for this test only, the harness has
// its own (desktop/mac/AppHarness-Info.plist, where Sparkle keeps the date a
// signed feed first failed), and what Sparkle leaves under either (their
// preferences, a cache folder) is removed before and after.

const (
	testBundleID    = "io.metarush.app-update-test"
	harnessBundleID = "io.metarush.app-update-test.harness"
	// failedFeedKey is where Sparkle keeps the date a signed feed first failed
	// to check: in the preferences of the program that updates (the harness),
	// named after the app it updates when the two differ (SUAppcastDriver).
	failedFeedKey = "SUInitialFailedFeedSigningValidationDate_" + testBundleID
)

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
// restart; its Info.plist the one the app ships, filled in the way
// scripts/mac-app.sh build does (plutil); signed ad hoc, as a build without a
// Developer ID is. without names settings left out (the control case).
func testBundle(t *testing.T, dir, short, publicKey, feedURL string, without ...string) string {
	t.Helper()
	app := filepath.Join(dir, "MetaRush.app")
	helpers := filepath.Join(app, "Contents", "Helpers")
	answers := filepath.Join(app, "Contents", "Resources", "stand-in") // the stand-in's answers (fake-metarush.sh)
	for _, d := range []string{filepath.Join(app, "Contents", "MacOS"), helpers, answers} {
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
		write(filepath.Join(answers, name+".json"), fixture(f), 0o644)
	}
	write(filepath.Join(answers, "after-restart.json"), fixture("service-status-new.json"), 0o644)
	write(filepath.Join(answers, "service-restart.sh"), []byte(`cp "$D/after-restart.json" "$D/service-status.json"`+"\n"), 0o644)
	bundleVersion, err := BundleVersion(short)
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := os.ReadFile(filepath.Join("..", "..", "desktop", "mac", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	info := filepath.Join(app, "Contents", "Info.plist")
	write(info, shipped, 0o644)
	for key, value := range map[string]string{"CFBundleIdentifier": testBundleID, "CFBundleShortVersionString": short, "CFBundleVersion": bundleVersion,
		"MetaRushCommit": "test", "SUFeedURL": feedURL, "SUPublicEDKey": publicKey} {
		plutil(t, "-replace", key, "-string", value, info)
	}
	for _, key := range without {
		plutil(t, "-remove", key, info)
	}
	// Inside out, as scripts/mac-app.sh signs: the program, then the app.
	for _, p := range []string{filepath.Join(helpers, "metarush"), app} {
		if out, err := exec.Command("codesign", "--force", "--sign", "-", p).CombinedOutput(); err != nil {
			t.Fatalf("codesign %s: %v %s", p, err, out)
		}
	}
	if out, err := exec.Command("codesign", "--verify", "--strict", app).CombinedOutput(); err != nil {
		t.Fatalf("the test app's signature: %v %s", err, out)
	}
	return app
}

func plutil(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("plutil", args...).CombinedOutput(); err != nil {
		t.Fatalf("plutil %v: %v %s", args, err, out)
	}
}

// zipApp archives a bundle the way scripts/mac-app.sh does.
func zipApp(t *testing.T, app, archive string) {
	t.Helper()
	if out, err := exec.Command("ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", app, archive).CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v %s", err, out)
	}
}

// sparkleLeftovers removes what Sparkle keeps under the test bundles' and the
// harness's identifiers in the person's own Library.
func sparkleLeftovers(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		return
	}
	for _, id := range []string{testBundleID, harnessBundleID} {
		_ = exec.Command("defaults", "delete", id).Run()
		_ = os.Remove(filepath.Join(u.HomeDir, "Library", "Preferences", id+".plist"))
		_ = os.RemoveAll(filepath.Join(u.HomeDir, "Library", "Caches", id))
	}
}

// result is the harness's last JSON line.
type result map[string]any

func runHarness(t *testing.T, h, userHome string, args ...string) (result, []result, int) {
	last, all, code, _ := runHarnessSaid(t, h, userHome, args...)
	return last, all, code
}

// runHarnessSaid also gives what the harness said on stderr (the steps
// Sparkle took: "downloading", "extracting", "installing").
func runHarnessSaid(t *testing.T, h, userHome string, args ...string) (result, []result, int, string) {
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
	return all[len(all)-1], all, code, stderr.String()
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
	// The harness keeps its preferences under its own identifier, which this test removes.
	if who, _, _ := runHarness(t, h, t.TempDir(), "identity"); who["bundleIdentifier"] != harnessBundleID {
		t.Fatalf("the harness is %v, not %s: its preferences would land under another name", who, harnessBundleID)
	}
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
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}

	// The feed and the archive, from this test only: what each request gets is set per case.
	var mu sync.Mutex
	var feed, zip []byte
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/appcast.xml":
			_, _ = w.Write(feed)
		case "/MetaRush-0.2.0.zip":
			downloads++
			_, _ = w.Write(zip)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	feedURL := srv.URL + "/appcast.xml"

	newApp := testBundle(t, filepath.Join(dir, "build"), "0.2.0", publicKey, feedURL)
	archive := filepath.Join(dir, "MetaRush-0.2.0.zip")
	zipApp(t, newApp, archive)
	good, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}

	type offered struct {
		short    string
		body     []byte
		signedBy ed25519.PrivateKey // the archive's signature
		feedKey  ed25519.PrivateKey // the feed's (nil: not signed)
		changed  bool               // the feed changed after it was signed
		noEdDSA  bool               // the archive without an EdDSA signature (code signing alone)
	}
	offer := func(o offered) {
		t.Helper()
		bv, _ := BundleVersion(o.short)
		b, err := Feed{Title: "MetaRush", Items: []Item{{Title: "MetaRush " + o.short, ShortVersion: o.short, Version: bv, MinimumSystem: "13.0",
			Published: time.Now(), URL: srv.URL + "/MetaRush-0.2.0.zip", Length: int64(len(o.body)),
			EdSignature: base64.StdEncoding.EncodeToString(ed25519.Sign(o.signedBy, good)), NotesHTML: Notes(string(changelog), o.short)}}}.XML()
		if err != nil {
			t.Fatal(err)
		}
		if o.noEdDSA {
			b = regexp.MustCompile(` sparkle:edSignature="[^"]*"`).ReplaceAll(b, nil)
		}
		if o.feedKey != nil {
			b = SignFeed(b, o.feedKey)
		}
		if o.changed {
			b = bytes.Replace(b, []byte("<title>MetaRush "+o.short+"</title>"), []byte("<title>MetaRush 9.9.9</title>"), 1)
		}
		mu.Lock()
		feed, zip, downloads = b, o.body, 0
		mu.Unlock()
	}
	downloaded := func() int {
		mu.Lock()
		defer mu.Unlock()
		return downloads
	}
	installed := func(name string, without ...string) string {
		t.Helper()
		return testBundle(t, filepath.Join(dir, name), "0.1.0", publicKey, feedURL, without...)
	}
	unchanged := func(t *testing.T, app string) {
		t.Helper()
		if v := plistValue(t, app, "CFBundleShortVersionString"); v != "0.1.0" {
			t.Fatalf("the app changed to %s", v)
		}
	}
	errorCodes := func(r result) []int {
		codes := []int{}
		if c, ok := r["error"].(float64); ok {
			codes = append(codes, int(c))
		}
		if u, ok := r["underlying"].([]any); ok {
			for _, c := range u {
				if f, ok := c.(float64); ok {
					codes = append(codes, int(f))
				}
			}
		}
		return codes
	}
	has := func(codes []int, c int) bool {
		for _, x := range codes {
			if x == c {
				return true
			}
		}
		return false
	}
	const validationError, feedError = 3002, 1000 // SUValidationError, SUAppcastParseError
	// refusedFeed: the feed refused on its signature, nothing downloaded.
	refusedFeed := func(t *testing.T, code int, last result, why string) {
		t.Helper()
		if code != 2 || last["installed"] == true || downloaded() != 0 {
			t.Fatalf("exit %d, %v, %d downloads", code, last, downloaded())
		}
		if c, _ := last["error"].(float64); int(c) != feedError || !has(errorCodes(last), validationError) || !strings.Contains(fmt.Sprint(last["underlyingMessages"]), why) {
			t.Errorf("not refused for %q: %v", why, last)
		}
	}
	// refusedBeforeUnpacking: Sparkle refused the archive on its EdDSA
	// signature before unpacking any of it (no progress from the unarchiver).
	refusedBeforeUnpacking := func(t *testing.T, last result, said string) {
		t.Helper()
		under := fmt.Sprint(last["underlyingMessages"])
		if !has(errorCodes(last), validationError) || !strings.Contains(under, "before unarchiving") {
			t.Errorf("not refused on its signature before unarchiving: %v", last)
		}
		if strings.Contains(said, "unpacking") {
			t.Errorf("the archive was unpacked before its signature was checked:\n%s", said)
		}
	}

	t.Run("an update applies and finishes", func(t *testing.T) {
		offer(offered{short: "0.2.0", body: good, signedBy: priv, feedKey: priv})
		app := installed("installed")
		last, _, code := runHarness(t, h, userHome, "apply-update", app, feedURL)
		if code != 0 || last["installed"] != true {
			t.Fatalf("the update: exit %d, %v", code, last)
		}
		if v := plistValue(t, app, "CFBundleShortVersionString"); v != "0.2.0" {
			t.Fatalf("the installed app says %s", v)
		}
		if b, _ := os.ReadFile(filepath.Join(app, "Contents", "Resources", "stand-in", "version.json")); !bytes.Contains(b, []byte(`"0.2.0"`)) {
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
		calls, _ := os.ReadFile(filepath.Join(app, "Contents", "Resources", "stand-in", "calls.log"))
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

	t.Run("an unsigned feed is refused", func(t *testing.T) {
		offer(offered{short: "0.2.0", body: good, signedBy: priv})
		app := installed("unsigned-feed")
		last, _, code := runHarness(t, h, userHome, "apply-update", app, feedURL)
		refusedFeed(t, code, last, "no EdDSA signature in the appcast")
		unchanged(t, app)
	})

	t.Run("a feed changed after it was signed is refused", func(t *testing.T) {
		offer(offered{short: "0.2.0", body: good, signedBy: priv, feedKey: priv, changed: true})
		app := installed("changed-feed")
		last, _, code := runHarness(t, h, userHome, "apply-update", app, feedURL)
		refusedFeed(t, code, last, "EdDSA signature does not match")
		unchanged(t, app)
	})

	t.Run("a feed signed with another key is refused", func(t *testing.T) {
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		offer(offered{short: "0.2.0", body: good, signedBy: priv, feedKey: other})
		app := installed("other-feed-key")
		last, _, code := runHarness(t, h, userHome, "apply-update", app, feedURL)
		refusedFeed(t, code, last, "EdDSA signature does not match")
		unchanged(t, app)
	})

	t.Run("a wrong signature is refused before anything is unpacked", func(t *testing.T) {
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		offer(offered{short: "0.2.0", body: good, signedBy: other, feedKey: priv})
		app := installed("wrong-key")
		last, _, code, said := runHarnessSaid(t, h, userHome, "apply-update", app, feedURL)
		if code != 2 || last["installed"] == true {
			t.Fatalf("a wrong signature: exit %d, %v", code, last)
		}
		refusedBeforeUnpacking(t, last, said)
		unchanged(t, app)
	})

	t.Run("an update signed only by its code signature is refused before anything is unpacked", func(t *testing.T) {
		offer(offered{short: "0.2.0", body: good, signedBy: priv, feedKey: priv, noEdDSA: true})
		app := installed("code-signature-only")
		last, _, code, said := runHarnessSaid(t, h, userHome, "apply-update", app, feedURL)
		if code != 2 || last["installed"] == true {
			t.Fatalf("no EdDSA signature: exit %d, %v", code, last)
		}
		refusedBeforeUnpacking(t, last, said)
		if !strings.Contains(fmt.Sprint(last["underlyingMessages"]), "no EdDSA signature") {
			t.Errorf("refused, but not for its missing signature: %v", last)
		}
		unchanged(t, app)
	})

	t.Run("without SUVerifyUpdateBeforeExtraction it would be unpacked first", func(t *testing.T) {
		// The control: an app without the setting (nor SURequireSignedFeed, which
		// Sparkle won't take without it) unpacks the update to look for a code
		// signature to fall back on. The app ships with both, so it never does.
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		offer(offered{short: "0.2.0", body: good, signedBy: other, feedKey: priv})
		app := installed("no-verify-first", "SUVerifyUpdateBeforeExtraction", "SURequireSignedFeed")
		last, _, code, said := runHarnessSaid(t, h, userHome, "apply-update", app, feedURL)
		if code == 0 || last["installed"] == true {
			t.Fatalf("a wrong signature, the settings left out: exit %d, %v", code, last)
		}
		if !strings.Contains(said, "unpacking") || strings.Contains(fmt.Sprint(last["underlyingMessages"]), "before unarchiving") {
			t.Errorf("without the setting the archive wasn't unpacked first (the control shows nothing):\n%s\n%v", said, last)
		}
		unchanged(t, app)
	})

	t.Run("a changed archive is refused on its signature", func(t *testing.T) {
		changed := append([]byte(nil), good...)
		changed[len(changed)/2] ^= 0xff
		offer(offered{short: "0.2.0", body: changed, signedBy: priv, feedKey: priv})
		app := installed("changed")
		last, _, code, said := runHarnessSaid(t, h, userHome, "apply-update", app, feedURL)
		if code != 2 || last["installed"] == true {
			t.Fatalf("a changed archive: exit %d, %v\n%s", code, last, said)
		}
		refusedBeforeUnpacking(t, last, said)
		if !strings.Contains(fmt.Sprint(last["underlyingMessages"]), "EdDSA signature does not match") {
			t.Errorf("refused, but not on its signature: %v", last)
		}
		unchanged(t, app)
	})

	// failedSince sets the date a signed feed first failed to check to `days`
	// ago, where Sparkle keeps it (the harness's preferences).
	failedSince := func(t *testing.T, days int) {
		t.Helper()
		when := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02 15:04:05 +0000")
		if out, err := exec.Command("defaults", "write", harnessBundleID, failedFeedKey, "-date", when).CombinedOutput(); err != nil {
			t.Fatalf("defaults write: %v %s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command("defaults", "delete", harnessBundleID, failedFeedKey).Run() })
	}

	t.Run("an unsigned feed is refused even after a month of failed checks", func(t *testing.T) {
		failedSince(t, 31)
		offer(offered{short: "0.2.0", body: good, signedBy: priv})
		app := installed("month-of-failures")
		last, _, code := runHarness(t, h, userHome, "apply-update", app, feedURL)
		refusedFeed(t, code, last, "no EdDSA signature in the appcast")
		unchanged(t, app)
	})

	t.Run("without SUSignedFeedFailureExpirationInterval a month of failures lets an unsigned feed through", func(t *testing.T) {
		// The control: Sparkle's default (20 days) takes an unsigned feed after a
		// month of failed checks ("safe mode"). The app ships with 0, so it never does.
		failedSince(t, 31)
		offer(offered{short: "0.2.0", body: good, signedBy: priv})
		app := installed("default-expiry", "SUSignedFeedFailureExpirationInterval")
		last, _, code := runHarness(t, h, userHome, "apply-update", app, feedURL)
		if code != 0 || last["installed"] != true {
			t.Fatalf("the control took nothing (exit %d, %v): the setting shows no effect", code, last)
		}
	})

	t.Run("an older release isn't offered", func(t *testing.T) {
		offer(offered{short: "0.1.0", body: good, signedBy: priv, feedKey: priv})
		app := testBundle(t, filepath.Join(dir, "newer"), "0.2.0", publicKey, feedURL)
		last, _, code := runHarness(t, h, userHome, "apply-update", app, feedURL)
		if code != 3 || last["notFound"] != true {
			t.Fatalf("an older release: exit %d, %v", code, last)
		}
	})
}

// SignFeed signs as Sparkle's own sign_update does: the tool verifies it, and
// refuses it changed. (The tool comes with the Sparkle the app's package
// fetched; without it, this is skipped.)
func TestSignFeedLikeSignUpdate(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Sparkle's tools are for macOS")
	}
	tool := filepath.Join("..", "..", "desktop", "mac", ".build", "artifacts", "mac", "Sparkle", "bin", "sign_update")
	if _, err := os.Stat(tool); err != nil {
		harness(t) // builds the package, which fetches Sparkle and its tools
		if _, err := os.Stat(tool); err != nil {
			t.Skip("Sparkle's sign_update isn't here")
		}
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader) // made now, never kept
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(priv.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Feed{Title: "MetaRush", Items: []Item{{Title: "MetaRush 0.2.0", ShortVersion: "0.2.0", Version: "0.2.0", Published: time.Now(),
		URL: "https://example.invalid/MetaRush-0.2.0.zip", Length: 10, EdSignature: "c2ln"}}}.XML()
	if err != nil {
		t.Fatal(err)
	}
	signed := SignFeed(b, priv)
	if err := VerifyFeed(signed, base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
	if again := SignFeed(signed, priv); !bytes.Equal(again, signed) {
		t.Error("signing a signed feed again isn't the same feed")
	}
	verify := func(feed []byte) error {
		path := filepath.Join(dir, "appcast.xml")
		if err := os.WriteFile(path, feed, 0o644); err != nil {
			t.Fatal(err)
		}
		return exec.Command(tool, "--verify", "--ed-key-file", keyFile, path).Run()
	}
	if err := verify(signed); err != nil {
		t.Fatalf("sign_update doesn't verify SignFeed's signature: %v", err)
	}
	changed := bytes.Replace(signed, []byte("0.2.0.zip"), []byte("0.2.1.zip"), 1)
	if verify(changed) == nil {
		t.Error("sign_update took a changed feed")
	}
	if VerifyFeed(changed, base64.StdEncoding.EncodeToString(pub)) == nil || VerifyFeed(b, base64.StdEncoding.EncodeToString(pub)) == nil {
		t.Error("VerifyFeed took a changed or unsigned feed")
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if VerifyFeed(signed, base64.StdEncoding.EncodeToString(otherPub)) == nil {
		t.Error("VerifyFeed took another key")
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("archive")))
	if VerifyArchive([]byte("archive"), sig, base64.StdEncoding.EncodeToString(pub)) != nil ||
		VerifyArchive([]byte("archivE"), sig, base64.StdEncoding.EncodeToString(pub)) == nil ||
		VerifyArchive([]byte("archive"), sig, base64.StdEncoding.EncodeToString(otherPub)) == nil {
		t.Error("VerifyArchive")
	}
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
