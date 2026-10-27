package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/interchange"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

type importClip struct {
	asset           string
	from, dur, trim int
	trans           string
	transDur        int
	muted           bool
}

func importClips(p *editspec.RenderPlan) []importClip {
	var out []importClip
	for _, c := range p.Clips {
		out = append(out, importClip{asset: c.AssetID, from: c.From, dur: c.DurationInFrames, trim: c.TrimBefore, trans: c.TransitionIn.Type, transDur: c.TransitionIn.DurationInFrames, muted: c.Muted})
	}
	return out
}

func near1(a, b int) bool { return a-b <= 1 && b-a <= 1 }

// The round trip (D304): a cut exported with export.timeline in each format,
// with its footage, and imported back makes a new cut whose plan plays the
// same clips from the same frames at the same times, with the same
// transitions, and the same music.
func TestTimelineImportRoundTrip(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Round trip"})
	video := models.MediaInfo{Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30}
	one := storedAsset(t, a, p.Project.ID, "Take.mov", "video", video)
	two := storedAsset(t, a, p.Project.ID, "Take.mov", "video", video) // the same name: told apart by their bytes
	music := storedAsset(t, a, p.Project.ID, "Bed.m4a", "audio", models.MediaInfo{Duration: 60, HasAudio: true})
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), music.ID, map[string]any{"$set": map[string]any{"rights": models.MusicRights{License: "CC0"}}})
	plan := fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"title":"Cut","output":{"preset":"landscape-1080p","fps":30},
		"clips":[{"id":"a","assetId":%q,"in":1,"out":5},
		         {"id":"b","assetId":%q,"in":2,"out":6,"transitionIn":{"type":"crossfade","duration":0.5}},
		         {"id":"c","assetId":%q,"in":10,"out":14,"muted":true,"transitionIn":{"type":"dip-to-black","duration":0.6}},
		         {"id":"d","assetId":%q,"in":20,"out":24,"transitionIn":{"type":"cut","audio":{"mode":"lead","duration":0.5}}}],
		"audio":[{"id":"bed","assetId":%q,"role":"music","in":0,"out":12,"at":0,"fadeIn":1,"fadeOut":1}]}}`, p.Project.ID, one.ID, two.ID, one.ID, two.ID, music.ID)
	chk := call[EditCheckOut](t, a, "edit.check", EditCheckIn{Plan: json.RawMessage(plan)})
	if !chk.Valid {
		t.Fatalf("plan: %+v", chk.Errors)
	}
	rev := call[EditApplyOut](t, a, "edit.apply", EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken})
	want := call[editspec.RenderPlan](t, a, "revision.plan", RevisionPlanIn{ID: rev.Revision.ID})
	wantClips := importClips(want)

	dir := t.TempDir()
	for _, format := range interchange.Formats {
		t.Run(format, func(t *testing.T) {
			out := call[ExportTimelineOut](t, a, "export.timeline", ExportTimelineIn{RevisionID: rev.Revision.ID, Format: format, Out: filepath.Join(dir, "cut-"+format), Media: filepath.Join(dir, "Footage")})
			// The Library learns the files' fingerprints as ingest does.
			for _, m := range out.Media {
				fp, _, err := media.FingerprintFile(m.Path)
				if err != nil {
					t.Fatal(err)
				}
				_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), m.AssetID, map[string]any{"$set": map[string]any{"fingerprint": fp}})
			}
			read := call[TimelineReadOut](t, a, "timeline.read", TimelineReadIn{File: out.File, ProjectID: p.Project.ID})
			if read.Questions != 0 || len(read.Footage) != 3 {
				t.Fatalf("read: %+v", read)
			}
			for _, ft := range read.Footage {
				if ft.How != "metarush" && ft.How != "same-file" {
					t.Fatalf("%s matched by %s (%s)", ft.Name, ft.How, ft.Why)
				}
			}
			dry := call[TimelineImportOut](t, a, "timeline.import", TimelineImportIn{File: out.File, ProjectID: p.Project.ID, DryRun: true})
			if dry.Check == nil || !dry.Check.Valid || dry.Check.ConfirmToken == "" {
				t.Fatalf("check: %+v", dry.Check)
			}
			made := call[TimelineImportOut](t, a, "timeline.import", TimelineImportIn{File: out.File, ProjectID: p.Project.ID, ConfirmToken: dry.Check.ConfirmToken})
			if !made.Saved || made.Applied == nil || made.Branch == "" || made.Read.Seconds <= 0 {
				t.Fatalf("import: %+v", made)
			}
			got := call[editspec.RenderPlan](t, a, "revision.plan", RevisionPlanIn{ID: made.Applied.Revision.ID})
			have := importClips(got)
			if len(have) != len(wantClips) || got.DurationInFrames != want.DurationInFrames {
				t.Fatalf("%d clips, %d frames; want %d, %d", len(have), got.DurationInFrames, len(wantClips), want.DurationInFrames)
			}
			for i, w := range wantClips {
				h := have[i]
				if h.asset != w.asset || !near1(h.from, w.from) || !near1(h.dur, w.dur) || !near1(h.trim, w.trim) || h.trans != w.trans || !near1(h.transDur, w.transDur) || h.muted != w.muted {
					t.Errorf("clip %d: got %+v, want %+v", i, h, w)
				}
			}
			var bed *editspec.PlanAudio
			for i := range got.Audio {
				if got.Audio[i].AssetID == music.ID {
					bed = &got.Audio[i]
				}
			}
			if bed == nil || bed.From != 0 || !near1(bed.DurationInFrames, 360) {
				t.Errorf("music: %+v", bed)
			}
		})
	}
}

// Footage the Library doesn't have is asked about, refused until answered,
// then relinked, added or left out; nothing is changed by reading.
func TestTimelineImportQuestions(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Questions"})
	video := models.MediaInfo{Duration: 20, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 25}
	known := storedAsset(t, a, p.Project.ID, "Interview.mov", "video", video)
	other := storedAsset(t, a, p.Project.ID, "Wide shot.mov", "video", video)
	edl := "TITLE: Questions\nFCM: NON-DROP FRAME\n\n" +
		"001  AX       AA/V  C        00:00:01:00 00:00:05:00 01:00:00:00 01:00:04:00\n* FROM CLIP NAME: Interview.mov\n" +
		"002  AX       V     C        00:00:02:00 00:00:04:00 01:00:04:00 01:00:06:00\n* FROM CLIP NAME: Missing broll.mov\n" +
		"003  AX       AA/V  C        00:00:08:00 00:00:10:00 01:00:06:00 01:00:08:00\n* FROM CLIP NAME: Interview.mov\n* LOC: 01:00:06:00 RED Chapter 2: The answer\n"
	in := TimelineReadIn{Content: edl, Name: "questions.edl", ProjectID: p.Project.ID, FPS: 25}
	read := call[TimelineReadOut](t, a, "timeline.read", in)
	if read.Questions != 1 || read.RendersAt != 25 || len(read.Footage) != 2 || read.Footage[1].Question == "" || read.Footage[0].AssetID != known.ID {
		t.Fatalf("read: %+v", read)
	}
	if len(read.Chapters) != 1 || read.Chapters[0].Title != "The answer" {
		t.Fatalf("chapters: %+v", read.Chapters)
	}
	// Refused while a question is open.
	_, err := CallTyped[TimelineImportOut](ctx, a, "timeline.import", TimelineImportIn{Content: edl, Name: "questions.edl", ProjectID: p.Project.ID, FPS: 25}, Caller{})
	if apperr.From(err).Code != apperr.InvalidInput || !strings.Contains(err.Error(), "Missing broll") {
		t.Fatalf("an open question: %v", err)
	}
	// Relinked to another clip.
	ans := []TimelineAnswer{{Footage: "Missing broll.mov", Relink: "Wide shot"}}
	dry := call[TimelineImportOut](t, a, "timeline.import", TimelineImportIn{Content: edl, Name: "questions.edl", ProjectID: p.Project.ID, FPS: 25, Answers: ans, DryRun: true})
	if dry.Read.Questions != 0 || dry.Check == nil || !dry.Check.Valid || dry.Read.Footage[1].AssetID != other.ID || dry.Read.Footage[1].How != "relinked" {
		t.Fatalf("relinked: %+v %+v", dry.Read.Footage, dry.Check)
	}
	made := call[TimelineImportOut](t, a, "timeline.import", TimelineImportIn{Content: edl, Name: "questions.edl", ProjectID: p.Project.ID, FPS: 25, Answers: ans, ConfirmToken: dry.Check.ConfirmToken})
	if !made.Saved || !made.ChaptersSaved || made.CutName != "Questions" || made.Branch != "questions" {
		t.Fatalf("made: %+v", made)
	}
	notes := call[DeliveryNotesOut](t, a, "delivery.notes", DeliveryNotesIn{ProjectID: p.Project.ID, Revision: made.Applied.Revision.ID})
	if notes.Notes == nil || len(notes.Notes.Chapters) != 2 || notes.Notes.Chapters[0].At != 0 || notes.Notes.Chapters[1].Title != "The answer" {
		t.Fatalf("chapters saved: %+v", notes.Notes)
	}
	// Left out: the cut gets shorter, and says so.
	skip := call[TimelineImportOut](t, a, "timeline.import", TimelineImportIn{Content: edl, Name: "questions.edl", ProjectID: p.Project.ID, FPS: 25,
		Answers: []TimelineAnswer{{Footage: "Missing broll.mov", Skip: true}}, DryRun: true})
	if skip.Read.Shots != 2 || skip.Read.Seconds != 6 || !strings.Contains(strings.Join(skip.Read.Lost, "\n"), "Missing broll") {
		t.Fatalf("skipped: %+v", skip.Read)
	}
	// Added from this computer: a file that isn't footage is refused plainly.
	notVideo := filepath.Join(t.TempDir(), "Missing broll.txt")
	_ = os.WriteFile(notVideo, []byte("not a video"), 0o644)
	_, err = CallTyped[TimelineImportOut](ctx, a, "timeline.import", TimelineImportIn{Content: edl, Name: "questions.edl", ProjectID: p.Project.ID, FPS: 25,
		Answers: []TimelineAnswer{{Footage: "Missing broll.mov", Add: notVideo}}, ConfirmToken: "x"}, Caller{})
	if err == nil {
		t.Fatal("a text file was used as footage")
	}
	// A file from far away is never read off this computer.
	_, err = CallTyped[TimelineReadOut](ctx, a, "timeline.read", TimelineReadIn{File: "/etc/hosts"}, Caller{Remote: true})
	if apperr.From(err).Code != apperr.Unauthorized {
		t.Fatalf("remote file read: %v", err)
	}
	// Not a timeline.
	_, err = CallTyped[TimelineReadOut](ctx, a, "timeline.read", TimelineReadIn{Content: "hello", Name: "notes.txt"}, Caller{})
	if apperr.From(err).Code != apperr.InvalidInput || !strings.Contains(apperr.From(err).Message, "can't tell what kind of timeline") {
		t.Fatalf("not a timeline: %v", err)
	}
}

// fakeFootage writes a file whose first bytes say video (an MP4's ftyp box)
// and returns it with its fingerprint.
func fakeFootage(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	p := filepath.Join(dir, name)
	b := append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), make([]byte, 64<<10)...)
	for i := 24; i < len(b); i++ {
		b[i] = byte(i * 7)
	}
	b = append(b, []byte(name)...) // each file its own bytes
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	fp, _, err := media.FingerprintFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, fp
}

// The files a timeline names are never opened or looked for: a timeline can
// name any path on this computer. Only a file a person answered with is read
// — footage only, never a folder, a picture or a PDF — its fingerprint finds
// it in the Library, and it is added only once the import is confirmed with a
// token that checks out.
func TestTimelineImportReadsOnlyFilesAPersonChose(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Chosen files"})
	video := models.MediaInfo{Duration: 20, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 25}
	known := storedAsset(t, a, p.Project.ID, "Interview.mov", "video", video)
	dir := t.TempDir()
	named, namedFP := fakeFootage(t, dir, "Somewhere.mov")
	// The Library has the file the timeline names, under another name.
	inLibrary := storedAsset(t, a, p.Project.ID, "Kept as something else.mov", "video", video)
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), inLibrary.ID, map[string]any{"$set": map[string]any{"fingerprint": namedFP}})
	edl := "TITLE: Chosen\nFCM: NON-DROP FRAME\n\n" +
		"001  AX       AA/V  C        00:00:01:00 00:00:05:00 01:00:00:00 01:00:04:00\n* FROM CLIP NAME: Interview.mov\n" +
		"002  AX       V     C        00:00:02:00 00:00:04:00 01:00:04:00 01:00:06:00\n* FROM CLIP NAME: Missing broll.mov\n* SOURCE FILE: " + named + "\n"
	in := TimelineReadIn{Content: edl, Name: "chosen.edl", ProjectID: p.Project.ID, FPS: 25}
	read := call[TimelineReadOut](t, a, "timeline.read", in)
	if read.Questions != 1 || read.Footage[0].AssetID != known.ID || read.Footage[1].How != "" || read.Footage[1].AssetID != "" || strings.Contains(read.Footage[1].Question, named) {
		t.Fatalf("the file the timeline names was looked at: %+v", read.Footage)
	}

	// Answers that aren't footage, or aren't files, are refused before anything is read further.
	png := filepath.Join(dir, "still.mov")
	_ = os.WriteFile(png, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...), 0o644)
	pdf := filepath.Join(dir, "notes.mov")
	_ = os.WriteFile(pdf, []byte("%PDF-1.7\n%…"), 0o644)
	for _, bad := range []string{dir, png, pdf} {
		_, err := CallTyped[TimelineReadOut](ctx, a, "timeline.read", TimelineReadIn{Content: edl, Name: "chosen.edl", ProjectID: p.Project.ID, FPS: 25,
			Answers: []TimelineAnswer{{Footage: "Missing broll.mov", Add: bad}}}, Caller{})
		if apperr.From(err).Code != apperr.InvalidInput {
			t.Fatalf("answered with %s: %v", filepath.Base(bad), err)
		}
	}
	// Over the network a file on this computer is never named.
	if _, err := CallTyped[TimelineReadOut](ctx, a, "timeline.read", TimelineReadIn{Content: edl, Name: "chosen.edl", ProjectID: p.Project.ID, FPS: 25,
		Answers: []TimelineAnswer{{Footage: "Missing broll.mov", Add: named}}}, Caller{Remote: true}); apperr.From(err).Code != apperr.Unauthorized {
		t.Fatalf("a file named from far away: %v", err)
	}

	// The person answers with that file: its fingerprint finds it, nothing is added.
	ans := []TimelineAnswer{{Footage: "Missing broll.mov", Add: named}}
	dry := call[TimelineImportOut](t, a, "timeline.import", TimelineImportIn{Content: edl, Name: "chosen.edl", ProjectID: p.Project.ID, FPS: 25, Answers: ans, DryRun: true})
	if ft := dry.Read.Footage[1]; ft.How != "same-file" || ft.AssetID != inLibrary.ID {
		t.Fatalf("answered with a file the Library has: %+v", ft)
	}

	// A file the Library doesn't have is added only once the import is confirmed.
	fresh, _ := fakeFootage(t, dir, "Fresh broll.mov")
	ans = []TimelineAnswer{{Footage: "Missing broll.mov", Add: fresh}}
	dry = call[TimelineImportOut](t, a, "timeline.import", TimelineImportIn{Content: edl, Name: "chosen.edl", ProjectID: p.Project.ID, FPS: 25, Answers: ans, DryRun: true})
	if ft := dry.Read.Footage[1]; ft.How != "will-add" || dry.Check == nil || dry.Check.ConfirmToken == "" {
		t.Fatalf("will add: %+v %+v", ft, dry.Check)
	}
	assets := func() int64 {
		n, _ := a.DB.C(store.CAssets).CountDocuments(ctx, map[string]any{"workspaceId": a.WorkspaceID()})
		return n
	}
	before := assets()
	_, err := CallTyped[TimelineImportOut](ctx, a, "timeline.import", TimelineImportIn{Content: edl, Name: "chosen.edl", ProjectID: p.Project.ID, FPS: 25, Answers: ans,
		ConfirmToken: "ct_1999999999_00000000000000000000000000000000"}, Caller{})
	if apperr.From(err).Code != apperr.ConfirmRequired || assets() != before {
		t.Fatalf("a token that doesn't check out: %v (%d → %d clips)", err, before, assets())
	}
	// A token for another import doesn't add it either.
	other := call[TimelineImportOut](t, a, "timeline.import", TimelineImportIn{Content: edl, Name: "chosen.edl", ProjectID: p.Project.ID, FPS: 25,
		Answers: []TimelineAnswer{{Footage: "Missing broll.mov", Skip: true}}, CutName: "Another", DryRun: true})
	_, err = CallTyped[TimelineImportOut](ctx, a, "timeline.import", TimelineImportIn{Content: edl, Name: "chosen.edl", ProjectID: p.Project.ID, FPS: 25, Answers: ans,
		ConfirmToken: other.Check.ConfirmToken}, Caller{})
	if apperr.From(err).Code != apperr.ConfirmRequired || assets() != before {
		t.Fatalf("another import's token: %v (%d → %d clips)", err, before, assets())
	}

	// With the token of this import's check, the file is added and the cut uses it.
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) || !ts.Has(tools.FFprobe) {
		t.Skip("ffmpeg not installed")
	}
	real := filepath.Join(dir, "Real broll.mp4")
	if out, err := exec.Command(ts.Path(tools.FFmpeg), "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=160x90:rate=25:duration=6",
		"-pix_fmt", "yuv420p", real).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	ans = []TimelineAnswer{{Footage: "Missing broll.mov", Add: real}}
	dry = call[TimelineImportOut](t, a, "timeline.import", TimelineImportIn{Content: edl, Name: "chosen.edl", ProjectID: p.Project.ID, FPS: 25, Answers: ans, DryRun: true})
	made := call[TimelineImportOut](t, a, "timeline.import", TimelineImportIn{Content: edl, Name: "chosen.edl", ProjectID: p.Project.ID, FPS: 25, Answers: ans, ConfirmToken: dry.Check.ConfirmToken})
	if !made.Saved || made.Read.Footage[1].How != "added" || made.Read.Shots != 2 || assets() != before+1 {
		t.Fatalf("added: %+v (%d → %d clips)", made.Read.Footage, before, assets())
	}
}

// noted is an FCPXML timeline whose one file carries MetaRush's own note.
func noted(assetID, sha string, picture bool) string {
	md := `<md key="metarush.assetId" value="` + assetID + `"/>`
	if sha != "" {
		md += `<md key="metarush.sha256" value="` + sha + `"/>`
	}
	hasVideo := "1"
	if !picture {
		hasVideo = "0"
	}
	return `<fcpxml version="1.10"><resources><format id="f" frameDuration="1/25s"/>` +
		`<asset id="a" name="Somewhere else" start="0s" duration="20s" hasVideo="` + hasVideo + `" hasAudio="1"><metadata>` + md + `</metadata></asset></resources>` +
		`<library><event><project name="Noted"><sequence format="f"><spine><asset-clip ref="a" offset="0s" start="1s" duration="4s"/></spine></sequence></project></event></library></fcpxml>`
}

// MetaRush's note on a file is words anyone can write: it is believed with
// the clip's bytes, or without them only for a clip of the project the cut
// goes into, and never for a clip with no picture where the cut shows one.
func TestTimelineImportBelievesNotesOnlyWithTheirBytes(t *testing.T) {
	a := testapp.New(t)
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Ours"})
	q := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Another client"})
	video := models.MediaInfo{Duration: 20, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 25}
	theirs := storedAsset(t, a, q.Project.ID, "Their interview.mov", "video", video)
	ours := storedAsset(t, a, p.Project.ID, "Our interview.mov", "video", video)
	sound := storedAsset(t, a, p.Project.ID, "Our sound.wav", "audio", models.MediaInfo{Duration: 20, HasAudio: true})
	how := func(doc string) TimelineFootage {
		t.Helper()
		r := call[TimelineReadOut](t, a, "timeline.read", TimelineReadIn{Content: doc, Name: "noted.fcpxml", ProjectID: p.Project.ID})
		return r.Footage[0]
	}
	if ft := how(noted(theirs.ID, "", true)); ft.How == "metarush" || ft.AssetID == theirs.ID {
		t.Fatalf("another client's clip taken on a note alone: %+v", ft)
	}
	if ft := how(noted(theirs.ID, theirs.SHA256, true)); ft.How != "metarush" || ft.AssetID != theirs.ID {
		t.Fatalf("with its bytes: %+v", ft)
	}
	if ft := how(noted(ours.ID, "", true)); ft.How != "metarush" || ft.AssetID != ours.ID {
		t.Fatalf("a clip of this project: %+v", ft)
	}
	if ft := how(noted(sound.ID, sound.SHA256, true)); ft.AssetID == sound.ID {
		t.Fatalf("a sound file standing in for a picture: %+v", ft)
	}
}

// The confirm token covers which file an import adds: a token from a check
// that adds one file doesn't add another.
func TestTimelineImportTokenCoversTheFileAdded(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Adds"})
	video := models.MediaInfo{Duration: 20, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 25}
	storedAsset(t, a, p.Project.ID, "Interview.mov", "video", video)
	edl := "TITLE: Adds\nFCM: NON-DROP FRAME\n\n" +
		"001  AX       AA/V  C        00:00:01:00 00:00:05:00 01:00:00:00 01:00:04:00\n* FROM CLIP NAME: Interview.mov\n" +
		"002  AX       V     C        00:00:02:00 00:00:04:00 01:00:04:00 01:00:06:00\n* FROM CLIP NAME: Missing broll.mov\n"
	dir := t.TempDir()
	chosen, _ := fakeFootage(t, dir, "Chosen.mov")
	swapped, _ := fakeFootage(t, dir, "Swapped.mov")
	in := TimelineImportIn{Content: edl, Name: "adds.edl", ProjectID: p.Project.ID, FPS: 25, Answers: []TimelineAnswer{{Footage: "Missing broll.mov", Add: chosen}}, DryRun: true}
	dry := call[TimelineImportOut](t, a, "timeline.import", in)
	if dry.Check == nil || dry.Check.ConfirmToken == "" {
		t.Fatalf("check: %+v", dry.Check)
	}
	clips := func() int64 {
		n, _ := a.DB.C(store.CAssets).CountDocuments(ctx, map[string]any{"workspaceId": a.WorkspaceID()})
		return n
	}
	before := clips()
	in.DryRun, in.ConfirmToken, in.Answers = false, dry.Check.ConfirmToken, []TimelineAnswer{{Footage: "Missing broll.mov", Add: swapped}}
	_, err := CallTyped[TimelineImportOut](ctx, a, "timeline.import", in, Caller{})
	if apperr.From(err).Code != apperr.ConfirmRequired || clips() != before {
		t.Fatalf("another file on the same token: %v (%d → %d clips)", err, before, clips())
	}
}
