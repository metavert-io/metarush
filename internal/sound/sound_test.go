package sound

// End-to-end tests of clearer voices, levels per person and music through the
// ops, on a throwaway database (scripts/dev-mongo.sh):
//
//   - music fitting, levels and getting quieter when someone talks need only
//     ffmpeg; cloud voice isolation runs against internal/testutil/fakeeleven
//     (never a real key)
//   - clean and studio need the components on this computer: point
//     METARUSH_TEST_SOUND_HOME at a home where `metarush sound setup` ran (or
//     add METARUSH_TEST_SOUND_INSTALL=1 to set them up there); studio also
//     needs METARUSH_TEST_STUDIO=1. Without them those tests skip.
//   - the speech fixtures come from scripts/make-sound-fixtures.sh (macOS say)
//
// The measurements (voice clarity score, background level, words heard) are
// logged with -v.

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/interchange"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/voiceover"
)

type sfx struct {
	t       *testing.T
	a       *app.App
	project string
	dir     string
}

func newSfx(t *testing.T) *sfx {
	t.Helper()
	keyring.MockInit()
	if !tools.New(config.Tools{}).Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	wsID := ids.New(ids.Workspace)
	cfg.Workspace = config.Ref{ID: wsID, Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "studio"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "Ada"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")}}
	cfg.Secrets = "env"
	cfg.Tools.ModelsDir = filepath.Join(os.Getenv("HOME"), ".local", "share", "metarush-models")
	ctx := context.Background()
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	now := time.Now().UTC()
	_ = a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: wsID, WorkspaceID: wsID, Name: "T", CreatedAt: now})
	pid := ids.New(ids.Project)
	_ = a.DB.Insert(ctx, store.CProjects, models.Project{ID: pid, WorkspaceID: wsID, Name: "Garden show", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now})
	return &sfx{t: t, a: a, project: pid, dir: t.TempDir()}
}

func (f *sfx) ffmpeg(args ...string) {
	f.t.Helper()
	if out, err := exec.Command(f.a.Tools.Path(tools.FFmpeg), append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}, args...)...).CombinedOutput(); err != nil {
		f.t.Fatalf("ffmpeg: %v %s", err, out)
	}
}

func (f *sfx) ingest(path string, tags ...string) *models.Asset {
	f.t.Helper()
	ctx := context.Background()
	r := ingest.File(ctx, f.a, path, ingest.Options{ProjectID: f.project, Via: "cli", Tags: tags})
	if r.AssetID == "" {
		f.t.Fatalf("ingest %s: %+v", path, r)
	}
	return f.asset(r.AssetID)
}

func (f *sfx) asset(id string) *models.Asset {
	f.t.Helper()
	var as models.Asset
	if err := f.a.DB.GetByID(context.Background(), store.CAssets, f.a.WorkspaceID(), id, &as); err != nil {
		f.t.Fatal(err)
	}
	return &as
}

type turn struct {
	Speaker string  `json:"voice"`
	Start   float64 `json:"start_s"`
	End     float64 `json:"end_s"`
	Text    string  `json:"text"`
}

// transcript gives an asset a transcript with named speakers (what people do
// once diarization ran and they named the voices).
func (f *sfx) transcript(assetID string, turns []turn) {
	f.t.Helper()
	ctx := context.Background()
	labels := map[string]string{}
	for i, tr := range turns {
		if labels[tr.Speaker] == "" {
			labels[tr.Speaker] = "speaker_0" + string(rune('0'+len(labels)))
		}
		seg := models.TranscriptSegment{ID: ids.New("seg"), WorkspaceID: f.a.WorkspaceID(), AssetID: assetID, Index: i, From: tr.Start, To: tr.End, Text: tr.Text,
			Speaker: labels[tr.Speaker], SpeakerName: tr.Speaker, Provider: "test", Model: "truth", CreatedAt: time.Now().UTC()}
		if err := f.a.DB.Insert(ctx, store.CSegments, seg); err != nil {
			f.t.Fatal(err)
		}
	}
	st := models.TranscriptStatus{State: "available", Provider: "test", Precision: "segment", Language: "en", Segments: len(turns), Speakers: len(labels), UpdatedAt: time.Now().UTC()}
	if err := f.a.DB.Update(ctx, store.CAssets, f.a.WorkspaceID(), assetID, map[string]any{"$set": map[string]any{"transcript": st}}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *sfx) apply(plan editspec.Plan) string {
	f.t.Helper()
	ctx := context.Background()
	raw, _ := json.Marshal(plan)
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, f.a, "edit.check", ops.EditCheckIn{Plan: raw}, ops.Caller{})
	if err != nil {
		f.t.Fatal(err)
	}
	if !chk.Valid {
		f.t.Fatalf("plan invalid: %+v", chk.Errors)
	}
	out, err := ops.CallTyped[ops.EditApplyOut](ctx, f.a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: chk.ConfirmToken}, ops.Caller{})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.Revision.ID
}

func (f *sfx) cut(clips ...editspec.Clip) string {
	f.t.Helper()
	spec := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30}, Clips: clips}
	return f.apply(editspec.Plan{ProjectID: f.project, Instruction: "test cut", Spec: spec})
}

func (f *sfx) plan(rev string) *editspec.RenderPlan {
	f.t.Helper()
	out, err := scall[editspec.RenderPlan](f.t, f.a, "revision.plan", ops.RevisionPlanIn{ID: rev})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *sfx) spec(rev string) *editspec.Spec {
	var r models.Revision
	if err := f.a.DB.GetByID(context.Background(), store.CRevisions, f.a.WorkspaceID(), rev, &r); err != nil {
		f.t.Fatal(err)
	}
	return &r.Spec
}

func scall[O any](t *testing.T, a *app.App, name string, in any) (*O, error) {
	t.Helper()
	return ops.CallTyped[O](context.Background(), a, name, in, ops.Caller{Human: true})
}

