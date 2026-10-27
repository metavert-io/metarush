package recorder

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// fixture is a workspace with a project and the files a browser would
// record: the screen (WebM when this ffmpeg makes it, else fragmented MP4 as
// Chrome and Safari record it), the camera (fragmented MP4) and the voice
// (16-bit mono samples).
type fixture struct {
	a       *app.App
	project string
	screen  []byte
	sformat string
	camera  []byte
	voice   []byte
}

const voiceRate = 48000

func newFixture(t *testing.T) *fixture {
	t.Helper()
	a := testapp.New(t)
	if !a.Tools.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	ctx := context.Background()
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	_ = a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now})
	pid := ids.New(ids.Project)
	if err := a.DB.Insert(ctx, store.CProjects, models.Project{ID: pid, WorkspaceID: ws, Name: "Launch video", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{a: a, project: pid}
	dir := t.TempDir()
	run := func(args ...string) []byte {
		t.Helper()
		out := args[len(args)-1]
		base := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}
		if _, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: append(base, args...)}); err != nil {
			t.Fatalf("ffmpeg %v: %v", args, err)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// The microphone heard beeps at irregular times; the pictures carry a copy
	// of it that starts 0.1 s before the voice's file (the voice belongs 0.1 s
	// into them).
	mic := filepath.Join(dir, "mic.wav")
	if err := os.WriteFile(mic, wavOf(beeps(voiceRate, 3, voiceLag)), 0o600); err != nil {
		t.Fatal(err)
	}
	fmp4 := func(name, src string) []byte {
		return run("-f", "lavfi", "-i", src+"=size=320x180:rate=30:duration=3", "-i", mic,
			"-c:v", "mpeg4", "-c:a", "aac", "-shortest", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", filepath.Join(dir, name))
	}
	if a.Tools.HasEncoder(ctx, "libvpx") && a.Tools.HasEncoder(ctx, "libopus") {
		f.screen = run("-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30:duration=3", "-i", mic,
			"-c:v", "libvpx", "-deadline", "realtime", "-c:a", "libopus", "-shortest", "-f", "webm", filepath.Join(dir, "screen.webm"))
		f.sformat = "video/webm;codecs=vp8,opus"
	} else {
		f.screen = fmp4("screen.mp4", "testsrc2")
		f.sformat = "video/mp4"
	}
	f.camera = fmp4("camera.mp4", "smptebars")
	f.voice = pcmOf(beeps(voiceRate, 3, 0))
	return f
}

// voiceLag is how much later the voice's file starts than the pictures' (by their sound).
const voiceLag = 0.1

// beeps are 60 ms beeps of 1 kHz at irregular times (so sound lines up only
// one way), shifted later by `shift` seconds.
func beeps(rate int, secs, shift float64) []int16 {
	out := make([]int16, int(float64(rate)*secs))
	gaps := []float64{0.31, 0.47, 0.23, 0.61, 0.37, 0.29, 0.53}
	at := 0.2
	for k := 0; at < secs-0.2; k++ {
		i0 := int((at + shift) * float64(rate))
		for j := 0; j < rate*6/100 && i0+j < len(out); j++ {
			out[i0+j] = int16(16000 * math.Sin(2*math.Pi*1000*float64(j)/float64(rate)))
		}
		at += gaps[k%len(gaps)]
	}
	return out
}

func pcmOf(s []int16) []byte {
	b := make([]byte, 2*len(s))
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func wavOf(s []int16) []byte {
	var w bytes.Buffer
	pcm := pcmOf(s)
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], voiceRate)
	binary.LittleEndian.PutUint32(h[28:], voiceRate*2)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	w.Write(h)
	w.Write(pcm)
	return w.Bytes()
}

func (f *fixture) create(t *testing.T, tracks ...string) *models.Recording {
	t.Helper()
	return f.createWith(t, "mic", tracks...)
}

