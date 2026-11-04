package macapp

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sparkleTools is the folder of Sparkle's own tools (sign_update,
// generate_keys) that came with the Sparkle the app's package fetched.
func sparkleTools(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("MetaRush.app is a Mac app")
	}
	dir, _ := filepath.Abs(filepath.Join("..", "..", "desktop", "mac", ".build", "artifacts", "mac", "Sparkle"))
	if _, err := os.Stat(filepath.Join(dir, "bin", "sign_update")); err != nil {
		harness(t) // builds the package, which fetches Sparkle and its tools
		if _, err := os.Stat(filepath.Join(dir, "bin", "sign_update")); err != nil {
			t.Skip("Sparkle's tools aren't here")
		}
	}
	return dir
}

// runScript runs scripts/mac-app.sh with Sparkle's tools from tools.
func runScript(t *testing.T, tools string, stdin []byte, args ...string) (string, error) {
	t.Helper()
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	cmd := exec.Command("bash", append([]string{filepath.Join(root, "scripts", "mac-app.sh")}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "METARUSH_SPARKLE_TOOLS="+tools, "METARUSH_SPARKLE_PUBLIC_KEY=", "METARUSH_SIGN_IDENTITY=")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// scripts/mac-app.sh appcast signs the archive and the feed (SURequireSignedFeed),
// then checks both against the update key the archive's app carries: a release
// signed with another key, or an app without a key, never gets a feed (D471,
// D479). Keys are made now and never kept.
func TestAppcastSignsAndChecksARelease(t *testing.T) {
	tools := sparkleTools(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	publicKey := base64.StdEncoding.EncodeToString(pub)
	dir := t.TempDir()
	seed := []byte(base64.StdEncoding.EncodeToString(priv.Seed()))
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	release := func(name, key string) string {
		t.Helper()
		app := testBundle(t, filepath.Join(dir, name), "0.2.0", key, "https://example.invalid/appcast.xml")
		zip := filepath.Join(dir, name, "MetaRush-0.2.0.zip")
		zipApp(t, app, zip)
		return zip
	}
	zip := release("good", publicKey)
	feed := filepath.Join(dir, "appcast.xml")
	for _, how := range [][]string{{"--ed-key-file", keyFile}, {"--ed-key-file", "-"}} {
		var stdin []byte
		if how[1] == "-" {
			stdin = seed
		}
		_ = os.Remove(feed)
		out, err := runScript(t, tools, stdin, append([]string{"appcast", zip, "--url", "https://example.invalid/MetaRush-0.2.0.zip", "--out", feed}, how...)...)
		if err != nil {
			t.Fatalf("appcast %v: %v\n%s", how, err, out)
		}
		b, err := os.ReadFile(feed)
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyFeed(b, publicKey); err != nil {
			t.Errorf("appcast %v: the feed: %v\n%s", how, err, b)
		}
		if !bytes.Contains(b, []byte("sparkle:edSignature=")) || !bytes.Contains(b, []byte("https://example.invalid/MetaRush-0.2.0.zip")) {
			t.Errorf("appcast %v: the item:\n%s", how, b)
		}
	}

	// The app carries another key than the one that signs: refused, before release.
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherKey := filepath.Join(dir, "other-key")
	if err := os.WriteFile(otherKey, []byte(base64.StdEncoding.EncodeToString(otherPriv.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runScript(t, tools, nil, "appcast", zip, "--url", "https://example.invalid/x.zip", "--out", feed, "--ed-key-file", otherKey); err == nil ||
		!strings.Contains(out, "don't match the update key in the app") {
		t.Errorf("signed with another key: %v\n%s", err, out)
	}
	// An app without a key would never update: no feed for it.
	noKey := release("no-key", "__ED_PUBLIC_KEY__")
	if out, err := runScript(t, tools, nil, "appcast", noKey, "--url", "https://example.invalid/x.zip", "--out", feed, "--ed-key-file", keyFile); err == nil ||
		!strings.Contains(out, "has no update key") {
		t.Errorf("an app without a key: %v\n%s", err, out)
	}
}

// scripts/mac-app.sh build refuses, before building anything, a feed that
// isn't https and an update key that isn't the one this Mac's keychain signs
// with (Sparkle's generate_keys -p; here a stand-in naming another key).
func TestBuildRefusesAPlainFeedAndAnotherKey(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("MetaRush.app is a Mac app")
	}
	if testing.Short() {
		t.Skip("runs the build script")
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	tools := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tools, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	keychain := "#!/bin/sh\necho " + base64.StdEncoding.EncodeToString(other) + "\n"
	if err := os.WriteFile(filepath.Join(tools, "bin", "generate_keys"), []byte(keychain), 0o755); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"--feed-url", "http://updates.example.invalid/appcast.xml"}, "must be https"},
		{[]string{"--feed-url", "https://updates.example.invalid/appcast.xml", "--ed-public-key", "not-a-key"}, "isn't an EdDSA public key"},
		{[]string{"--feed-url", "https://updates.example.invalid/appcast.xml", "--ed-public-key", base64.StdEncoding.EncodeToString(pub)}, "isn't the one in this Mac's keychain"},
	} {
		said, err := runScript(t, tools, nil, append([]string{"build", "--arch", "arm64", "--skip-web", "--no-dmg", "--out", out, "--bundle-id", "io.example.metarush-test"}, c.args...)...)
		if err == nil || !strings.Contains(said, c.says) {
			t.Errorf("build %v: %v\n%s", c.args, err, said)
		}
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("a refused build left %d files", len(entries))
	}
}