// applied returns the version an op with apply made.
func applied(t *testing.T, ap *voiceover.Applied) string {
	t.Helper()
	if ap == nil || ap.Problem != "" || ap.RevisionID == "" {
		t.Fatalf("not applied: %+v", ap)
	}
	return ap.RevisionID
}

// decode reads [from, to) of an asset's sound as 48 kHz mono.
func (f *sfx) decode(as *models.Asset, from, to float64) []float32 {
	f.t.Helper()
	ctx := context.Background()
	p, err := f.a.Cache.Ensure(ctx, f.a.Media, as.StorageKey, as.SHA256, as.Size, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	e := &Engine{Tools: f.a.Tools, Scratch: filepath.Join(f.dir, "decode")}
	au, err := e.Extract(ctx, p, from, to, WetRate, 1)
	if err != nil {
		f.t.Fatal(err)
	}
	return au.Data
}

func repoRoot(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		d = filepath.Dir(d)
	}
	t.Fatal("repository root not found")
	return ""
}

// soundFixture is one of scripts/make-sound-fixtures.sh's files (made when missing and possible).
func (f *sfx) soundFixture(name string) string {
	f.t.Helper()
	root := repoRoot(f.t)
	p := filepath.Join(root, "tests", "fixtures", "gen", "sound", name)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if _, err := exec.LookPath("say"); err != nil {
		f.t.Skip("the speech fixtures need macOS say (scripts/make-sound-fixtures.sh)")
	}
	cmd := exec.Command(filepath.Join(root, "scripts", "make-sound-fixtures.sh"))
	cmd.Env = append(os.Environ(), "FFMPEG="+f.a.Tools.Path(tools.FFmpeg))
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("make-sound-fixtures: %v %s", err, out)
	}
	return p
}

func twoSpeakerTurns(t *testing.T) []turn {
	t.Helper()
	var truth struct {
		Turns []turn `json:"turns"`
	}
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "tests", "fixtures", "truth", "sound-two-speakers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &truth); err != nil {
		t.Fatal(err)
	}
	return truth.Turns
}

// linkComponents gives the test home the components set up in
// METARUSH_TEST_SOUND_HOME (never downloaded per test).
func (f *sfx) linkComponents(studio bool) {
	f.t.Helper()
	src := os.Getenv("METARUSH_TEST_SOUND_HOME")
	if src == "" {
		f.t.Skip("set METARUSH_TEST_SOUND_HOME to a home where clearer voices are set up (METARUSH_TEST_SOUND_INSTALL=1 sets them up there)")
	}
	ctx := context.Background()
	comp := &Components{Home: src, Tools: f.a.Tools}
	none := func(int, int, string) error { return nil }
	install := os.Getenv("METARUSH_TEST_SOUND_INSTALL") == "1"
	if !comp.CleanInstalled() {
		if !install {
			f.t.Skipf("clean is not set up in %s (METARUSH_TEST_SOUND_INSTALL=1 sets it up)", src)
		}
		if err := comp.InstallClean(ctx, none); err != nil {
			f.t.Fatal(err)
		}
	}
	if studio {
		if os.Getenv("METARUSH_TEST_STUDIO") != "1" {
			f.t.Skip("studio quality is tested with METARUSH_TEST_STUDIO=1 (it is slow)")
		}
		if !comp.studioStatus().Installed {
			if !install {
				f.t.Skipf("studio is not set up in %s (METARUSH_TEST_SOUND_INSTALL=1 sets it up)", src)
			}
			start := time.Now()
			if _, err := comp.InstallStudio(ctx, func(d, n int, msg string) error { f.t.Logf("studio setup %d/%d %s", d, n, msg); return nil }); err != nil {
				f.t.Fatal(err)
			}
			f.t.Logf("studio setup took %s", time.Since(start).Round(time.Second))
		}
	}
	if err := os.Symlink(filepath.Join(src, "components"), filepath.Join(f.a.Cfg.Home(), "components")); err != nil {
		f.t.Fatal(err)
	}
}

// gainAt evaluates a Go-resolved envelope at an element frame (AudioLayer's envelopeGain).
func gainAt(env []editspec.GainPoint, fr int) float64 {
	if len(env) == 0 {
		return 1
	}
	if fr <= env[0].F {
		return env[0].G
	}
	for i := 1; i < len(env); i++ {
		if fr <= env[i].F {
			a, b := env[i-1], env[i]
			if b.F == a.F {
				return b.G
			}
			return a.G + (b.G-a.G)*float64(fr-a.F)/float64(b.F-a.F)
		}
	}
	return env[len(env)-1].G
}

func rmsDB(x []float32) float64 {
	var s float64
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return 10 * math.Log10(math.Max(1e-20, s/math.Max(1, float64(len(x)))))
}

// diffDB is the level of the difference between two signals over [from, to) seconds.
func diffDB(a, b []float32, from, to float64) float64 {
	i0, i1 := int(from*WetRate), min(int(to*WetRate), len(a), len(b))
	if i1 <= i0 {
		return -200
	}
	d := make([]float32, i1-i0)
	for i := range d {
		d[i] = a[i0+i] - b[i0+i]
	}
	return rmsDB(d)
}

var wordRe = regexp.MustCompile(`[a-z']+`)

func words(s string) []string { return wordRe.FindAllString(strings.ToLower(s), -1) }

// heard transcribes a file with the local recognizer (nil when none is installed).
func (f *sfx) heard(path string) map[string]int {
	f.t.Helper()
	p := transcribe.Select(config.Transcription{}, f.a.Tools)
	if p == nil {
		return nil
	}
	wav := filepath.Join(f.dir, "asr-"+filepath.Base(path)+".wav")
	f.ffmpeg("-i", path, "-vn", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav)
	tr, err := p.Transcribe(context.Background(), wav, f.dir, "en")
	if err != nil {
		f.t.Fatalf("transcribe: %v", err)
	}
	out := map[string]int{}
	for _, s := range tr.Segments {
		for _, w := range words(s.Text) {
			out[w]++
		}
	}
	return out
}

