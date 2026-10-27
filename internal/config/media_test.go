package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A store description: the media keys at the top level or under media:,
// relative paths taken from the file's folder, secrets only as references.
func TestLoadMediaFile(t *testing.T) {
	keyring.MockInit() // never touch the real keychain
	home, dir := t.TempDir(), t.TempDir()

	f, err := LoadMediaFile(home, writeFile(t, dir, "fs.yaml", "provider: filesystem\nfilesystem:\n  root: shared media\n  shared: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Media.Filesystem.Root != filepath.Join(dir, "shared media") || !f.Media.Filesystem.Shared {
		t.Fatalf("%+v", f.Media)
	}
	if text, _ := NodeYAML(f.Node); !strings.Contains(text, filepath.Join(dir, "shared media")) {
		t.Fatalf("the section written to config.yaml keeps the absolute folder: %s", text)
	}

	t.Setenv("MR_TEST_MEDIA_SECRET", "s3cr3t-value")
	f, err = LoadMediaFile(home, writeFile(t, dir, "s3.yaml", "media:\n  provider: s3\n  s3:\n    endpoint: https://storage.example.test\n    bucket: footage\n    accessKeyId: AKID\n    secretAccessKey: ${MR_TEST_MEDIA_SECRET}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Media.S3.SecretAccessKey != "s3cr3t-value" || f.SecretVar != "MR_TEST_MEDIA_SECRET" || f.Media.S3.Bucket != "footage" {
		t.Fatalf("%+v %q", f.Media.S3, f.SecretVar)
	}
	text, _ := NodeYAML(f.Node)
	if strings.Contains(text, "s3cr3t-value") || !strings.Contains(text, "${MR_TEST_MEDIA_SECRET}") {
		t.Fatalf("the section as written keeps the reference, never the secret: %s", text)
	}
	f.SetSecretRef("keychain:new-media")
	if text, _ := NodeYAML(f.Node); !strings.Contains(text, "keychain:new-media") || !RefsOnly(f.Node) {
		t.Fatalf("secret reference: %s", text)
	}

	// A keychain reference resolves through this home's keychain namespace.
	ref, err := StoreSecret(home, "media-secret", "from-keychain")
	if err != nil {
		t.Fatal(err)
	}
	f, err = LoadMediaFile(home, writeFile(t, dir, "s3k.json", `{"provider":"s3","s3":{"bucket":"b","accessKeyId":"AKID","secretAccessKey":"`+ref+`"}}`))
	if err != nil || f.Media.S3.SecretAccessKey != "from-keychain" || f.SecretVar != "" {
		t.Fatalf("keychain: %+v %v", f, err)
	}

	f, err = LoadMediaFile(home, writeFile(t, dir, "gcs.yaml", "provider: gcs\ngcs:\n  bucket: team-media\n  prefix: metarush\n  credentialsFile: key.json\n"))
	if err != nil || f.Media.GCS.CredentialsFile != filepath.Join(dir, "key.json") || f.Media.GCS.Prefix != "metarush" {
		t.Fatalf("gcs: %+v %v", f, err)
	}

	for name, body := range map[string]string{
		"plaintext secret": "provider: s3\ns3:\n  bucket: b\n  accessKeyId: AKID\n  secretAccessKey: abc123plaintext\n", // secret-scan: allow
		"secret default":   "provider: s3\ns3:\n  bucket: b\n  accessKeyId: AKID\n  secretAccessKey: ${X:abc123}\n",     // secret-scan: allow
		"no secret":        "provider: s3\ns3:\n  bucket: b\n  accessKeyId: AKID\n",
		"unset variable":   "provider: s3\ns3:\n  bucket: b\n  accessKeyId: AKID\n  secretAccessKey: ${MR_TEST_UNSET_SECRET}\n",
		"no provider":      "bucket: b\n",
		"unknown provider": "provider: ftp\n",
		"no root":          "provider: filesystem\n",
		"no bucket":        "provider: gcs\ngcs:\n  prefix: x\n",
		"not yaml":         "provider: [\n",
	} {
		if _, err := LoadMediaFile(home, writeFile(t, dir, "bad.yaml", body)); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "abc123") {
			t.Errorf("%s: the error repeats the secret: %v", name, err)
		}
	}
	if _, err := LoadMediaFile(home, filepath.Join(dir, "missing.yaml")); !os.IsNotExist(err) {
		t.Fatalf("missing file: %v", err)
	}
}

// METARUSH_* variables win over config.yaml: the ones that would change the
// new storage's settings are named.
func TestMediaOverrides(t *testing.T) {
	m := Media{Provider: "s3", S3: S3{Bucket: "new", SecretAccessKey: "x"}}
	if got := MediaOverrides(m); len(got) != 0 {
		t.Fatalf("nothing set: %v", got)
	}
	t.Setenv("METARUSH_S3_BUCKET", "old")
	t.Setenv("METARUSH_GCS_BUCKET", "elsewhere")   // another provider's: does not matter
	t.Setenv("METARUSH_S3_SECRET_ACCESS_KEY", "x") // the same value: not an override
	if got := MediaOverrides(m); len(got) != 1 || got[0] != "METARUSH_S3_BUCKET" {
		t.Fatalf("%v", got)
	}
	t.Setenv("METARUSH_MEDIA_PROVIDER", "gcs")
	if got := MediaOverrides(m); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	// The same folder written another way is not an override.
	t.Setenv("METARUSH_MEDIA_PROVIDER", "")
	h, _ := os.UserHomeDir()
	t.Setenv("METARUSH_MEDIA_ROOT", "~/footage/")
	if got := MediaOverrides(Media{Provider: "filesystem", Filesystem: Filesystem{Root: filepath.Join(h, "footage")}}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

// GetField reads config.yaml as written; a section kept as text parses back.
func TestGetFieldAndParseMediaNode(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(Path(home), []byte("version: 1\nmedia:\n    provider: filesystem\n    filesystem:\n        root: ${MR_TEST_ROOT:/tmp/somewhere}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := GetField(home, []string{"media"})
	if err != nil || n == nil {
		t.Fatal(err)
	}
	text, _ := NodeYAML(n)
	if !strings.Contains(text, "${MR_TEST_ROOT:/tmp/somewhere}") {
		t.Fatalf("as written: %s", text)
	}
	m, err := ParseMediaNode(home, text)
	if err != nil || m.Filesystem.Root != "/tmp/somewhere" {
		t.Fatalf("%+v %v", m, err)
	}
	if n, err := GetField(home, []string{"nothing", "here"}); err != nil || n != nil {
		t.Fatalf("missing: %v %v", n, err)
	}
	// Replacing the section with a node keeps the rest of the file.
	f, err := LoadMediaFile(home, writeFile(t, t.TempDir(), "to.yaml", "provider: filesystem\nfilesystem:\n  root: /srv/media\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := SetField(home, []string{"media"}, f.Node); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(Path(home))
	if !strings.Contains(string(raw), "root: /srv/media") || !strings.Contains(string(raw), "version: 1") {
		t.Fatalf("%s", raw)
	}
	if err := SetField(home, []string{"media"}, n); err != nil { // and back
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(Path(home)); !strings.Contains(string(raw), "${MR_TEST_ROOT:/tmp/somewhere}") {
		t.Fatalf("restored: %s", raw)
	}
}
