package ops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/firstrun"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/winname"
	"github.com/metavert-io/metarush/internal/wsl"
)

// Delivering to Windows from inside WSL (D520–D523): a folder typed as
// Windows gives it is where WSL shows it; what lands there has names Windows
// keeps; a Windows user's OneDrive is a sync folder, so a client's rules are
// asked; a timeline's footage there is copied, never linked; a network
// folder's Windows address is refused in plain words.
func TestDeliveringToWindowsInsideWSL(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p, rid, _ := approvedWithRender(t, a, "Harbour: what's next?", false)
	c, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wsl.Pretend(wsl.Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Interop: true, DriveRoot: "/mnt/", Drives: map[string]string{c: "c", d: "d"}}))

	out, err := ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: `"C:\Users\You\Desktop\Client"`}, person)
	if err != nil || len(out.Files) != 1 {
		t.Fatalf("deliver: %+v %v", out, err)
	}
	f := out.Files[0]
	if filepath.Dir(f.Path) != filepath.Join(c, "Users", "You", "Desktop", "Client") || !winname.Kept(filepath.Base(f.Path)) || !winname.Kept(filepath.Base(f.Sidecar)) {
		t.Fatalf("delivered %s, %s", f.Path, f.Sidecar)
	}
	if _, err := os.Stat(f.Path); err != nil {
		t.Fatal(err)
	}

	// The client's rules keep footage out of deliveries: OneDrive on Windows is a sync folder.
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, a, "content.own", ops.ContentOwnIn{ProjectID: p.ID}, person); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[leave.LeaveOut](ctx, a, "content.leave.set", leave.LeaveSetIn{ProjectID: p.ID, Keep: map[string][]string{"delivery": {"footage", "voices", "faces", "words"}}}, person); err != nil {
		t.Fatal(err)
	}
	if err := doErr(a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: `C:\Users\You\OneDrive - Contoso\Client`}, person); code(err) != "E_KEPT_HERE" {
		t.Fatalf("delivered into a Windows user's OneDrive: %v", err)
	}
	if err := doErr(a, "export.timeline", ops.ExportTimelineIn{RevisionID: rid, Format: "otio", Out: `C:\Users\You\Dropbox\cut.otio`}, person); code(err) != "E_KEPT_HERE" {
		t.Fatalf("a timeline into a Windows user's Dropbox: %v", err)
	}
	// Dropbox moved to the top of another drive (a footage drive) is one too.
	if err := doErr(a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: `D:\Dropbox\Client`}, person); code(err) != "E_KEPT_HERE" {
		t.Fatalf("delivered into Dropbox at the top of drive D: %v", err)
	}
	if _, err := ops.CallTyped[leave.LeaveOut](ctx, a, "content.leave.set", leave.LeaveSetIn{ProjectID: p.ID, Rules: &models.LeaveRules{}}, person); err != nil {
		t.Fatal(err)
	}

	// A timeline's footage on the Windows side is copied, never linked.
	tl, err := ops.CallTyped[ops.ExportTimelineOut](ctx, a, "export.timeline", ops.ExportTimelineIn{RevisionID: rid, Format: "otio",
		Out: `C:\Users\You\Timelines\cut.otio`, Media: `C:\Users\You\Timelines\Footage`, Link: true}, person)
	if err != nil {
		t.Fatal(err)
	}
	if tl.File != filepath.Join(c, "Users", "You", "Timelines", "cut.otio") || len(tl.Media) == 0 || !strings.Contains(strings.Join(tl.Notes, " "), "copied there instead") {
		t.Fatalf("timeline: %+v", tl)
	}
	for _, m := range tl.Media {
		if m.How == "linked" || !winname.Kept(m.Name) {
			t.Fatalf("footage %+v", m)
		}
	}

	if err := doErr(a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: `\\nas\share\Client`}, person); code(err) != "E_INVALID_INPUT" || !strings.Contains(err.Error(), "network folder") {
		t.Fatalf("a network folder: %v", err)
	}
	// A Windows path Ubuntu can't place is refused, never made in the current folder.
	wd, _ := os.Getwd()
	for typed, says := range map[string]string{`C:Client`: "which folder on drive C:", `\Client`: "which drive", `\\nas`: "network folder"} {
		if err := doErr(a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: typed}, person); code(err) != "E_INVALID_INPUT" || !strings.Contains(err.Error(), says) {
			t.Fatalf("%s: %v", typed, err)
		}
	}
	for _, made := range []string{"C:Client", `\Client`, `\\nas`} {
		if _, err := os.Stat(filepath.Join(wd, made)); err == nil {
			t.Fatalf("%s was made in the current folder", made)
		}
	}
}

// The paths behind WSL's notes about MetaRush's folders on the Windows side
// name the Windows user: version.show and setup.status give them to owners
// only, the words to everyone (D515).
func TestWindowsFolderPathsAreForOwners(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	c, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	obs := filepath.Join(c, "Users", "Sam", "Videos", "OBS")
	if err := os.MkdirAll(obs, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wsl.Pretend(wsl.Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Interop: true, Systemd: true, DriveRoot: "/mnt/", Drives: map[string]string{c: "c"}}))
	a.Cfg.Worker.Watch = config.Watch{Enabled: true, Dir: obs}
	member := ops.OverHTTP(ctx, func() bool { return false })
	owner := ops.OverHTTP(ctx, func() bool { return true })
	files := func(r *wsl.Report) wsl.Note {
		t.Helper()
		n, ok := r.Note("files")
		if !ok || !strings.Contains(n.Says, "Windows side (C:)") {
			t.Fatalf("no note about the watched folder: %+v", r)
		}
		return n
	}
	for _, who := range []struct {
		ctx       context.Context
		seesPaths bool
	}{{member, false}, {owner, true}, {ctx, true}} {
		v, err := ops.CallTyped[ops.VersionShowOut](who.ctx, a, "version.show", ops.VersionShowIn{}, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		st, err := ops.CallTyped[firstrun.SetupStatusOut](who.ctx, a, "setup.status", firstrun.SetupStatusIn{}, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range []wsl.Note{files(v.Windows), files(st.WhatWorks.Windows)} {
			if strings.Contains(n.Command, obs) != who.seesPaths || strings.Contains(n.Says+n.Next, "Sam") {
				t.Errorf("owner %v: %+v", who.seesPaths, n)
			}
		}
	}
}
