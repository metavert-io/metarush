package fixes_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/calls"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// webcam makes a shaky webcam picture from one of the fixture photographs
// (3 s at 30 fps), with a tone for its sound.
func (f *flow) webcam(name, photo, jitter string, secs int) string {
	f.t.Helper()
	d := strconv.Itoa(secs)
	return f.ffmpeg(name, "-loop", "1", "-framerate", "30", "-t", d, "-i", filepath.Join("..", "..", "tests", "fixtures", "images", photo),
		"-f", "lavfi", "-i", "sine=frequency=330:duration="+d,
		"-vf", "scale=720:420:force_original_aspect_ratio=increase,crop=720:420,crop=640:360:"+jitter,
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "16", "-c:a", "aac", "-shortest")
}

// frames counts the frames of a stored file.
func (f *flow) frames(key, sha string, size int64) int {
	f.t.Helper()
	p, err := f.a.Cache.Ensure(context.Background(), f.a.Media, key, sha, size, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=nb_read_frames", "-of", "csv=p=0", p).Output()
	if err != nil {
		f.t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(string(out)), ","))
	if err != nil {
		f.t.Fatalf("frames of %s: %q", p, out)
	}
	return n
}

// "Steady the camera" for a call recording (D190): offered and measured like
// a camera video's (between the picture's switches), and made one stretch at
// a time — each stretch between two switches steadied on its own — with every
// frame kept and the switch where it was.
func TestSteadyingACallStretchByStretch(t *testing.T) {
	f := newFlow(t)
	ctx := context.Background()
	f.basicSteadying() // vid.stab is in TestSteadyingStaysWithinEachPicture
	jitter := "x='40+16*sin(n*1.7)*cos(n*0.6)':y='30+12*sin(n*2.1+1)'"
	ada, ben := f.webcam("ada.mp4", "beach.jpg", jitter, 3), f.webcam("ben.mp4", "portrait.jpg", jitter, 3)
	// Speaker view: Ada for 3 s, then the picture switches to Ben.
	call := f.ffmpeg("zoom garden call.mp4", "-i", ada, "-i", ben, "-filter_complex", "[0:v][0:a][1:v][1:a]concat=n=2:v=1:a=1[v][a]", "-map", "[v]", "-map", "[a]",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "16", "-c:a", "aac")
	as := f.ingest(call)
	// The switches job found the switch to the frame (D160).
	if _, err := calls.Save(ctx, f.a.DB, f.a.WorkspaceID(), as.ID, &calls.Stored{Version: calls.Version, Source: "original", FPS: 30, Duration: 6,
		Switches: []calls.Switch{{At: 3, Last: 2.967, Score: 0.9, By: "scene"}}}, "test"); err != nil {
		t.Fatal(err)
	}
	f.measure(as)
	v := f.view(as.ID)
	if v.Kind != models.SourceCall || len(v.Fixes) != 4 || v.Fixes[3].ID != models.FixSteady {
		t.Fatalf("a call's fixes, Steady the camera last: %+v", v.Fixes)
	}
	steady := v.Fixes[3]
	if !steady.Fits || !strings.Contains(steady.Why, "shakes") || !strings.Contains(steady.Measured, "between the picture's switches") {
		t.Fatalf("the webcams shake, measured between the switches: %+v", steady)
	}
	// Ticking it is no longer refused for a call.
	v = f.tick(fixes.SetIn{ID: as.ID, Tick: []string{models.FixSteady}})
	if v.State != models.FixPreparing || len(v.JobIDs) != 1 {
		t.Fatalf("after ticking: %+v", v)
	}
	f.prepare(v.JobIDs[0])
	as = f.asset(as.ID)
	if !as.Fixed() {
		t.Fatalf("the call uses its steady copy: %+v", as.Fixes)
	}
	var c models.SourceCopy
	if err := f.a.DB.GetByID(ctx, store.CSourceCopies, f.a.WorkspaceID(), as.Fixes.Copy.ID, &c); err != nil {
		t.Fatal(err)
	}
	st := c.FixSteps.Steps[slices.IndexFunc(c.FixSteps.Steps, func(s models.FixStep) bool { return s.Fix == models.FixSteady })]
	if st.Params["switches"] != "1" || st.Params["stretches"] != "2" || st.Params["switchesFrom"] != calls.Version {
		t.Fatalf("steadied stretch by stretch: %+v", st)
	}
	if !slices.ContainsFunc(c.Notes, func(n string) bool {
		return strings.Contains(n, "each stretch between switches was steadied on its own")
	}) {
		t.Fatalf("the copy says how it was steadied: %v", c.Notes)
	}
	// As many frames as the call itself has: two 3-second webcams at 30 fps
	// make 180 with Homebrew's ffmpeg and 181 with Ubuntu's.
	want := f.frames(as.StorageKey, as.SHA256, as.Size)
	if want < 175 || want > 185 {
		t.Fatalf("the call should be about 6 s at 30 fps: %d frames", want)
	}
	if got := f.frames(c.StorageKey, c.SHA256, c.Size); got != want {
		t.Fatalf("every frame kept: %d of %d", got, want)
	}
	v = f.view(as.ID)
	if v.Copy == nil || len(v.Copy.Measures) != 1 || v.Copy.Measures[0].After >= v.Copy.Measures[0].Before*0.7 {
		t.Fatalf("steadier, measured between the switches: %+v", v.Copy)
	}
}

