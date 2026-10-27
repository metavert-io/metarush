package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/version"
)

// `metarush version` (D130): the plain line; --json the build alone, with no
// workspace needed (deploy.sh and release.sh read it); --details what's new
// and every computer's version (op version.show), or why the computers are
// missing — a version check never fails.
func TestVersionCommand(t *testing.T) {
	none := filepath.Join(t.TempDir(), "not-set-up")
	r := runCLI(t, none, "version", "--json")
	var doc struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || !doc.OK || r.code != 0 {
		t.Fatalf("version --json: %v\n%s%s", err, r.stdout, r.stderr)
	}
	for _, k := range []string{"version", "release", "dev", "commit", "date", "editSpec"} {
		if _, ok := doc.Data[k]; !ok {
			t.Errorf("version --json has no %q: %v", k, doc.Data)
		}
	}
	if doc.Data["version"] != version.Version || doc.Data["commit"] != version.Commit {
		t.Errorf("version --json: %v", doc.Data)
	}
	if _, ok := doc.Data["whatsNew"]; ok {
		t.Error("version --json carries the release notes: they belong to --details")
	}
	r = runCLI(t, none, "version")
	if r.code != 0 || !strings.Contains(r.stdout, version.Current().Plain()) || len(plainwords.CheckShown(strings.ReplaceAll(r.stdout, "--details", ""))) > 0 {
		t.Errorf("version:\n%s", r.stdout)
	}
	r = runCLI(t, none, "version", "--details")
	if r.code != 0 || !strings.Contains(r.stdout, "version "+version.Version) || !strings.Contains(r.stdout, "isn't set up on this computer yet") || !strings.Contains(r.stdout, "\n- ") {
		t.Errorf("version --details without a workspace (exit %d):\n%s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(none); err == nil {
		t.Error("version --details made a home folder")
	}

	// With a workspace: this computer and an older one.
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, in := range []models.Installation{
		{ID: a.Cfg.Installation.ID, WorkspaceID: a.WorkspaceID(), Name: "Studio Mac", OS: "darwin", Arch: "arm64", Version: version.Version, CreatedAt: now, LastSeenAt: now},
		{ID: ids.New(ids.Installation), WorkspaceID: a.WorkspaceID(), Name: "Mac mini", OS: "darwin", Arch: "arm64", Version: "0.1.0-dev", Commit: "1234567", CreatedAt: now, LastSeenAt: now.Add(-time.Minute)},
	} {
		if err := a.DB.Insert(ctx, store.CInstallations, in); err != nil {
			t.Fatal(err)
		}
	}
	r = runCLI(t, a.Cfg.Home(), "version", "--details", "--json")
	var about struct {
		OK   bool `json:"ok"`
		Data struct {
			Version   string `json:"version"`
			Older     int    `json:"older"`
			WhatsNew  []any  `json:"whatsNew"`
			Computers []struct {
				Name, Standing string
			} `json:"computers"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &about); err != nil || !about.OK || about.Data.Version != version.Version || about.Data.Older != 1 ||
		len(about.Data.Computers) != 2 || about.Data.Computers[1].Standing != "older" || len(about.Data.WhatsNew) == 0 {
		t.Fatalf("version --details --json: %v %+v\n%s", err, about, r.stderr)
	}
	r = runCLI(t, a.Cfg.Home(), "version", "--details")
	for _, want := range []string{"“Studio Mac” (this computer)", "“Mac mini” · Mac · MetaRush 0.1.0, older than this one: update MetaRush there", "0.1.0-dev, commit 1234567", "brew upgrade --cask metarush"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("version --details lacks %q:\n%s", want, r.stdout[:min(len(r.stdout), 1500)])
		}
	}
}