// missing lists the expected words a transcript did not hear.
func missing(expected []string, got map[string]int) []string {
	left := map[string]int{}
	for k, v := range got {
		left[k] = v
	}
	var miss []string
	for _, w := range expected {
		if left[w] > 0 {
			left[w]--
			continue
		}
		miss = append(miss, w)
	}
	return miss
}

func logScore(t *testing.T, label string, s PersonScore) {
	t.Helper()
	if s.Before.Clarity != nil && s.After.Clarity != nil {
		t.Logf("%s: clarity (DNSMOS P.835 overall/speech/background) %.2f/%.2f/%.2f → %.2f/%.2f/%.2f", label,
			s.Before.Clarity.Overall, s.Before.Clarity.Speech, s.Before.Clarity.Background, s.After.Clarity.Overall, s.After.Clarity.Speech, s.After.Clarity.Background)
	}
	if s.Before.Noise != nil && s.After.Noise != nil {
		t.Logf("%s: voice above the background (SNR) %.0f dB → %.0f dB", label, s.Before.Noise.SNR, s.After.Noise.SNR)
	}
	if s.Likeness != nil {
		t.Logf("%s: still sounds like them %.2f", label, *s.Likeness)
	}
	t.Logf("%s: %q warnings=%q", label, s.Summary, s.Warnings)
}

