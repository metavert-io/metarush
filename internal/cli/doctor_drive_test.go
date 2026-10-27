package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/testutil/fakerclone"
	"github.com/metavert-io/metarush/internal/tools"
)

// The checkup says whether Google Drive folders can be read without syncing
// them (D220), and how to set it up when they can't — never failing for it.
func TestDoctorSaysHowToSetUpGoogleDrive(t *testing.T) {
	ctx := context.Background()
	collect := func(ts *tools.Set) Check {
		var out []Check
		driveChecks(ctx, ts, func(area, name, status, detail, fix string, plain ...string) {
			ch := Check{Area: area, Name: name, Status: status, Detail: detail, Fix: fix}
			if len(plain) > 0 {
				ch.Say = plain[0]
			}
			if len(plain) > 1 {
				ch.Do = plain[1]
			}
			out = append(out, ch)
		})
		if len(out) != 1 {
			t.Fatalf("checks %+v", out)
		}
		return out[0]
	}
	none := collect(tools.New(config.Tools{Rclone: filepath.Join(t.TempDir(), "rclone")}))
	if none.Status != "info" || !strings.Contains(none.Say, "once rclone is set up") || !strings.Contains(none.Do, "rclone's own setup") {
		t.Fatalf("not installed: %+v", none)
	}
	tool, _ := fakerclone.Write(t, fakerclone.State{Remotes: []fakerclone.Remote{{Name: "photos", Type: "s3", Root: t.TempDir()}}})
	noDrive := collect(tools.New(config.Tools{Rclone: tool}))
	if noDrive.Status != "info" || !strings.Contains(noDrive.Say, "no Google Drive is set up") || noDrive.Fix != "rclone config" {
		t.Fatalf("no drive: %+v", noDrive)
	}
	tool, _ = fakerclone.Write(t, fakerclone.State{Remotes: []fakerclone.Remote{{Name: "gdrive", Type: "drive", Root: t.TempDir()}}})
	ok := collect(tools.New(config.Tools{Rclone: tool}))
	if ok.Status != "ok" || !strings.Contains(ok.Say, "“gdrive”") {
		t.Fatalf("ready: %+v", ok)
	}
	for _, ch := range []Check{none, noDrive, ok} {
		for _, s := range []string{ch.Say, ch.Do} {
			if fs := plainwords.Check(s); len(fs) > 0 {
				t.Errorf("not plain: %q %+v", s, fs)
			}
		}
	}
}
