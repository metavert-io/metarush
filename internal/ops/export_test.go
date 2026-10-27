package ops

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/publish"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// storedAsset puts random bytes in the media store and records them as an asset.
func storedAsset(t *testing.T, a *app.App, projectID, name, kind string, mi models.MediaInfo) models.Asset {
	t.Helper()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), name)
	b := make([]byte, 8192)
	_, _ = rand.Read(b)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(ctx, key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	mi.Streams = []models.Stream{}
	if mi.HasAudio {
		mi.Streams = []models.Stream{{Index: 1, Type: "audio", Codec: "aac", Channels: 2, SampleRate: 48000}}
	}
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{projectID}, Kind: kind, Name: name,
		SHA256: sha, Size: int64(len(b)), StorageKey: key, State: models.AssetAvailable, Media: mi, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CAssets, as)
	return as
}

// export.timeline: every format, the footage under readable names (two files
// called the same get " 2"), captions as .srt, nothing ever overwritten, a
// second export into the same folder keeps the footage it already put there.
func TestExportTimeline(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Shop story"})
	video := models.MediaInfo{Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30}
	one := storedAsset(t, a, p.Project.ID, "Take.mov", "video", video)
	two := storedAsset(t, a, p.Project.ID, "Take.mov", "video", video) // another file with the same name
	music := storedAsset(t, a, p.Project.ID, "Bed.m4a", "audio", models.MediaInfo{Duration: 60, HasAudio: true})
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), music.ID, map[string]any{"$set": map[string]any{"rights": models.MusicRights{License: "CC0"}}})
	plan := fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"title":"Cut","output":{"preset":"landscape-1080p","fps":30},
		"clips":[{"id":"a","assetId":%q,"in":1,"out":5},{"id":"b","assetId":%q,"in":2,"out":6,"transitionIn":{"type":"crossfade","duration":0.5}}],
		"audio":[{"id":"bed","assetId":%q,"role":"music","in":0,"out":8,"at":0,"fadeIn":1,"fadeOut":1}],
		"captions":{"enabled":true,"source":"manual","segments":[{"from":0.5,"to":2,"text":"Hello there"}]}}}`, p.Project.ID, one.ID, two.ID, music.ID)
	chk := call[EditCheckOut](t, a, "edit.check", EditCheckIn{Plan: json.RawMessage(plan)})
	if !chk.Valid {
		t.Fatalf("plan: %+v", chk.Errors)
	}
	rev := call[EditApplyOut](t, a, "edit.apply", EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken})

	dir := t.TempDir()
	in := ExportTimelineIn{RevisionID: rev.Revision.ID, Format: "otio", Out: filepath.Join(dir, "cut"), Media: filepath.Join(dir, "Footage")}
	out := call[ExportTimelineOut](t, a, "export.timeline", in)
	if out.File != filepath.Join(dir, "cut.otio") || out.Captions != filepath.Join(dir, "cut.srt") || out.Clips != 2 || out.SoundTracks != 2 || out.App != "DaVinci Resolve" {
		t.Fatalf("export: %+v", out)
	}
	names := map[string]string{}
	for _, m := range out.Media {
		names[m.AssetID] = m.Name
		if m.How != "cloned" && m.How != "copied" {
			t.Fatalf("%s was %s", m.Name, m.How)
		}
		got, _ := media.HashFile(m.Path)
		if got != m.SHA256 {
			t.Fatalf("%s: the copy's SHA-256 differs", m.Name)
		}
	}
	if names[one.ID] != "Take.mov" || names[two.ID] != "Take 2.mov" || names[music.ID] != "Bed.m4a" {
		t.Fatalf("readable names: %v", names)
	}
	body, _ := os.ReadFile(out.File)
	if !strings.Contains(string(body), "Footage/Take%202.mov") || !strings.Contains(string(body), `"OTIO_SCHEMA": "Transition.1"`) {
		t.Fatal("the timeline points at the copies and carries the dissolve")
	}
	if srt, _ := os.ReadFile(out.Captions); !strings.Contains(string(srt), "Hello there") {
		t.Fatalf("captions: %q", srt)
	}

	// The same file again: refused, then replaced on request.
	_, err := CallTyped[ExportTimelineOut](ctx, a, "export.timeline", in, Caller{})
	if apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("exporting over an existing timeline: %v", err)
	}
	in.Replace = true
	if _, err := CallTyped[ExportTimelineOut](ctx, a, "export.timeline", in, Caller{}); err != nil {
		t.Fatal(err)
	}
	// Another format into the same folders: the footage and the captions are kept.
	x := call[ExportTimelineOut](t, a, "export.timeline", ExportTimelineIn{RevisionID: rev.Revision.ID, Format: "xmeml", Out: filepath.Join(dir, "cut"), Media: filepath.Join(dir, "Footage")})
	for _, m := range x.Media {
		if m.How != "kept" {
			t.Fatalf("%s should be kept, was %s", m.Name, m.How)
		}
	}
	if x.File != filepath.Join(dir, "cut.xml") || x.Captions == "" {
		t.Fatalf("xmeml: %+v", x)
	}
	// Links instead of copies.
	l := call[ExportTimelineOut](t, a, "export.timeline", ExportTimelineIn{RevisionID: rev.Revision.ID, Format: "fcpxml", Out: filepath.Join(dir, "linked"), Media: filepath.Join(dir, "Links"), Link: true})
	for _, m := range l.Media {
		st, err := os.Lstat(m.Path)
		if err != nil || st.Mode()&os.ModeSymlink == 0 || m.How != "linked" {
			t.Fatalf("%s: %v %v", m.Name, err, m.How)
		}
	}
	// Without a media folder the timeline points at this machine's cache.
	c := call[ExportTimelineOut](t, a, "export.timeline", ExportTimelineIn{RevisionID: rev.Revision.ID, Format: "fcpxml", Out: filepath.Join(dir, "cached.fcpxml"), NoCaptions: true})
	if c.Captions != "" || c.Media[0].How != "cache" || !strings.HasPrefix(c.Media[0].Path, a.Cfg.Cache.Dir) || len(c.Notes) == 0 {
		t.Fatalf("cache export: %+v", c)
	}
	// Bad input.
	for _, bad := range []ExportTimelineIn{
		{RevisionID: rev.Revision.ID, Format: "aaf", Out: filepath.Join(dir, "x")},
		{RevisionID: rev.Revision.ID, Format: "otio"},
		{RevisionID: rev.Revision.ID, Format: "otio", Out: filepath.Join(dir, "y"), Link: true},
	} {
		if _, err := CallTyped[ExportTimelineOut](ctx, a, "export.timeline", bad, Caller{}); apperr.From(err).Code != apperr.InvalidInput {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	// Over HTTP it is refused (it writes files where it is told): the
	// dashboard has its own endpoint that picks the folder.
	if _, err := CallTyped[ExportTimelineOut](ctx, a, "export.timeline", in, Caller{Remote: true}); apperr.From(err).Code != apperr.Unauthorized {
		t.Fatalf("over HTTP: %v", err)
	}
}

// PublishJournalForTest is what this computer keeps while a post goes out.
type PublishJournalForTest = publishJournal

// SavePublishJournal keeps it, as a post going out does.
func SavePublishJournal(a *app.App, j *PublishJournalForTest) error { return j.save(a) }

// PublishJournalKept reports whether this computer still keeps a post's journal.
func PublishJournalKept(a *app.App, pub string) bool { return loadPublishJournal(a, pub) != nil }

// SendPublication sends a post that is on its version, as the daemon's look does.
func SendPublication(ctx context.Context, a *app.App, j *PublishJournalForTest, pub models.Publication) (*publish.Posted, error) {
	return sendPublication(ctx, a, j, pub, false)
}