// TestCleanVoiceOverOnePerson: the clean mode makes one person clearer over
// their speech only, measurably, without losing words; the copy is cached,
// switched off and on (A/B) without work, and plays in the plan.
func TestCleanVoiceOverOnePerson(t *testing.T) {
	f := newSfx(t)
	f.linkComponents(false)
	turns := twoSpeakerTurns(t)
	src := f.ingest(f.soundFixture("noisy-two-speakers.mov"))
	f.transcript(src.ID, turns)
	dur := src.Media.Duration
	rev := f.cut(editspec.Clip{ID: "talk", AssetID: src.ID, In: 0, Out: dur, Label: "the talk"})

	est, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev, Target: "samantha", Mode: "clean", Estimate: true})
	if err != nil {
		t.Fatal(err)
	}
	if !est.Estimate.Allowed || est.Estimate.SpeechSeconds < 9 || est.Estimate.SetsUp != "" || len(est.Copies) != 0 {
		t.Fatalf("estimate: %+v", est.Estimate)
	}
	start := time.Now()
	out, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev, Target: "Samantha", Mode: "clean", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("clean: %.1f s of sound made clearer in %s", est.Estimate.SpeechSeconds, time.Since(start).Round(10*time.Millisecond))
	rev2 := applied(t, out.Applied)
	if len(out.Copies) != 1 || out.Copies[0].Reused || len(out.Scores) != 1 || out.Scores[0].Speaker != "Samantha" {
		t.Fatalf("enhance: %+v", out)
	}
	sc := out.Scores[0]
	logScore(t, "clean, Samantha", sc)
	if sc.Before.Clarity == nil || sc.After.Clarity == nil || sc.After.Clarity.Overall < sc.Before.Clarity.Overall+0.4 || sc.After.Clarity.Background < sc.Before.Clarity.Background+0.8 {
		t.Fatalf("the voice clarity score should rise clearly: %+v → %+v", sc.Before.Clarity, sc.After.Clarity)
	}
	if sc.Before.Noise == nil || sc.After.Noise == nil || sc.After.Noise.SNR < sc.Before.Noise.SNR+6 {
		t.Fatalf("the background should drop: %+v → %+v", sc.Before.Noise, sc.After.Noise)
	}
	if len(sc.Warnings) > 0 {
		t.Fatalf("no warnings at the default strength: %q", sc.Warnings)
	}
	if !strings.Contains(out.Summary, "voice clarity score") || strings.Contains(out.Summary, "DNSMOS") || strings.Contains(out.Summary, "LUFS") {
		t.Fatalf("plain summary: %q", out.Summary)
	}

	// Only her speech changed: Daniel's turns are the recording itself.
	cp := f.asset(out.Copies[0].Variant)
	if !cp.HasTag(models.TagEnhanced) || cp.Media.Duration < dur-0.01 {
		t.Fatalf("copy asset: tags %v, %.2f s", cp.Tags, cp.Media.Duration)
	}
	// It is MetaRush's, not footage: no Library view or search lists it.
	for _, in := range []ops.LibraryIn{{View: "all"}, {View: "projects"}, {View: "inbox"}, {View: "all", Query: "clearer"}} {
		lib, err := scall[ops.LibraryOut](t, f.a, "library.list", in)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range lib.Items {
			if it.ID == cp.ID {
				t.Fatalf("the Library (%+v) lists the clearer copy", in)
			}
		}
		if in.View == "all" && in.Query == "" && (lib.Counts.All != 1 || len(lib.Items) != 1) {
			t.Fatalf("the Library counts %d clips, lists %d: want just the recording", lib.Counts.All, len(lib.Items))
		}
	}
	// A project that uses this one's things gets the recording, not the copy.
	other := ids.New(ids.Project)
	now := time.Now().UTC()
	_ = f.a.DB.Insert(context.Background(), store.CProjects, models.Project{ID: other, WorkspaceID: f.a.WorkspaceID(), Name: "Spring promo", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now})
	ln, err := scall[ops.ProjectLinkOut](t, f.a, "project.link", ops.ProjectLinkIn{ProjectID: other, From: f.project})
	if err != nil {
		t.Fatal(err)
	}
	if ln.Link == nil || ln.Link.Counts.Footage != 1 {
		t.Fatalf("the link brings in %+v: want one clip", ln.Link)
	}
	ps, err := scall[ops.ProjectShowOut](t, f.a, "project.show", ops.ProjectShowIn{ID: other})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.Assets) != 1 || ps.Assets[0].ID != src.ID {
		t.Fatalf("the linked project sees %d clips: want just the recording", len(ps.Assets))
	}
	orig := f.decode(src, out.Copies[0].From, out.Copies[0].To)
	clear := f.decode(cp, 0, 0)
	for _, tr := range turns {
		from, to := tr.Start+0.15-out.Copies[0].From, tr.End-0.15-out.Copies[0].From
		d := diffDB(orig, clear, from, to)
		if tr.Speaker == "Daniel" && d > -70 {
			t.Fatalf("Daniel's turn %.1f–%.1f s changed (difference %.1f dBFS)", tr.Start, tr.End, d)
		}
		if tr.Speaker == "Samantha" && d < -45 {
			t.Fatalf("Samantha's turn %.1f–%.1f s did not change (difference %.1f dBFS)", tr.Start, tr.End, d)
		}
	}

	// The speech check still hears every word (judged against the clean recording).
	if ref := f.heard(f.soundFixture("clean-two-speakers.wav")); ref != nil {
		var expected []string
		for _, tr := range turns {
			expected = append(expected, words(tr.Text)...)
		}
		p, _ := f.a.Cache.Ensure(context.Background(), f.a.Media, cp.StorageKey, cp.SHA256, cp.Size, nil)
		noisy, clearer := f.heard(f.soundFixture("noisy-two-speakers.wav")), f.heard(p)
		missRef := missing(expected, ref)
		var want []string // words the recognizer hears in the clean recording
		skip := map[string]int{}
		for _, w := range missRef {
			skip[w]++
		}
		for _, w := range expected {
			if skip[w] > 0 {
				skip[w]--
				continue
			}
			want = append(want, w)
		}
		t.Logf("speech check: %d words; clean recording misses %v, noisy %v, clearer %v", len(expected), missRef, missing(want, noisy), missing(want, clearer))
		if m := missing(want, clearer); len(m) > 0 {
			t.Fatalf("the clearer voice lost words: %v", m)
		}
	} else {
		t.Log("speech check skipped: no local recognizer installed")
	}

	// The plan plays the copy over the clip's sound, from the right place.
	pl := f.plan(rev2)
	ca := pl.Clips[0].Audio
	if ca == nil || ca.Variant == nil || ca.Variant.AssetID != cp.ID || ca.Variant.TrimBefore != ca.TrimBefore-int(math.Round(out.Copies[0].From*30)) || pl.Sources[cp.ID] == "" {
		t.Fatalf("plan variant: %+v (sources %v)", ca, pl.Sources)
	}
	// QA says what a render of it plays, in plain words.
	notes := clearerVoicesIn(context.Background(), f.a, pl)
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "Clearer voice for Samantha (clean): voice clarity score ") ||
		strings.Contains(notes[0], "DNSMOS") || strings.Contains(notes[0], "SNR") || strings.Contains(notes[0], "LUFS") {
		t.Fatalf("QA note: %q", notes)
	}
	// A timeline for another editing app plays it too, with the copy beside the footage.
	exDir := t.TempDir()
	ex, err := scall[ops.ExportTimelineOut](t, f.a, "export.timeline", ops.ExportTimelineIn{RevisionID: rev2, Format: "fcpxml",
		Out: filepath.Join(exDir, "talk.fcpxml"), Media: filepath.Join(exDir, "media")})
	if err != nil {
		t.Fatal(err)
	}
	var copyFile string
	for _, m := range ex.Media {
		if m.AssetID == cp.ID {
			copyFile = m.Path
		}
	}
	body, _ := os.ReadFile(ex.File)
	fi, err := os.Stat(copyFile)
	if copyFile == "" || err != nil || fi.Size() != cp.Size {
		t.Fatalf("export: the clearer copy should be beside the footage (%q, %v, %d files)", copyFile, err, len(ex.Media))
	}
	if u := interchange.FileURL(copyFile); !strings.Contains(string(body), u) || !strings.Contains(string(body), `srcEnable="video"`) {
		t.Fatalf("export: the timeline should play the clearer copy (%s) with the picture muted:\n%s", u, body)
	}
	show, err := scall[ShowOut](t, f.a, "sound.show", voiceover.RevisionIn{RevisionID: rev2})
	if err != nil {
		t.Fatal(err)
	}
	if len(show.People) != 2 || show.People[0].Name != "Samantha" || show.People[0].Enhance == nil || show.People[0].Enhance.State != "ready" ||
		show.People[0].Enhance.Score == nil || show.People[1].Enhance != nil || len(show.Warnings) != 0 {
		t.Fatalf("show: %+v warnings %+v", show.People, show.Warnings)
	}

	// The same settings again: nothing is made.
	again, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev2, Target: "Samantha", Mode: "clean"})
	if err != nil || len(again.Copies) != 1 || !again.Copies[0].Reused || again.Estimate.SpeechSeconds != 0 || again.Copies[0].Variant != cp.ID {
		t.Fatalf("cache: %+v %v", again, err)
	}
	// A/B: the original plays again, then the clearer one, without new work.
	offRev := func(off bool) string {
		o, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev2, Target: "Samantha", Off: &off, Apply: true})
		if err != nil {
			t.Fatal(err)
		}
		return applied(t, o.Applied)
	}
	rOff := offRev(true)
	if a := f.plan(rOff).Clips[0].Audio; a != nil && a.Variant != nil {
		t.Fatalf("A/B off still plays the copy: %+v", a.Variant)
	}
	if v := f.spec(rOff).Mix.VoiceFor("Samantha"); v == nil || v.Enhance == nil || !v.Enhance.Off || v.Enhance.Mode != "clean" {
		t.Fatalf("A/B off keeps the setting: %+v", v)
	}
	rev2 = rOff
	rOn := offRev(false)
	if a := f.plan(rOn).Clips[0].Audio; a == nil || a.Variant == nil || a.Variant.AssetID != cp.ID {
		t.Fatalf("A/B on plays the same copy again: %+v", a)
	}

	// Her level over her speech only.
	lv, err := scall[LevelsOut](t, f.a, "sound.levels", LevelsIn{RevisionID: rOn, People: []PersonLevel{{Name: "Samantha", GainDB: 6}}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rLv := applied(t, lv.Applied)
	a := f.plan(rLv).Clips[0].Audio
	at := func(sec float64) float64 { return gainAt(a.Gain, int(math.Round(sec*30))-a.From) }
	ratio := 20 * math.Log10(at((turns[0].Start+turns[0].End)/2)/at((turns[1].Start+turns[1].End)/2))
	if math.Abs(ratio-6) > 0.3 {
		t.Fatalf("Samantha +6 dB over her speech only: %.2f dB between her turn and Daniel's", ratio)
	}
	if a.Variant == nil {
		t.Fatal("a level change keeps the clearer voice")
	}
	cmp, err := scall[CompareOut](t, f.a, "sound.compare", CompareIn{RevisionID: rLv})
	if err != nil || len(cmp.Scores) != 1 || cmp.Scores[0].Speaker != "Samantha" || !strings.Contains(cmp.Summary, "clearer") {
		t.Fatalf("compare: %+v %v", cmp, err)
	}
	// Asked on a version that moved on while it worked (two people made clearer
	// at once from the dashboard): it lands on the latest version, keeping both.
	late, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rOn, Target: "Daniel", Mode: "clean", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rLate := applied(t, late.Applied)
	ls := f.spec(rLate)
	if s, d := ls.Mix.VoiceFor("Samantha"), ls.Mix.VoiceFor("Daniel"); s == nil || s.GainDB == nil || *s.GainDB != 6 || s.Enhance == nil || d == nil || d.Enhance == nil {
		t.Fatalf("both changes kept: %+v", ls.Mix.Voices)
	}
	if len(ls.Mix.Enhanced) != 1 || !strings.Contains(ls.Mix.Enhanced[0].Settings, "Daniel: clean") || !strings.Contains(ls.Mix.Enhanced[0].Settings, "Samantha: clean") {
		t.Fatalf("the copy covers both people: %+v", ls.Mix.Enhanced)
	}
	if a := f.plan(rLate).Clips[0].Audio; a == nil || a.Variant == nil || a.Variant.AssetID != ls.Mix.Enhanced[0].Variant {
		t.Fatalf("the latest version plays the copy with both: %+v", a)
	}
}

// TestCleanNarrationTake: a recorded narration take gets a clearer version the
// line plays (the take's own A/B), scored, without losing words.
func TestCleanNarrationTake(t *testing.T) {
	f := newSfx(t)
	f.linkComponents(false)
	path := f.soundFixture("noisy-narration.wav")
	video := filepath.Join(f.dir, "shot.mp4")
	f.ffmpeg("-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-t", "12",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", video)
	shot := f.ingest(video)
	take := f.ingest(path, "voiceover")
	spec := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
		Clips: []editspec.Clip{{ID: "open", AssetID: shot.ID, In: 0, Out: 12}},
		Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{{ID: "line-1", Text: "Pick a sunny spot.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "open", Offset: 0.5},
			Take: &editspec.TakeRef{AssetID: take.ID, In: 0, Out: take.Media.Duration, Source: models.TakeImported}}}}}
	rev := f.apply(editspec.Plan{ProjectID: f.project, Instruction: "narrated", Spec: spec})
	out, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev, Target: "narration", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev2 := applied(t, out.Applied)
	if len(out.Scores) != 1 || out.Mode != "clean" || out.Strength != 1 {
		t.Fatalf("narration: %+v", out)
	}
	logScore(t, "clean, narration", out.Scores[0])
	if c := out.Scores[0]; c.After.Clarity == nil || c.After.Clarity.Overall < c.Before.Clarity.Overall+0.2 || len(c.Warnings) > 0 {
		t.Fatalf("narration clarity: %+v", c)
	}
	b := f.spec(rev2).Voiceover.Beats[0].Take
	if b.Processed == nil || b.Processed.Preset != "clean 100%" || !b.UseProcessed {
		t.Fatalf("the take's clearer version: %+v", b)
	}
	pl := f.plan(rev2)
	if len(pl.Audio) == 0 || pl.Audio[0].AssetID != b.Processed.AssetID {
		t.Fatalf("the line plays its clearer version: %+v", pl.Audio)
	}
	if notes := clearerVoicesIn(context.Background(), f.a, pl); len(notes) != 1 || !strings.HasPrefix(notes[0], "Clearer voice for the narration (clean): voice clarity score ") {
		t.Fatalf("QA note: %q", notes)
	}
	cp := f.asset(b.Processed.AssetID)
	if exp, got := words("Pick a sunny spot, prepare the soil, and plant the peppers deep. Pat the earth down, then water it well."), (map[string]int)(nil); true {
		p, _ := f.a.Cache.Ensure(context.Background(), f.a.Media, cp.StorageKey, cp.SHA256, cp.Size, nil)
		if got = f.heard(p); got != nil {
			t.Logf("speech check (narration): noisy misses %v, clearer misses %v", missing(exp, f.heard(path)), missing(exp, got))
			if m := missing(exp, got); len(m) > 1 {
				t.Fatalf("the clearer narration lost words: %v", m)
			}
		}
	}
	show, err := scall[ShowOut](t, f.a, "sound.show", voiceover.RevisionIn{RevisionID: rev2})
	if err != nil || show.Narration == nil || show.Narration.Enhance == nil || show.Narration.Enhance.Score == nil {
		t.Fatalf("show narration: %+v %v", show.Narration, err)
	}
}