// createWith starts a recording whose screen's sound is screenSound (own or mic).
func (f *fixture) createWith(t *testing.T, screenSound string, tracks ...string) *models.Recording {
	t.Helper()
	in := &RecordingCreateIn{ProjectID: f.project, Name: "Product demo", Layout: "pip"}
	for _, id := range tracks {
		switch id {
		case "screen":
			in.Tracks = append(in.Tracks, RecordingTrackIn{ID: "screen", Label: "Docs", Surface: "window", Format: f.sformat, Sound: screenSound, Width: 320, Height: 180})
		case "camera":
			in.Tracks = append(in.Tracks, RecordingTrackIn{ID: "camera", Label: "FaceTime HD Camera", Format: "video/mp4", Sound: "mic", Width: 320, Height: 180})
		case "voice":
			in.Tracks = append(in.Tracks, RecordingTrackIn{ID: "voice", Label: "MacBook Microphone", Format: FormatPCM, Rate: voiceRate})
		}
	}
	r, err := Create(context.Background(), f.a, in, false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *fixture) bytesOf(track string) []byte {
	switch track {
	case "screen":
		return f.screen
	case "camera":
		return f.camera
	}
	return f.voice
}

// send appends a track's bytes in pieces, as the browser does.
func (f *fixture) send(t *testing.T, id, track string, b []byte) {
	t.Helper()
	ctx := context.Background()
	const piece = 16 << 10
	for off := 0; off < len(b); off += piece {
		end := min(len(b), off+piece)
		n, err := Append(ctx, f.a, id, track, int64(off), int64(end-off), bytes.NewReader(b[off:end]))
		if err != nil {
			t.Fatalf("%s piece at %d: %v", track, off, err)
		}
		if n != int64(end) {
			t.Fatalf("%s offset %d after a piece, want %d", track, n, end)
		}
	}
}

func clockFor(f *fixture, tracks ...string) *RecordingClockIn {
	c := &RecordingClockIn{Length: 3, Spread: 4, Ended: "stopped"}
	for _, id := range tracks {
		tc := RecordingTrackClock{ID: id, Bytes: int64(len(f.bytesOf(id)))}
		if id == "voice" {
			tc.Start, tc.Drift = 0.042, 21.4
		}
		c.Tracks = append(c.Tracks, tc)
	}
	return c
}

// A recording sent in pieces becomes three clips in the project, one
// conversation lined up by its clock, and a cut with the camera in a corner.
func TestRecordingBecomesLinkedFootageAndACut(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.create(t, "screen", "camera", "voice")
	if r.State != models.RecordingRecording || len(r.Tracks) != 3 || r.Tracks[0].ID != "screen" || r.Tracks[2].ID != "voice" {
		t.Fatalf("created %+v", r)
	}
	for _, tr := range []string{"screen", "camera", "voice"} {
		f.send(t, r.ID, tr, f.bytesOf(tr))
	}
	// A piece for the wrong place is refused with the host's offset.
	if _, err := Append(ctx, f.a, r.ID, "voice", 10, 2, strings.NewReader("xx")); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("a piece at the wrong offset: %v", err)
	} else if d, _ := apperr.From(err).Details.(map[string]any); d["offset"] != int64(len(f.voice)) {
		t.Fatalf("details %v", apperr.From(err).Details)
	}
	if _, err := SetClock(ctx, f.a, r.ID, clockFor(f, "screen", "camera", "voice")); err != nil {
		t.Fatal(err)
	}
	out, err := Finish(ctx, f.a, &RecordingFinishIn{Recording: r.ID, Clock: clockFor(f, "screen", "camera", "voice")})
	if err != nil {
		t.Fatal(err)
	}
	got := out.Recording
	if got.State != models.RecordingReady {
		t.Fatalf("state %s: %s %+v", got.State, got.Error, got.Tracks)
	}
	if _, err := os.Stat(dir(f.a, r.ID)); !os.IsNotExist(err) {
		t.Fatal("the pieces are still on disk after the recording became footage")
	}
	for _, tr := range got.Tracks {
		var as models.Asset
		if err := f.a.DB.GetByID(ctx, store.CAssets, f.a.WorkspaceID(), tr.AssetID, &as); err != nil {
			t.Fatalf("%s: no clip: %v", tr.ID, err)
		}
		if !contains(as.ProjectIDs, f.project) || !as.HasTag("screen-recording") || !as.HasTag(tr.ID) {
			t.Fatalf("%s clip %+v", tr.ID, as)
		}
		if as.Media.Duration < 2.5 || as.Media.Duration > 3.5 {
			t.Fatalf("%s lasts %.2f s", tr.ID, as.Media.Duration)
		}
		if tr.ID == "voice" && (as.Media.HasVideo || !as.Media.HasAudio) {
			t.Fatalf("the voice: %+v", as.Media)
		}
	}
	if len(got.Links) != 1 || got.Links[0].ProjectID != f.project {
		t.Fatalf("links %+v", got.Links)
	}
	var c models.Conversation
	if err := f.a.DB.GetByID(ctx, store.CConversations, f.a.WorkspaceID(), got.Links[0].ConversationID, &c); err != nil {
		t.Fatal(err)
	}
	// The pictures carry the microphone: their sound puts the voice 0.1 s in
	// (the clock had said 0.042 s); fairly sure on three seconds and close to
	// the clock, so the sound's alignment is kept.
	if c.Source == nil || c.Source.Provider != "recorder" || c.Sync == nil || c.Sync.Method != "audio" || c.Sync.Confidence < 0.8 {
		t.Fatalf("conversation %+v %+v", c.Source, c.Sync)
	}
	screen, _ := c.Member("screen")
	voice, _ := c.Member("voice")
	cam, _ := c.Member("camera")
	if screen == nil || voice == nil || cam == nil || screen.UseOf() != editspec.UsePicture || voice.UseOf() != editspec.UseSound || cam.UseOf() != editspec.UsePicture {
		t.Fatalf("members %+v", c.Members)
	}
	// Within a frame: an AAC copy starts with the encoder's priming (about 21 ms).
	if math.Abs(voice.Start-screen.Start-voiceLag) > 0.03 || math.Abs(cam.Start-screen.Start) > 0.03 || screen.Framing == nil || screen.Framing.Mode != "fit" {
		t.Fatalf("screen %+v camera %+v voice %+v", screen.ConversationMember, cam.ConversationMember, voice.ConversationMember)
	}
	// The recording keeps what its clock measured.
	if v := got.Track("voice"); v.Start != 0.042 || v.Drift != 21.4 {
		t.Fatalf("the voice on the recording's clock: %+v", v)
	}

	// The first cut is the project's main cut: one clip, the screen with the camera in a corner.
	co, err := ops.CallTyped[RecordingCutOut](ctx, f.a, "recording.cut", RecordingCutIn{Recording: r.ID, Apply: true}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if co.Applied == nil || co.Applied.Problem != "" || co.Branch != "main" || !co.NewCut || co.Layout != "pip" {
		t.Fatalf("cut %+v applied %+v (%s)", co.LayoutCutOut, co.Applied, co.Summary)
	}
	var rev models.Revision
	if err := f.a.DB.GetByID(ctx, store.CRevisions, f.a.WorkspaceID(), co.Applied.RevisionID, &rev); err != nil {
		t.Fatal(err)
	}
	pin, ok := rev.Spec.Conversations["call"]
	if !ok || pin.ID != c.ID || pin.NameTags != "off" || pin.Gate != nil || !pin.SampleAligned || len(rev.Spec.Clips) != 1 {
		t.Fatalf("spec %+v", rev.Spec)
	}
	sh := rev.Spec.Clips[0].Shots
	if len(sh) != 1 || sh[0].Layout != editspec.LayoutPiP || strings.Join(sh[0].Members, ",") != "screen,camera" {
		t.Fatalf("shots %+v", sh)
	}
	if rev.Origin != "recording:"+r.ID {
		t.Fatalf("origin %q", rev.Origin)
	}
	// Another cut starts a cut of its own, named after the recording.
	co2, err := ops.CallTyped[RecordingCutOut](ctx, f.a, "recording.cut", RecordingCutIn{Recording: r.ID, Layout: "side-by-side", Apply: true}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if co2.Branch != "product-demo" || co2.Applied == nil || co2.Applied.Problem != "" {
		t.Fatalf("second cut %+v %+v", co2.LayoutCutOut, co2.Applied)
	}
	var p models.Project
	_ = f.a.DB.GetByID(ctx, store.CProjects, f.a.WorkspaceID(), f.project, &p)
	if p.Branches["product-demo"].Title != "Product demo" {
		t.Fatalf("the new cut's name: %+v", p.Branches["product-demo"])
	}

	// list and show say it plainly.
	lo, err := ops.CallTyped[RecordingListOut](ctx, f.a, "recording.list", RecordingListIn{ProjectID: f.project}, ops.Caller{})
	if err != nil || len(lo.Recordings) != 1 {
		t.Fatalf("list %+v %v", lo, err)
	}
	v := lo.Recordings[0]
	if !strings.Contains(v.Text, "“Product demo”: 0:03, the screen, camera and voice; in the Library.") || len(v.Links) != 1 || v.Links[0].ProjectName != "Launch video" || !v.Here {
		t.Fatalf("view %q %+v", v.Text, v.Links)
	}
	if !strings.Contains(v.Tracks[0].Text, "a window") || v.Tracks[0].Title != "Product demo (screen)" {
		t.Fatalf("screen track %+v", v.Tracks[0])
	}
	// Linking again lines it up again in the same conversation (never a second one).
	if _, err := ops.CallTyped[RecordingInfo](ctx, f.a, "recording.link", RecordingLinkIn{Recording: r.ID, Project: f.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	n, _ := f.a.DB.C(store.CConversations).CountDocuments(ctx, bson.M{"workspaceId": f.a.WorkspaceID()})
	if n != 1 {
		t.Fatalf("%d conversations after linking twice", n)
	}
}

// Finishing waits for pieces the browser recorded but the host doesn't have;
// keeping what arrived makes footage of a recording cut off mid-piece.
func TestFinishWaitsForPiecesOrKeepsWhatArrived(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.create(t, "camera", "voice")
	cut := len(f.camera) * 2 / 3
	f.send(t, r.ID, "camera", f.camera[:cut])
	f.send(t, r.ID, "voice", f.voice[:len(f.voice)/2])
	_, err := Finish(ctx, f.a, &RecordingFinishIn{Recording: r.ID, Clock: clockFor(f, "camera", "voice")})
	if !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "hasn't arrived yet") {
		t.Fatalf("finish with pieces missing: %v", err)
	}
	// Without the browser (a closed tab), a recording whose pieces arrived a moment ago is still recording.
	if _, err := Finish(ctx, f.a, &RecordingFinishIn{Recording: r.ID}); !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "still being recorded") {
		t.Fatalf("finish while pieces arrive: %v", err)
	}
	out, err := Finish(ctx, f.a, &RecordingFinishIn{Recording: r.ID, Keep: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Recording.State != models.RecordingReady || out.Recording.Ended != "kept" {
		t.Fatalf("kept: %s %q %+v", out.Recording.State, out.Recording.Error, out.Recording.Tracks)
	}
	var as models.Asset
	if err := f.a.DB.GetByID(ctx, store.CAssets, f.a.WorkspaceID(), out.Recording.Track("voice").AssetID, &as); err != nil || math.Abs(as.Media.Duration-1.5) > 0.05 {
		t.Fatalf("the voice kept: %.3f s, %v", as.Media.Duration, err)
	}
	if err := f.a.DB.GetByID(ctx, store.CAssets, f.a.WorkspaceID(), out.Recording.Track("camera").AssetID, &as); err != nil || as.Media.Duration < 0.5 || !as.Media.HasVideo {
		t.Fatalf("the camera kept up to where it was cut off: %+v, %v", as.Media, err)
	}
	// No clock arrived (the files are taken to start together), but the sound
	// still lines the voice up, and the length comes from the footage.
	var c models.Conversation
	_ = f.a.DB.GetByID(ctx, store.CConversations, f.a.WorkspaceID(), out.Recording.Links[0].ConversationID, &c)
	cam, _ := c.Member("camera")
	voice, _ := c.Member("voice")
	if c.Sync == nil || c.Sync.Method != "audio" || cam == nil || voice == nil || math.Abs(voice.Start-cam.Start-voiceLag) > 0.03 {
		t.Fatalf("sync %+v members %+v", c.Sync, c.Members)
	}
	if out.Recording.Length < 1.4 {
		t.Fatalf("length %.3f", out.Recording.Length)
	}
	// Finishing again is the same answer.
	if again, err := Finish(ctx, f.a, &RecordingFinishIn{Recording: r.ID}); err != nil || again.Recording.State != models.RecordingReady {
		t.Fatalf("again: %v", err)
	}
}

// Pieces come only from the person recording, on the computer receiving it,
// while it records; a recording becomes footage only where its pieces are.
func TestPiecesAreTheRecordersAndStayWhereTheyArrived(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.create(t, "voice")
	other := *f.a
	other.Actor.UserID = ids.New(ids.User)
	if _, err := Append(ctx, &other, r.ID, "voice", 0, 2, strings.NewReader("ab")); !apperr.Is(err, apperr.Unauthorized) {
		t.Fatalf("another person's piece: %v", err)
	}
	if _, err := Append(ctx, f.a, r.ID, "screen", 0, 2, strings.NewReader("ab")); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("a track it doesn't have: %v", err)
	}
	elsewhere := *f.a
	cfg := *f.a.Cfg
	cfg.Installation.ID, cfg.Installation.Name = ids.New(ids.Installation), "Sam's MacBook"
	elsewhere.Cfg = &cfg
	if _, err := Append(ctx, &elsewhere, r.ID, "voice", 0, 2, strings.NewReader("ab")); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("a piece on another computer: %v", err)
	}
	if _, err := Finish(ctx, &elsewhere, &RecordingFinishIn{Recording: r.ID, Keep: true}); !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "test-machine") {
		t.Fatalf("finished on another computer: %v", err)
	}
	// Bad tracks are refused when it starts.
	for _, in := range []*RecordingCreateIn{
		{Tracks: []RecordingTrackIn{{ID: "voice", Format: "audio/webm", Rate: 48000}}},
		{Tracks: []RecordingTrackIn{{ID: "screen", Format: "audio/pcm"}}},
		{Tracks: []RecordingTrackIn{{ID: "camera", Format: "video/mp4"}, {ID: "camera", Format: "video/mp4"}}},
		{Tracks: []RecordingTrackIn{{ID: "tripod", Format: "video/mp4"}}},
		{Tracks: nil},
	} {
		if _, err := Create(ctx, f.a, in, false); !apperr.Is(err, apperr.InvalidInput) {
			t.Fatalf("%+v: %v", in.Tracks, err)
		}
	}
	// Thrown away: its pieces go and it takes no more.
	f.send(t, r.ID, "voice", f.voice[:1000])
	if _, err := ops.CallTyped[RecordingInfo](ctx, f.a, "recording.discard", RecordingRefIn{Recording: r.ID}, ops.Caller{}); !apperr.Is(err, apperr.HumanOnly) {
		t.Fatalf("discard without a person: %v", err)
	}
	if _, err := ops.CallTyped[RecordingInfo](ctx, f.a, "recording.discard", RecordingRefIn{Recording: r.ID}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir(f.a, r.ID)); !os.IsNotExist(err) {
		t.Fatal("pieces kept after throwing it away")
	}
	if _, err := Append(ctx, f.a, r.ID, "voice", 1000, 2, strings.NewReader("ab")); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("a piece after throwing it away: %v", err)
	}
	lo, _ := ops.CallTyped[RecordingListOut](ctx, f.a, "recording.list", RecordingListIn{}, ops.Caller{})
	if len(lo.Recordings) != 0 {
		t.Fatalf("a thrown-away recording is listed: %+v", lo.Recordings)
	}
}

