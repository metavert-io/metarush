package fixes_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// says gives a clip a transcript: who speaks from when to when (label, name).
func (f *flow) says(as *models.Asset, lines ...[4]any) {
	f.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for i, l := range lines {
		seg := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: f.a.WorkspaceID(), AssetID: as.ID, Index: i, From: l[0].(float64), To: l[1].(float64),
			Speaker: l[2].(string), SpeakerName: l[3].(string), Text: "we planted tomatoes in the garden this spring", CreatedAt: now}
		if err := f.a.DB.Insert(ctx, store.CSegments, seg); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := f.a.DB.Update(ctx, store.CAssets, f.a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"transcript": models.TranscriptStatus{State: "available", Segments: len(lines), Words: 8 * len(lines)}}}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *flow) row(v *fixes.View, id string) fixes.Row {
	f.t.Helper()
	i := slices.IndexFunc(v.Fixes, func(r fixes.Row) bool { return r.ID == id })
	if i < 0 {
		f.t.Fatalf("no %s row in %+v", id, v.Fixes)
	}
	return v.Fixes[i]
}

func (f *flow) copyOf(as *models.Asset) *models.SourceCopy {
	f.t.Helper()
	var c models.SourceCopy
	if err := f.a.DB.GetByID(context.Background(), store.CSourceCopies, f.a.WorkspaceID(), as.Fixes.Copy.ID, &c); err != nil {
		f.t.Fatal(err)
	}
	return &c
}

func stepOf(c *models.SourceCopy, fix string) *models.FixStep {
	for i := range c.FixSteps.Steps {
		if c.FixSteps.Steps[i].Fix == fix {
			return &c.FixSteps.Steps[i]
		}
	}
	return nil
}