// TestStudioVoice measures studio quality on the two-person recording.
func TestStudioVoice(t *testing.T) {
	f := newSfx(t)
	f.linkComponents(true)
	turns := twoSpeakerTurns(t)
	src := f.ingest(f.soundFixture("noisy-two-speakers.mov"))
	f.transcript(src.ID, turns)
	rev := f.cut(editspec.Clip{ID: "talk", AssetID: src.ID, In: 0, Out: src.Media.Duration})
	comp := &Components{Home: f.a.Cfg.Home(), Tools: f.a.Tools}
	st := comp.studioStatus()
	t.Logf("studio: %s on %s, measured %.2f s of work per second of audio at setup", st.Version, st.Device, st.Speed)
	start := time.Now()
	out, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev, Target: "everyone", Mode: "studio", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	applied(t, out.Applied)
	t.Logf("studio: %.1f s of sound in %s (%.2f s per second)", out.Estimate.SpeechSeconds, took.Round(100*time.Millisecond), took.Seconds()/math.Max(1, out.Estimate.SpeechSeconds))
	if len(out.Scores) == 0 {
		t.Fatalf("studio scores: %+v", out)
	}
	for _, s := range out.Scores {
		logScore(t, "studio, "+s.Speaker, s)
		t.Logf("studio, %s: word check kept %d moment(s): %q", s.Speaker, s.Kept, s.KeptWords)
		if s.After.Clarity == nil || s.After.Clarity.Overall < s.Before.Clarity.Overall+0.3 || s.SafeOnly {
			t.Fatalf("studio clarity for %s: %+v", s.Speaker, s)
		}
	}
	// Studio never changes what people said: every word the recognizer hears
	// in the clean recording is heard in the studio copy.
	cp := f.asset(out.Copies[0].Variant)
	p, _ := f.a.Cache.Ensure(context.Background(), f.a.Media, cp.StorageKey, cp.SHA256, cp.Size, nil)
	ref := f.heard(f.soundFixture("clean-two-speakers.wav"))
	if ref == nil {
		t.Fatal("studio needs the local recognizer for its word check")
	}
	var expected []string
	for _, tr := range turns {
		expected = append(expected, words(tr.Text)...)
	}
	missRef, got := missing(expected, ref), f.heard(p)
	skip := map[string]int{}
	for _, w := range missRef {
		skip[w]++
	}
	var want []string
	for _, w := range expected {
		if skip[w] > 0 {
			skip[w]--
			continue
		}
		want = append(want, w)
	}
	t.Logf("speech check (studio): clean recording misses %v, studio copy misses %v", missRef, missing(want, got))
	if m := missing(want, got); len(m) > 0 {
		t.Fatalf("the studio voice lost words: %v", m)
	}
}

