package logbook

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
)

// KnownSecrets lists the keys this computer keeps (configured, in the home's
// secrets file, named, remembered), never identifiers such as a storage's
// access key ID, and nothing shorter than eight characters.
func TestKnownSecrets(t *testing.T) {
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Database.URI = "mongodb://maya:" + "longPassw0rd" + "@127.0.0.1:1/x"
	cfg.Media.S3.AccessKeyID = "tid_" + "IdentifierOnly123"
	cfg.Media.S3.SecretAccessKey = "tsec_" + "SecretValue123456"
	if err := os.WriteFile(filepath.Join(h, ".env"), []byte("METARUSH_HEYGEN_API_KEY=heygen"+"KeyValue1234\nMETARUSH_WATCH_DIR=/Volumes/x\nSHORT_KEY=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := keyLookup
	SetKeyLookup(func(_ *config.Config, name string) string {
		if name == "stockpics" {
			return "ability" + "KeyValue99"
		}
		return ""
	})
	defer SetKeyLookup(old)
	RememberSecret("remembered" + "Key4242")
	got := KnownSecrets(cfg, "stockpics")
	for _, want := range []string{"longPassw0rd", "tsec_SecretValue123456", "heygenKeyValue1234", "abilityKeyValue99", "rememberedKey4242"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, not := range []string{"tid_IdentifierOnly123", "/Volumes/x", "abc"} {
		if slices.Contains(got, not) {
			t.Errorf("%q is not a secret", not)
		}
	}
}