// Without a camera the cut shows the screen; asking for the camera in a
// corner says plainly why it can't.
func TestCutWithoutACamera(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.createWith(t, "own", "screen", "voice")
	f.send(t, r.ID, "screen", f.screen)
	f.send(t, r.ID, "voice", f.voice)
	if _, err := Finish(ctx, f.a, &RecordingFinishIn{Recording: r.ID, Clock: clockFor(f, "screen", "voice")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[RecordingCutOut](ctx, f.a, "recording.cut", RecordingCutIn{Recording: r.ID, Layout: "side-by-side"}, ops.Caller{}); !apperr.Is(err, apperr.InvalidInput) || !strings.Contains(err.Error(), "has no camera") {
		t.Fatalf("side by side without a camera: %v", err)
	}
	co, err := ops.CallTyped[RecordingCutOut](ctx, f.a, "recording.cut", RecordingCutIn{Recording: r.ID}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if co.Layout != "screen" || co.Result == nil || co.Result.Check == nil || !co.Result.Check.Valid || co.Applied != nil {
		t.Fatalf("cut %+v", co)
	}
	// The screen had sound of its own, so nothing can check the clock by sound: it stays.
	var c models.Conversation
	_ = f.a.DB.GetByID(ctx, store.CConversations, f.a.WorkspaceID(), co.Result.Plan.Spec.Conversations["call"].ID, &c)
	if c.Sync == nil || c.Sync.Method != "clock" || c.Sync.Reference != "voice" {
		t.Fatalf("sync %+v", c.Sync)
	}
	if v, _ := c.Member("voice"); v == nil || v.Start != 0.042 || v.Drift != 21.4 || v.Align == nil || v.Align.Method != "reference" {
		t.Fatalf("voice %+v", v)
	}
	if sh := co.Result.Plan.Spec.Clips[0].Shots; len(sh) != 1 || sh[0].Member != "screen" {
		t.Fatalf("shots %+v", sh)
	}
}