// musicFile makes test music: a kick on every beat at 120 bpm from 0.25 s
// over chords (like the fixture), seconds long.
func (f *sfx) musicFile(name string, seconds float64) string {
	p := filepath.Join(f.dir, name)
	d := strconv.FormatFloat(seconds, 'f', -1, 64)
	f.ffmpeg("-f", "lavfi", "-i", "aevalsrc='0.7*sin(2*PI*55*t)*exp(-mod(t-0.25+10,0.5)*14)*gte(t,0.25)':s=44100:d="+d,
		"-f", "lavfi", "-i", "aevalsrc='0.08*(sin(2*PI*261.6*t)+sin(2*PI*329.6*t)+sin(2*PI*392*t))':s=44100:d="+d,
		"-filter_complex", "[0:a][1:a]amix=inputs=2:normalize=0,afade=t=in:d=0.05,alimiter=limit=0.9:level=disabled", "-ac", "2", "-c:a", "aac", "-b:a", "160k", p)
	return p
}

// musicPieces are a track's elements in a plan, in order (a looped track's
// repeats are id~2, id~3…).
func musicPieces(pl *editspec.RenderPlan, id string) []editspec.PlanAudio {
	var out []editspec.PlanAudio
	for _, a := range pl.Audio {
		if a.ID == id || strings.HasPrefix(a.ID, id+"~") {
			out = append(out, a)
		}
	}
	return out
}

