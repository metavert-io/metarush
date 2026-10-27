package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestGCSConfigAndPruneInterval(t *testing.T) {
	h := t.TempDir()
	cfg := Default(h)
	cfg.Workspace = Ref{ID: "ws_x", Name: "W"}
	cfg.Database.URI = "mongodb://127.0.0.1:1/"
	cfg.Media = Media{Provider: "gcs", GCS: GCS{Bucket: "team-media", Prefix: "studio", CredentialsFile: "~/keys/sa.json"}}
	if err := Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path(h))
	if !strings.Contains(string(b), "credentialsFile: ~/keys/sa.json") || strings.Contains(string(b), "s3:") {
		t.Fatalf("config.yaml:\n%s", b)
	}
	got, err := Load(h)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if got.Media.Provider != "gcs" || got.Media.GCS.Bucket != "team-media" || got.Media.GCS.CredentialsFile != home+"/keys/sa.json" {
		t.Fatalf("loaded %+v", got.Media.GCS)
	}
	t.Setenv("METARUSH_GCS_BUCKET", "other")
	t.Setenv("METARUSH_GCS_CREDENTIALS", "/etc/metarush/sa.json")
	got, _ = Load(h)
	if got.Media.GCS.Bucket != "other" || got.Media.GCS.CredentialsFile != "/etc/metarush/sa.json" {
		t.Fatalf("env overrides %+v", got.Media.GCS)
	}
	if got.Cache.PruneInterval() != 10*time.Minute {
		t.Fatalf("default prune interval %v", got.Cache.PruneInterval())
	}
	if (Cache{PruneIntervalMinutes: 3}).PruneInterval() != 3*time.Minute || (Cache{PruneIntervalMinutes: -1}).PruneInterval() != 0 {
		t.Fatal("prune interval")
	}
}