// A call where one person is much quieter (D153): MetaRush says who is
// quieter than whom from the transcript's speakers, and "Even out each
// person's volume" brings each to the middle level over their own stretches.
func TestQuietPersonOnACallIsBroughtUp(t *testing.T) {
	f := newFlow(t)
	call := f.ffmpeg("weekly-call.mp4", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=15:duration=12",
		"-f", "lavfi", "-i", "aevalsrc='if(lt(t,6),0.5,0.08)*sin(2*PI*(210+30*sin(2*PI*3*t))*t)':s=48000:d=12",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")
	as := f.ingest(call)
	f.says(as, [4]any{0.2, 5.9, "speaker_00", "Ada"}, [4]any{6.1, 11.8, "speaker_01", ""})
	v := f.tick(fixes.SetIn{ID: as.ID, Kind: models.SourceCall})
	if v.Kind != models.SourceCall || v.KindBy != models.TitleByPerson {
		t.Fatalf("a call: %+v", v)
	}
	f.measure(f.asset(as.ID))
	v = f.view(as.ID)
	vol := f.row(v, models.FixVolumes)
	if !vol.Fits || !strings.HasPrefix(vol.Measured, "Speaker 2 is ") || !strings.HasSuffix(vol.Measured, " dB quieter than Ada") || vol.Why != "Speaker 2 is much quieter than Ada" {
		t.Fatalf("volumes: %+v", vol)
	}
	v = f.tick(fixes.SetIn{ID: as.ID, Tick: []string{models.FixVolumes}})
	f.prepare(v.JobIDs[0])
	as = f.asset(as.ID)
	if !as.Fixed() {
		t.Fatalf("the clip uses its fixed copy: %+v", as.Fixes)
	}
	st := stepOf(f.copyOf(as), models.FixVolumes)
	if st == nil || st.Engine != "per-person gain" || !strings.HasPrefix(st.Params["speaker_01"], "+") || !strings.HasPrefix(st.Params["speaker_00"], "-") {
		t.Fatalf("each person to the middle level: %+v", st)
	}
	v = f.view(as.ID)
	if v.Copy == nil || len(v.Copy.Measures) != 1 || v.Copy.Measures[0].After >= v.Copy.Measures[0].Before || f.row(v, models.FixVolumes).After == "" {
		t.Fatalf("closer after the fix: %+v", v.Copy)
	}
}

// A voice recording: "Even out loudness" settles it on the speech target in
// two passes; "Clean up voices" runs the Sound tab's engine (a stand-in
// here), and without one says so plainly instead of trying forever.
func TestVoiceLoudnessAndCleaning(t *testing.T) {
	f := newFlow(t)
	voice := f.ffmpeg("porch-voice.wav", "-f", "lavfi", "-i", "aevalsrc='0.02*sin(2*PI*230*t)*(0.6+0.4*sin(2*PI*2*t))':s=48000:d=6", "-c:a", "pcm_s16le")
	as := f.ingest(voice)
	v := f.tick(fixes.SetIn{ID: as.ID, Kind: models.SourceVoice, Tick: []string{models.FixLoudness}})
	if v.State != models.FixPreparing || len(v.JobIDs) != 1 || v.Summary != "Making the fixed copy: evening out the loudness." {
		t.Fatalf("ticked: %+v", v)
	}
	f.prepare(v.JobIDs[0])
	as = f.asset(as.ID)
	c := f.copyOf(as)
	if st := stepOf(c, models.FixLoudness); st == nil || st.Engine != "loudnorm" || st.Params["target"] != "-16" || st.Params["measured"] == "" || c.MIME != "audio/flac" {
		t.Fatalf("loudness: %+v (mime %s)", st, c.MIME)
	}

	// Cleaning voices with the Sound tab's engine (a stand-in that copies).
	prev := fixes.CleanVoices
	t.Cleanup(func() { fixes.CleanVoices = prev })
	fixes.CleanVoices = func(_ context.Context, _ *app.App, in, out string, progress func(string)) (string, error) {
		progress("cleaning")
		b, err := os.ReadFile(in)
		if err != nil {
			return "", err
		}
		return "stand-in clean", os.WriteFile(out, b, 0o644)
	}
	v = f.tick(fixes.SetIn{ID: as.ID, Only: &[]string{models.FixVoices}})
	f.prepare(v.JobIDs[0])
	if st := stepOf(f.copyOf(f.asset(as.ID)), models.FixVoices); st == nil || st.Tool != "stand-in clean" {
		t.Fatalf("voices: %+v", st)
	}
	// No engine here: the copy fails at once with a plain reason.
	fixes.CleanVoices = nil
	v = f.tick(fixes.SetIn{ID: as.ID, Only: &[]string{models.FixVoices, models.FixHum}})
	j, err := f.a.Queue.Get(context.Background(), v.JobIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixes.Prepare(context.Background(), f.a, j, func(float64, string) {})
	var fe *fixes.FinalError
	if !errors.As(err, &fe) {
		t.Fatalf("a final failure: %v", err)
	}
	v = f.view(as.ID)
	if v.State != models.FixFailed || v.Error != "Cleaning up voices isn't available on this machine." ||
		v.Summary != "The fixed copy couldn't be made. Cleaning up voices isn't available on this machine." {
		t.Fatalf("failed: %+v", v)
	}
	var cp models.SourceCopy
	if err := f.a.DB.GetByID(context.Background(), store.CSourceCopies, f.a.WorkspaceID(), j.Payload["copyId"].(string), &cp); err != nil || cp.State != models.FixFailed {
		t.Fatalf("the copy is marked failed: %+v %v", cp, err)
	}
	// Ticked again after the failure: the copy is tried again.
	fixes.CleanVoices = func(_ context.Context, _ *app.App, in, out string, _ func(string)) (string, error) {
		b, _ := os.ReadFile(in)
		return "stand-in clean", os.WriteFile(out, b, 0o644)
	}
	v = f.tick(fixes.SetIn{ID: as.ID, Only: &[]string{models.FixVoices, models.FixHum}})
	if v.State != models.FixPreparing || len(v.JobIDs) != 1 {
		t.Fatalf("again: %+v", v)
	}
}

// Music with silence at its ends: "Trim silence at the ends" makes no copy;
// it says where the music plays from, and offers it to a cut that plays the
// silence.
func TestMusicEndsAreTrimmedWithoutACopy(t *testing.T) {
	f := newFlow(t)
	ctx := context.Background()
	song := f.ffmpeg("theme.wav", "-f", "lavfi", "-i", "aevalsrc='if(between(t,1.5,5.5),0.3*sin(2*PI*440*t),0)':s=48000:d=7", "-c:a", "pcm_s16le")
	as := f.ingest(song)
	// A cut that plays the whole track under the picture.
	now := time.Now().UTC()
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: f.a.WorkspaceID(), ProjectID: f.project, Branch: "main", Number: 1, CreatedAt: now,
		Spec: editspec.Spec{SchemaVersion: 1, Audio: []editspec.AudioTrack{{ID: "theme", AssetID: as.ID, Role: "music", In: 0, Out: 7}}}}
	if err := f.a.DB.Insert(ctx, store.CRevisions, rev); err != nil {
		t.Fatal(err)
	}
	if err := f.a.DB.Update(ctx, store.CProjects, f.a.WorkspaceID(), f.project, bson.M{"$set": bson.M{"branches.main": models.BranchHead{Head: rev.ID}}}); err != nil {
		t.Fatal(err)
	}
	v := f.tick(fixes.SetIn{ID: as.ID, Kind: models.SourceMusic})
	f.measure(f.asset(as.ID))
	v = f.view(as.ID)
	if ends := f.row(v, models.FixEnds); !ends.Fits || ends.Why != "There's silence at the start and the end" {
		t.Fatalf("ends: %+v", ends)
	}
	v = f.tick(fixes.SetIn{ID: as.ID, Tick: []string{models.FixEnds}})
	if len(v.JobIDs) != 0 || v.State != "" || v.Trim == nil || v.Trim.Start < 1.3 || v.Trim.Start > 1.7 || v.Trim.End < 5.3 || v.Trim.End > 5.7 {
		t.Fatalf("no copy, a trim: %+v (trim %+v)", v, v.Trim)
	}
	if !strings.HasPrefix(v.TrimWords, "Plays from its first sound (1.") || len(v.Cuts) != 1 || !strings.HasPrefix(v.Cuts[0].Offer, "Use the trimmed music in “") {
		t.Fatalf("words %q cuts %+v", v.TrimWords, v.Cuts)
	}
	if as := f.asset(as.ID); as.Fixed() {
		t.Fatal("trimming the ends never makes a copy")
	}
	// Unticked: the trim goes.
	if v = f.tick(fixes.SetIn{ID: as.ID, Untick: []string{models.FixEnds}}); v.Trim != nil || v.TrimWords != "" {
		t.Fatalf("unticked: %+v", v)
	}
}