// TestMusicFitLevelAndQuieterWhenTalking: music fits the video by default,
// ending on a beat; a shorter track fades early or loops; manual timing is
// exact; its level and "quieter when someone talks" switch work.
func TestMusicFitLevelAndQuieterWhenTalking(t *testing.T) {
	f := newSfx(t)
	video := filepath.Join(f.dir, "shot.mp4")
	f.ffmpeg("-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30", "-f", "lavfi", "-i", "sine=frequency=330:sample_rate=48000", "-t", "10",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", video)
	shot := f.ingest(video)
	f.transcript(shot.ID, []turn{{Speaker: "Ada", Start: 2, End: 5, Text: "Here is what we planted this spring."}})
	rev := f.cut(editspec.Clip{ID: "open", AssetID: shot.ID, In: 0, Out: 10})
	song := f.ingest(f.musicFile("bed.m4a", 40))
	add, err := scall[MusicOut](t, f.a, "sound.music.add", MusicAddIn{RevisionID: rev, Asset: song.DisplayTitle(), Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev2 := applied(t, add.Applied)
	t.Logf("music: %q tempo %.1f, %d beats; %s", add.Track.ID, add.Tempo, add.Beats, add.Placement)
	if math.Abs(add.Tempo-120) > 2 || add.Beats < 70 || add.Track.Fit != editspec.TrackFitAuto || add.Track.Duck == nil || !*add.Track.Duck || add.Rights == "" {
		t.Fatalf("music add: %+v", add)
	}
	pl := f.plan(rev2)
	ps := musicPieces(pl, add.Track.ID)
	if len(ps) != 1 {
		t.Fatalf("one piece: %+v", ps)
	}
	m := ps[0]
	end := m.From + m.DurationInFrames
	if m.From != 0 || !m.EndsOnBeat || math.Abs(float64(end)-9.75*30) > 2 || m.FadeOutFrames == 0 && len(m.Gain) < 3 {
		t.Fatalf("auto fit: from %d to %d (beat 9.75 s = frame 292.5), on beat %v, %+v", m.From, end, m.EndsOnBeat, m)
	}
	duck := func(a editspec.PlanAudio) float64 {
		return 20 * math.Log10(gainAt(a.Gain, 105-a.From)/gainAt(a.Gain, 225-a.From)) // 3.5 s (talking) vs 7.5 s
	}
	if d := duck(m); d > -8 {
		t.Fatalf("the music gets quieter while Ada talks: %.1f dB", d)
	}
	if lvl := 20 * math.Log10(gainAt(m.Gain, 225)); math.Abs(lvl-(-12+pl.Mix.MasterGainDB)) > 1.5 {
		t.Logf("music level %.1f dB (master %.1f)", lvl, pl.Mix.MasterGainDB)
	}
	show, err := scall[ShowOut](t, f.a, "sound.show", voiceover.RevisionIn{RevisionID: rev2})
	if err != nil || len(show.Music) != 1 || !show.Music[0].EndsOnBeat || show.Music[0].Fit != "auto" || show.Music[0].Duck != "on" {
		t.Fatalf("show music: %+v %v", show.Music, err)
	}

	// Never quieter, and 6 dB louder.
	g := -6.0
	set, err := scall[MusicOut](t, f.a, "sound.music.set", MusicSetIn{RevisionID: rev2, Track: add.Track.ID, Duck: "off", GainDB: &g, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev3 := applied(t, set.Applied)
	m3 := musicPieces(f.plan(rev3), add.Track.ID)[0]
	if d := duck(m3); math.Abs(d) > 0.3 {
		t.Fatalf("duck off: still %.1f dB quieter while Ada talks", d)
	}
	if l := 20 * math.Log10(gainAt(m3.Gain, 225)/gainAt(m.Gain, 225)); math.Abs(l-6) > 0.3 {
		t.Fatalf("level -12 → -6 dB: %.2f dB louder", l)
	}
	// Through the levels op too (the Sound tab saves levels in one go).
	on, depth := "on", -20.0
	lv, err := scall[LevelsOut](t, f.a, "sound.levels", LevelsIn{RevisionID: rev3, Music: []MusicLevel{{Track: add.Track.ID, Duck: on, DuckDB: &depth}}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev4 := applied(t, lv.Applied)
	m4 := musicPieces(f.plan(rev4), add.Track.ID)[0]
	if d := duck(m4); d > -17 {
		t.Fatalf("duck on at 20 dB: %.1f dB", d)
	}

	// A shorter track fades out early; looped, it repeats to the end.
	short := f.ingest(f.musicFile("sting.m4a", 4))
	s1, err := scall[MusicOut](t, f.a, "sound.music.add", MusicAddIn{RevisionID: rev4, Asset: short.ID, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev5 := applied(t, s1.Applied)
	sp := musicPieces(f.plan(rev5), s1.Track.ID)
	if s1.Track.ID != "music-2" || len(sp) != 1 || sp[0].From+sp[0].DurationInFrames > 121 || sp[0].FadeOutFrames == 0 && len(sp[0].Gain) < 3 {
		t.Fatalf("shorter track: %s %+v", s1.Track.ID, sp)
	}
	yes := true
	s2, err := scall[MusicOut](t, f.a, "sound.music.set", MusicSetIn{RevisionID: rev5, Track: s1.Track.ID, Loop: &yes, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev6 := applied(t, s2.Applied)
	lp := musicPieces(f.plan(rev6), s1.Track.ID)
	if len(lp) < 3 || lp[len(lp)-1].From+lp[len(lp)-1].DurationInFrames != 300 {
		t.Fatalf("loop: %d pieces %+v", len(lp), lp)
	}
	t.Logf("loop: %s", s2.Placement)
	// By hand: from 12 s to 20 s of the track, starting 1 s into the video.
	from, to, at := 12.0, 20.0, 1.0
	s3, err := scall[MusicOut](t, f.a, "sound.music.add", MusicAddIn{RevisionID: rev6, Asset: song.ID, From: &from, To: &to, At: &at, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev7 := applied(t, s3.Applied)
	mp := musicPieces(f.plan(rev7), s3.Track.ID)
	if s3.Track.Fit != "" || len(mp) != 1 || mp[0].From != 30 || mp[0].TrimBefore != 360 || mp[0].DurationInFrames != 240 {
		t.Fatalf("manual: %+v %+v", s3.Track, mp)
	}
	// Dragging the looped track on the timeline is a music.set with new times:
	// placed by hand from then on (and played once).
	nat := 2.0
	moved, err := scall[MusicOut](t, f.a, "sound.music.set", MusicSetIn{RevisionID: rev7, Track: s1.Track.ID, At: &nat})
	if err != nil || moved.Track.Fit != "" || moved.Track.Loop || moved.Track.At != 2 || moved.Result == nil || !moved.Result.Check.Valid {
		t.Fatalf("drag: %+v %v", moved, err)
	}
}

// TestCloudVoiceIsPricedFirstAndBudgeted runs cloud isolation against the fake
// ElevenLabs: the estimate costs nothing, the project budget and the plan's
// credits refuse before anything is sent, and a result is never paid twice.
func TestCloudVoiceIsPricedFirstAndBudgeted(t *testing.T) {
	f := newSfx(t)
	key := "sk" + "_" + strings.Repeat("cd34", 10) + "beef"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	rec := filepath.Join(f.dir, "talk.mp4")
	// Two voices taking turns (tone bursts) over a steady hiss.
	f.ffmpeg("-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
		"-f", "lavfi", "-i", "aevalsrc='0.3*sin(2*PI*(220+110*gte(t\\,4))*t)*gt(sin(2*PI*2.5*t)\\,-0.3)*between(mod(t\\,4)\\,0.5\\,3.5)':s=48000:c=mono",
		"-f", "lavfi", "-i", "anoisesrc=color=pink:amplitude=0.03:seed=3:sample_rate=48000",
		"-filter_complex", "[1:a][2:a]amix=inputs=2:normalize=0:duration=first[a]", "-map", "0:v", "-map", "[a]", "-t", "8",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2", rec)
	src := f.ingest(rec)
	f.transcript(src.ID, []turn{{Speaker: "Ada", Start: 0.5, End: 3.5, Text: "One two three."}, {Speaker: "Ben", Start: 4.5, End: 7.5, Text: "Four five six."}})
	rev := f.cut(editspec.Clip{ID: "talk", AssetID: src.ID, In: 0, Out: 8})
	cloud := func(estimate bool) (*EnhanceOut, error) {
		return scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev, Target: "Ben", Mode: "cloud", Estimate: estimate, Apply: !estimate})
	}
	// No key on this computer: said plainly.
	if out, err := cloud(true); err != nil || out.Estimate.Allowed || !strings.Contains(out.Estimate.Problem, "ElevenLabs") {
		t.Fatalf("no key: %+v %v", out, err)
	}
	if _, err := scall[providers.Status](t, f.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}); err != nil {
		t.Fatal(err)
	}
	est, err := cloud(true)
	if err != nil {
		t.Fatal(err)
	}
	// Ben speaks from the middle of the pause (4.0 s) to the end, plus half a second before: 4.5 s.
	if !est.Estimate.Allowed || est.Estimate.Credits != CloudCredits(4.5) || len(fake.Isolations) != 0 || !strings.Contains(est.Summary, "credits") {
		t.Fatalf("estimate: %+v (isolations %d)", est.Estimate, len(fake.Isolations))
	}
	t.Logf("cloud estimate: %s", est.Summary)
	// Over the project's budget: refused before anything is sent.
	lim := est.Estimate.Credits - 1
	if _, err := scall[providers.SettingsOut](t, f.a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, Scope: "project", ProjectID: f.project, BudgetCredits: &lim}); err != nil {
		t.Fatal(err)
	}
	if out, _ := cloud(true); out == nil || out.Estimate.Allowed || !strings.Contains(out.Estimate.Problem, "budget") {
		t.Fatalf("budget estimate: %+v", out)
	}
	if _, err := cloud(false); !apperr.Is(err, apperr.Budget) || len(fake.Isolations) != 0 {
		t.Fatalf("over budget must refuse without sending: %v (isolations %d)", err, len(fake.Isolations))
	}
	zero := 0.0
	if _, err := scall[providers.SettingsOut](t, f.a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, Scope: "project", ProjectID: f.project, BudgetCredits: &zero}); err != nil {
		t.Fatal(err)
	}
	// The ElevenLabs plan has too few credits left.
	limit := fake.CharacterLimit
	fake.CharacterLimit = fake.CharacterCount + 10
	if _, err := cloud(false); !apperr.Is(err, apperr.Budget) || len(fake.Isolations) != 0 {
		t.Fatalf("plan limit must refuse without sending: %v", err)
	}
	fake.CharacterLimit = limit
	out, err := cloud(false)
	if err != nil {
		t.Fatal(err)
	}
	rev2 := applied(t, out.Applied)
	if len(fake.Isolations) != 1 || float64(fake.Isolations[0].Credits) != est.Estimate.Credits || fake.Isolations[0].Format != "other" {
		t.Fatalf("isolation calls: %+v (estimate %.0f)", fake.Isolations, est.Estimate.Credits)
	}
	bs, _ := providers.ProjectBudget(context.Background(), f.a, f.project)
	if bs.UsedCredits != est.Estimate.Credits {
		t.Fatalf("charged %.0f, estimated %.0f", bs.UsedCredits, est.Estimate.Credits)
	}
	if len(out.Scores) != 1 || out.Scores[0].Speaker != "Ben" || out.Scores[0].After.Noise == nil {
		t.Fatalf("cloud scores: %+v", out.Scores)
	}
	logScore(t, "cloud (fake), Ben", out.Scores[0])
	if pl := f.plan(rev2); pl.Clips[0].Audio == nil || pl.Clips[0].Audio.Variant == nil {
		t.Fatalf("cloud copy in the plan: %+v", pl.Clips[0].Audio)
	}
	// Made before: never sent or paid again, also from the new version.
	again, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev2, Target: "Ben", Mode: "cloud"})
	if err != nil || !again.Copies[0].Reused || again.Estimate.Credits != 0 || len(fake.Isolations) != 1 {
		t.Fatalf("cached cloud: %+v %v (isolations %d)", again, err, len(fake.Isolations))
	}
	if bs, _ := providers.ProjectBudget(context.Background(), f.a, f.project); bs.UsedCredits != est.Estimate.Credits {
		t.Fatalf("charged again: %.0f", bs.UsedCredits)
	}
	// Studio is not set up here: the estimate says so in plain words and the
	// work is refused with the setup hint.
	if out, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev2, Target: "Ada", Mode: "studio", Estimate: true}); err != nil || out.Estimate.Allowed || !strings.Contains(out.Estimate.Problem, "Studio quality is not set up") {
		t.Fatalf("studio not set up: %+v %v", out, err)
	}
	// The hint says where in plain words; the exact command is kept for Details.
	if _, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev2, Target: "Ada", Mode: "studio"}); !apperr.Is(err, apperr.NotConfigured) ||
		!strings.Contains(apperr.From(err).Hint, "Set up on the Sound tab") || apperr.From(err).Command() != "metarush sound setup studio" {
		t.Fatalf("studio refusal: %v (hint %q)", err, apperr.From(err).Hint)
	}
	// Deleting the footage for good deletes the voices made clearer from it
	// (asset.delete runs this hook): the copy and the processed voices.
	ctx := context.Background()
	copyID := out.Copies[0].Variant
	if n, err := deleteClearer(ctx, f.a, src.ID); err != nil || n < 2 {
		t.Fatalf("delete hook: %d %v", n, err)
	}
	var gone models.Asset
	if f.a.DB.GetByID(ctx, store.CAssets, f.a.WorkspaceID(), copyID, &gone) == nil {
		t.Fatal("the clearer copy is still there")
	}
	if n, _ := f.a.DB.C(Collection).CountDocuments(ctx, map[string]any{"workspaceId": f.a.WorkspaceID(), "sourceAssetId": src.ID}); n != 0 {
		t.Fatalf("%d records of the deleted footage are left", n)
	}
}
