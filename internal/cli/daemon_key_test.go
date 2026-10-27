package cli

import (
	"testing"

	"github.com/metavert-io/metarush/internal/config"
)

// The daemon restarts when config.yaml gives this machine another key for the
// same storage (`machine rekey`), not for other changes.
func TestStorageKeyChanged(t *testing.T) {
	old := config.Media{Provider: "s3", S3: config.S3{Bucket: "b", AccessKeyID: "shared", SecretAccessKey: "s1"}}
	for _, c := range []struct {
		name string
		now  config.Media
		want bool
	}{
		{"same", old, false},
		{"new key", config.Media{Provider: "s3", S3: config.S3{Bucket: "b", AccessKeyID: "own", SecretAccessKey: "s2"}}, true},
		{"new secret only", config.Media{Provider: "s3", S3: config.S3{Bucket: "b", AccessKeyID: "shared", SecretAccessKey: "s2"}}, true},
		{"another kind of storage", config.Media{Provider: "filesystem"}, false},
	} {
		if got := storageKeyChanged(old, c.now); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}