// A conversation's recordings (D99) are calls, and each member's own
// recording is steadied on its own: its own copy, made from its own picture
// (one webcam, no switches: one stretch), the other recordings untouched until
// someone ticks them too.
func TestEachRecordingOfAConversationIsSteadiedOnItsOwn(t *testing.T) {
	f := newFlow(t)
	ctx := context.Background()
	t.Setenv("METARUSH_STABILIZER_ZIP", filepath.Join(t.TempDir(), "none.zip"))
	a := f.ingest(f.webcam("ada-own.mp4", "beach.jpg", "x='40+16*sin(n*1.7)*cos(n*0.6)':y='30+12*sin(n*2.1+1)'", 3))
	b := f.ingest(f.webcam("ben-own.mp4", "dog.jpg", "x='40+14*sin(n*2.3)':y='30+10*sin(n*1.1+2)'", 3))
	now := time.Now().UTC()
	conv := models.Conversation{ID: ids.New(ids.Conversation), WorkspaceID: f.a.WorkspaceID(), ProjectIDs: []string{f.project}, Name: "garden-chat", Length: 3,
		Members: []models.ConversationMember{{ConversationMember: editspec.ConversationMember{ID: "ada", Name: "Ada", AssetID: a.ID}},
			{ConversationMember: editspec.ConversationMember{ID: "ben", Name: "Ben", AssetID: b.ID}}}, CreatedAt: now}
	if err := f.a.DB.Insert(ctx, store.CConversations, conv); err != nil {
		t.Fatal(err)
	}
	for _, as := range []*models.Asset{a, b} {
		if v := f.view(as.ID); v.Kind != models.SourceCall || !slices.ContainsFunc(v.Fixes, func(r fixes.Row) bool { return r.ID == models.FixSteady }) {
			t.Fatalf("a conversation's recording is a call, offered steadying: %+v", v)
		}
	}
	va := f.tick(fixes.SetIn{ID: a.ID, Tick: []string{models.FixSteady}})
	f.prepare(va.JobIDs[0])
	if a, b = f.asset(a.ID), f.asset(b.ID); !a.Fixed() || b.Fixed() || b.Fixes != nil && b.Fixes.State != "" {
		t.Fatalf("only Ada's recording is steadied: %+v / %+v", a.Fixes, b.Fixes)
	}
	vb := f.tick(fixes.SetIn{ID: b.ID, Tick: []string{models.FixSteady}})
	if len(vb.JobIDs) != 1 || vb.JobIDs[0] == va.JobIDs[0] {
		t.Fatalf("Ben's recording gets its own job: %+v", vb.JobIDs)
	}
	f.prepare(vb.JobIDs[0])
	if a, b = f.asset(a.ID), f.asset(b.ID); !b.Fixed() || b.Fixes.Copy.ID == a.Fixes.Copy.ID || b.Fixes.Copy.SHA256 == a.Fixes.Copy.SHA256 {
		t.Fatalf("each recording has its own steady copy: %+v / %+v", a.Fixes.Copy, b.Fixes.Copy)
	}
	for _, as := range []*models.Asset{a, b} {
		var c models.SourceCopy
		if err := f.a.DB.GetByID(ctx, store.CSourceCopies, f.a.WorkspaceID(), as.Fixes.Copy.ID, &c); err != nil {
			t.Fatal(err)
		}
		if c.AssetID != as.ID || c.FixSteps.Steps[0].Params["stretches"] != "" {
			t.Fatalf("made from its own picture, one stretch (no switches in one webcam): %+v", c.FixSteps)
		}
		if got := f.frames(c.StorageKey, c.SHA256, c.Size); got != 90 {
			t.Fatalf("every frame kept: %d", got)
		}
	}
}
