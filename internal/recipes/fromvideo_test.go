package recipes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/breakdown"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeytdlp"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/visual"
	"github.com/metavert-io/metarush/internal/worker"
)

// refLines are a reference video's words: a question, an intro, three points
// of a list and a call to action (the shape of tests/fixtures/gen/reference-short.mp4).
var refLines = []struct {
	from, to float64
	text     string
	caption  string
}{
	{0.4, 2.7, "Why do people stop watching so fast?", "WHY DO PEOPLE"},
	{3.5, 5.1, "Here are three fixes that work.", "THREE FIXES"},
	{6.8, 8.9, "First, lead with your strongest moment.", "STRONGEST MOMENT"},
	{10.8, 12.7, "Second, cut every slow pause.", "CUT EVERY PAUSE"},
	{14.8, 16.6, "Third, add big captions for silent viewers.", "BIG CAPTIONS"},
	{18.8, 20.6, "Follow for more tips every week.", "FOLLOW FOR TIPS"},
}

const refTitle, refEnd = "STOP SCROLLING", "FOLLOW FOR MORE"

// seedReference adds a video someone else made, imported from a link and
// already looked at — its words, cuts, loudness and what's on screen — as a
// reference clip (no media: nothing here plays or renders it).
func seedReference(t *testing.T, a *app.App, projectID string) *models.Asset {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	id := ids.New(ids.Asset)
	sum := sha256.Sum256([]byte(id))
	as := models.Asset{ID: id, WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{}, Kind: "video", Name: "Fake video refshort001.mp4", Title: "Fake video refshort001",
		SHA256: hex.EncodeToString(sum[:]), Size: 4096, MIME: "video/mp4", StorageKey: "k-" + id, State: models.AssetAvailable, Via: "cli",
		Media:    models.MediaInfo{Container: "mp4", Duration: 26, HasVideo: true, HasAudio: true, Width: 720, Height: 1280, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, Analysis: map[string]string{"evidence": "available"}, CreatedAt: now, UpdatedAt: now, CreatedBy: a.Actor,
		Source: &models.AssetSource{Platform: models.PlatformYouTube, URL: "https://www.youtube.com/watch?v=refshort001", VideoID: "refshort001", Uploader: "Fake Channel",
			Handle: "@fakechannel", Title: "Fake video refshort001", Tool: "yt-dlp", Method: "download", DownloadedAt: now},
		Reference: &models.AssetReference{By: a.Actor, At: now, Why: models.ReferenceStartFrom}}
	if projectID != "" {
		as.ProjectIDs = []string{projectID}
	}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	seedLooks(t, a, id)
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as)
	return &as
}

// seedLooks writes what MetaRush's first look found in a clip: its words, its
// cuts and loudness (the evidence) and what's on screen.
func seedLooks(t *testing.T, a *app.App, id string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	words := 0
	for i, l := range refLines {
		ts := strings.Fields(l.text)
		step := (l.to - l.from) / float64(len(ts))
		var ws []models.Word
		for k, w := range ts {
			f := l.from + step*float64(k)
			ws = append(ws, models.Word{Text: w, From: math.Round(f*1000) / 1000, To: math.Round((f+step-0.05)*1000) / 1000})
		}
		words += len(ws)
		if err := a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: id, Index: i, From: l.from,
			To: l.to, Text: l.text, Speaker: "speaker_0", Words: ws, Provider: "test", Model: "truth", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	st := models.TranscriptStatus{State: "available", Provider: "test", Precision: "word", Language: "en", Segments: len(refLines), Words: words, Speakers: 1, UpdatedAt: now}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), id, bson.M{"$set": bson.M{"transcript": st, "analysis.evidence": "available"}}); err != nil {
		t.Fatal(err)
	}
	insight := func(kind string, data any) {
		b, _ := bson.Marshal(data)
		m := map[string]any{}
		_ = bson.Unmarshal(b, &m)
		if err := a.DB.Insert(ctx, store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: id, Kind: kind, Provider: "test",
			Data: m, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	var cuts []evidence.VisualChange
	for _, c := range []float64{3.2, 6.5, 10.5, 14.5, 18.5, 22} {
		cuts = append(cuts, evidence.VisualChange{At: c, From: c, To: c, Score: 0.9, Kind: evidence.ChangeCut, Frames: []float64{c - 0.1, c + 0.1}})
	}
	insight("technical", evidence.Stored{Version: evidence.Version, Source: "original", Scenes: []evidence.Scene{}, Silences: []evidence.Span{}, Black: []evidence.Span{},
		Freezes: []evidence.Span{}, Notes: []string{}, VisualChanges: cuts})
	series := &evidence.Series{Step: 0.1}
	for k := 0; k < 260; k++ {
		end, v := float64(k+1)*0.1, -40.0
		for _, l := range refLines {
			if end-0.4 >= l.from-0.2 && end <= l.to+0.2 {
				v = -18
			}
		}
		series.LUFS = append(series.LUFS, v)
	}
	insight(evidence.InsightKind, series.SeriesData())
	idx := &visual.Index{Version: "test", Source: "original", Step: 1, Duration: 26, Labels: []visual.LabelSpan{}, Faces: []visual.FaceSample{}, Tracks: []visual.Track{},
		Terms: []string{}, Shots: []visual.ShotSpan{{Type: "close-up", From: 0, To: 6.5, Faces: 1, People: 1}, {Type: "wide", From: 6.5, To: 18.5},
			{Type: "close-up", From: 18.5, To: 22, Faces: 1, People: 1}, {Type: "none", From: 22, To: 26}},
		Text: []visual.TextSpan{{Text: refTitle, From: 0, To: 3.2, Box: [4]float64{0.03, 0.08, 0.93, 0.05}, Conf: 1}, {Text: refEnd, From: 22, To: 26, Box: [4]float64{0.07, 0.47, 0.86, 0.04}, Conf: 1}}}
	for _, l := range refLines {
		idx.Text = append(idx.Text, visual.TextSpan{Text: l.caption, From: l.from, To: l.to, Box: [4]float64{0.2, 0.75, 0.6, 0.04}, Conf: 1})
	}
	insight(visual.InsightKind, visual.ToMap(idx))
}

// refPhrases are the reference's own words: every run of four words it says
// and every line it shows. None may be in a draft made from it.
func refPhrases() []string {
	var out []string
	for _, l := range refLines {
		ws := strings.Fields(strings.ToLower(strings.NewReplacer(",", "", ".", "", "?", "").Replace(l.text)))
		for i := 0; i+4 <= len(ws); i++ {
			out = append(out, strings.Join(ws[i:i+4], " "))
		}
		out = append(out, strings.ToLower(l.caption))
	}
	return append(out, strings.ToLower(refTitle), strings.ToLower(refEnd), "fake video refshort001")
}

func noRefWords(t *testing.T, what, text string) {
	t.Helper()
	low := strings.ToLower(text)
	for _, p := range refPhrases() {
		if strings.Contains(low, p) {
			t.Errorf("%s holds the reference's own words %q", what, p)
		}
	}
}

// Starting from a video someone liked drafts a recipe shaped like it: the
// person's own footage in a chosen section per section of the video, with its
// lengths, its captions, rules and hints — and nothing of the video itself.
// The draft reads back to the same SHA-256 from its sheet, and only a person
// makes it current.
func TestStartFromAVideoDraftsARecipe(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ref := seedReference(t, a, "")
	out := must[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: ref.ID}, false)
	if out.State != FromVideoDrafted || out.Recipe == nil || out.Version == nil || out.Card == nil || out.Existing {
		t.Fatalf("drafted at once (the video was looked at): %+v", out)
	}
	if out.Recipe.Current != 0 || out.Version.State != models.RecipeDraft {
		t.Fatalf("a draft, not current: %+v %+v", out.Recipe, out.Version)
	}
	if out.Recipe.Title != "Like @fakechannel's vertical video" || out.Recipe.Name != "like-fakechannel-s-vertical-video" {
		t.Errorf("named after where it came from, not its words: %q %q", out.Recipe.Title, out.Recipe.Name)
	}
	r, err := loadRecipe(ctx, a, out.Recipe.Name)
	if err != nil {
		t.Fatal(err)
	}
	v, err := loadVersion(ctx, a, r, "draft")
	if err != nil {
		t.Fatal(err)
	}
	d := v.Doc
	// Sections mirror the video's, each chosen from the person's footage, with its length.
	want := []string{"hook", "intro", "part-1", "part-2", "part-3", "call-to-action", "end-card"}
	if len(d.Sections) != len(want) {
		t.Fatalf("sections: %+v", d.Sections)
	}
	for i, s := range d.Sections {
		cs := out.Card.Sections[i]
		if s.Name != want[i] || s.Kind != models.RecipeChosen || s.From != "footage" || len(s.Clips) > 0 || s.Length == nil || cs.Length < s.Length.Min || cs.Length > s.Length.Max {
			t.Errorf("section %d: %+v (the video's: %s %.1f s)", i, s, cs.Name, cs.Length)
		}
	}
	if !strings.HasPrefix(d.Sections[0].Purpose, "A hook of about 3 s: a question to camera") {
		t.Errorf("the hook's purpose describes it: %q", d.Sections[0].Purpose)
	}
	if len(d.Inputs) != 1 || d.Inputs[0].Kind != models.RecipeFootage || !d.Inputs[0].Required || len(d.Inputs[0].Default.Items) > 0 {
		t.Errorf("one input, the person's own footage each run: %+v", d.Inputs)
	}
	if d.Lands.Kind != models.RecipeLandsNewProject || d.Length == nil || d.Length.Min != 22 || d.Length.Max != 30 {
		t.Errorf("a project per run, about as long: %+v %+v", d.Lands, d.Length)
	}
	if o := d.Cut.Output; o.Preset != editspec.PresetVertical || o.Platform != "shorts" || len(d.Cut.Overlays) > 0 || len(d.Cut.Audio) > 0 {
		t.Errorf("vertical for shorts, no graphics or sound of its own: %+v", d.Cut)
	}
	if c := d.Cut.Captions; c == nil || !c.Enabled || c.Style != "bold" || c.Position != "lower" || !c.Uppercase || c.MaxWordsPerLine != 3 {
		t.Errorf("its captions' look: %+v", c)
	}
	rules := map[string]string{}
	for _, x := range d.Rules {
		rules[x.Text] = x.Measure
	}
	if rules["Under 30 s"] != "max-length:30" || rules["The hook is over within 4 s"] != "" || rules["Captions wherever someone speaks"] != "captions" ||
		rules["The voices stay about 22 dB over the music"] != "speech-over-music:22" {
		t.Errorf("rules: %+v", d.Rules)
	}
	hints := strings.Join(d.Hints, "\n")
	for _, w := range []string{"a new shot about every 3.7 s", "Open with a question", "title card", "end screen", "Music under the voices"} {
		if !strings.Contains(hints, w) {
			t.Errorf("hints say %q: %s", w, hints)
		}
	}
	if d.Reference == nil || d.Reference.AssetID != ref.ID || d.Reference.URL != ref.Source.URL || d.Reference.Platform != "youtube" || d.Reference.Who != "@fakechannel" {
		t.Errorf("where it came from: %+v", d.Reference)
	}
	// Nothing of the video's own: no clip of it, no words it says or shows.
	canon, hash := Canonical(&d)
	if hash != v.Hash {
		t.Fatalf("the version is its document's SHA-256")
	}
	plain := strings.ReplaceAll(string(canon), ref.ID, "")
	if strings.Contains(string(canon), ref.ID) && strings.Count(string(canon), ref.ID) != 1 {
		t.Errorf("the reference clip is named only as where it came from")
	}
	noRefWords(t, "the document", plain)
	// The sheet: all but the note naming the reference clip in the Library
	// (a derived comment, never part of the document).
	var shown []string
	for _, l := range strings.Split(out.Sheet, "\n") {
		if strings.HasPrefix(l, "reference: ") {
			l, _, _ = strings.Cut(l, "   # ")
		}
		shown = append(shown, l)
	}
	noRefWords(t, "the sheet", strings.Join(shown, "\n"))
	// The sheet reads back to the same SHA-256.
	sheet, probs := ParseSheet(out.Sheet, SheetParseOptions{Info: ops.SheetInfo(ctx, a, "")})
	for _, p := range probs {
		t.Errorf("sheet line %d: %s", p.Line, p.Message)
	}
	if _, h := Canonical(sheetDoc(sheet)); h != v.Hash {
		t.Errorf("the draft's sheet reads back to another version")
	}
	if chk := must[RecipeCheckOut](t, a, "recipe.check", RecipeCheckIn{Sheet: out.Sheet}, false); !chk.Same || !chk.Check.OK {
		t.Errorf("checking the sheet: the same version, and it checks out: %+v", chk)
	}
	if !strings.Contains(out.Sheet, "reference: clip "+ref.ID+" · link \"https://www.youtube.com/watch?v=refshort001\" · on youtube · by \"@fakechannel\"") {
		t.Errorf("the sheet says where it came from:\n%s", out.Sheet)
	}
	// The clip records the draft; asking again gives the same draft back.
	var after models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ref.ID, &after)
	if sf := after.StartFrom; sf == nil || sf.State != models.StartFromMade || sf.RecipeID != r.ID || sf.Version != 1 {
		t.Errorf("the clip records its draft: %+v", sf)
	}
	again := must[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: ref.ID}, false)
	if !again.Existing || again.Recipe.ID != r.ID {
		t.Errorf("asking again gives the draft back: %+v", again)
	}
	// Only a person makes it current; its check passes without footage of its own.
	if _, err := call[RecipePublishOut](t, a, "recipe.publish", RecipeRefIn{Recipe: r.Name}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("making it current by a program: %v", err)
	}
	pub := must[RecipePublishOut](t, a, "recipe.publish", RecipeRefIn{Recipe: r.Name}, true)
	if pub.Version.State != models.RecipeCurrent {
		t.Fatalf("a person made it current: %+v", pub)
	}
	// A run needs the person's footage, and never takes the reference as it.
	plan, err := call[RecipePlanOut](t, a, "recipe.plan", RecipePlanIn{Recipe: r.Name}, false)
	if err == nil && plan.Ready {
		t.Fatalf("a run without footage: %+v", plan)
	}
	if !strings.Contains(fmt.Sprint(plan, err), "needs clips") {
		t.Errorf("says the run needs the person's footage: %v %+v", err, plan)
	}
	plan, err = call[RecipePlanOut](t, a, "recipe.plan", RecipePlanIn{Recipe: r.Name, Inputs: []RecipeInputValue{{Name: "footage", Items: []string{ref.ID}}}}, false)
	if err == nil && plan.Ready {
		t.Fatalf("a run with the reference as footage: %+v", plan)
	}
	if !strings.Contains(fmt.Sprint(plan, err), "is a reference") {
		t.Errorf("says the reference isn't footage: %v %+v", err, plan)
	}
}

// A reference clip never plays in a cut: the check refuses it and says how to
// mark it as yours; a clip a cut plays can't become one; only a person says a
// video is theirs.
func TestReferenceClipsStayOutOfCuts(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := must[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Shop tips"}, true)
	pid := p.Project.ID
	ref := seedReference(t, a, pid)
	own := seedReference(t, a, pid) // a second clip, made the person's own
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": own.ID}, bson.M{"$unset": bson.M{"reference": "", "source": ""}}); err != nil {
		t.Fatal(err)
	}
	base := ""
	plan := func(asset string) editspec.Plan {
		return editspec.Plan{ProjectID: pid, BaseRevisionID: base, Instruction: "a cut", Spec: &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetVertical, FPS: 30},
			Clips: []editspec.Clip{{ID: "a", AssetID: asset, In: 0.4, Out: 2.7}}}}
	}
	check := func(asset string) *ops.EditCheckOut {
		raw, _ := json.Marshal(plan(asset))
		return must[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: raw}, false)
	}
	chk := check(ref.ID)
	var found *editspec.Issue
	for i, is := range chk.Errors {
		if is.Code == "reference_footage" {
			found = &chk.Errors[i]
		}
	}
	if chk.Valid || found == nil || !strings.Contains(found.Message, "someone else's video") || !strings.Contains(found.Message, "This is mine") ||
		found.Command != "metarush footage reference "+ref.ID+" --mine" {
		t.Fatalf("a reference in a cut is refused, saying how to mark it as yours: %+v", chk.Errors)
	}
	// The person's own clip plays; then it can't be marked a reference.
	raw, _ := json.Marshal(plan(own.ID))
	ok := check(own.ID)
	if !ok.Valid {
		t.Fatalf("own footage: %+v", ok.Errors)
	}
	base = must[ops.EditApplyOut](t, a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: ok.ConfirmToken}, false).Revision.ID
	if _, err := call[ops.FootageReferenceOut](t, a, "footage.reference", ops.FootageReferenceIn{AssetID: own.ID}, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("marking a clip a cut plays: %v", err)
	}
	// Saying it's yours is a person's.
	if _, err := call[ops.FootageReferenceOut](t, a, "footage.reference.mine", ops.FootageReferenceIn{AssetID: ref.ID}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("a program says a video is someone's: %v", err)
	}
	mine := must[ops.FootageReferenceOut](t, a, "footage.reference.mine", ops.FootageReferenceIn{AssetID: ref.ID}, true)
	if mine.Reference || !mine.Changed {
		t.Fatalf("a person said it's theirs: %+v", mine)
	}
	if again := check(ref.ID); !again.Valid {
		t.Errorf("once theirs, a cut plays it: %+v", again.Errors)
	}
	marked := must[ops.FootageReferenceOut](t, a, "footage.reference", ops.FootageReferenceIn{AssetID: ref.ID}, false)
	if !marked.Reference || !marked.Changed {
		t.Errorf("marked a reference again (no cut plays it): %+v", marked)
	}
	// A project's newest footage never hands a run a reference.
	var list []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), sourceFootage(pid), &list); err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.IsReference() {
			t.Errorf("a project's footage for a run includes a reference: %s", x.ID)
		}
	}
}

// The draft waits for MetaRush's look: asked while a look still runs, it is
// made by itself once the look is done — once, as the person who asked.
func TestTheDraftWaitsForTheLook(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ref := seedReference(t, a, "")
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeVisual, IdempotencyKey: "visual:" + ref.ID, AssetID: ref.ID, Payload: map[string]any{"assetId": ref.ID}, Actor: a.Actor})
	if err != nil {
		t.Fatal(err)
	}
	out := must[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: ref.ID, Title: "Shop tips like theirs"}, false)
	if out.State != FromVideoLooking || len(out.Waiting) != 1 || out.Waiting[0] != "what's on screen" || len(out.JobIDs) != 1 || out.Recipe != nil {
		t.Fatalf("waits for the look: %+v", out)
	}
	var as models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ref.ID, &as)
	if as.StartFrom == nil || as.StartFrom.State != models.StartFromWaiting || as.StartFrom.For.UserID != a.Actor.UserID || as.StartFrom.Title != "Shop tips like theirs" {
		t.Fatalf("the clip holds what was asked: %+v", as.StartFrom)
	}
	// The look finishes: the draft is made by itself, once.
	if err := a.DB.Update(ctx, store.CJobs, a.WorkspaceID(), j.ID, bson.M{"$set": bson.M{"state": models.JobSucceeded}}); err != nil {
		t.Fatal(err)
	}
	ops.SourceFactsChanged(ctx, a, ref.ID, ops.FactsAnalysis)
	ops.SourceFactsChanged(ctx, a, ref.ID, ops.FactsAnalysis)
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ref.ID, &as)
	if sf := as.StartFrom; sf == nil || sf.State != models.StartFromMade || sf.Recipe != "shop-tips-like-theirs" {
		t.Fatalf("made by itself: %+v", sf)
	}
	n, _ := a.DB.C(store.CRecipes).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()})
	if n != 1 {
		t.Fatalf("made once: %d recipes", n)
	}
	r, err := loadRecipe(ctx, a, "shop-tips-like-theirs")
	if err != nil || r.Current != "" || r.CreatedBy.UserID != a.Actor.UserID {
		t.Fatalf("a draft, the asker's: %+v %v", r, err)
	}
	// A card says what the video does on its own, too.
	card := must[breakdown.WhatItDoesOut](t, a, "footage.whatItDoes", breakdown.WhatItDoesIn{AssetID: ref.ID}, false)
	if !card.Clip.Reference || card.Clip.StartFrom == nil || len(card.Card.Sections) != 7 || !card.Card.Ready {
		t.Errorf("the clip's card: %+v", card.Clip)
	}
}

// A link to start from comes in through the link import (a stand-in
// downloader: never the internet) as a reference in the project asked for,
// and the draft follows by itself once MetaRush has looked at the video; the
// same link again gives the same draft back. A copy-protected video is
// refused by the import, and nothing is drafted.
func TestStartFromALink(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("the stand-in downloader makes its clips with ffmpeg")
	}
	a := testapp.New(t)
	a.Cfg.Tools.YtDlp = fakeytdlp.Write(t)
	a.Tools = tools.New(a.Cfg.Tools)
	ctx := context.Background()
	p := must[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Shop tips"}, true)
	link := "https://www.youtube.com/watch?v=refvideo001"
	out := must[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: link, ProjectID: p.Project.ID}, false)
	if out.State != FromVideoImporting || out.Import == nil || len(out.JobIDs) != 1 || out.Recipe != nil {
		t.Fatalf("the link is being brought in: %+v", out)
	}
	runJob(t, a, out.JobIDs[0])
	var as models.Asset
	if err := a.DB.C(store.CAssets).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "source.videoId": "refvideo001"}).Decode(&as); err != nil {
		t.Fatal(err)
	}
	if !as.IsReference() || as.Reference.Why != models.ReferenceStartFrom || len(as.ProjectIDs) != 1 || as.ProjectIDs[0] != p.Project.ID {
		t.Fatalf("a reference in the project: %+v %v", as.Reference, as.ProjectIDs)
	}
	if as.StartFrom == nil || as.StartFrom.State != models.StartFromWaiting || as.StartFrom.ProjectID != p.Project.ID || as.StartFrom.For.UserID != a.Actor.UserID {
		t.Fatalf("the draft asked for waits on the clip: %+v", as.StartFrom)
	}
	// MetaRush's looks (the stand-in downloader's clip is 2 s: the test writes what they find).
	_, _ = a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": as.ID}, bson.M{"$set": bson.M{"media.duration": 26, "media.width": 720, "media.height": 1280}})
	seedLooks(t, a, as.ID)
	if _, err := a.DB.C(store.CJobs).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": as.ID, "state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}},
		bson.M{"$set": bson.M{"state": models.JobSucceeded}}); err != nil {
		t.Fatal(err)
	}
	ops.SourceFactsChanged(ctx, a, as.ID, ops.FactsAnalysis)
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &as)
	if as.StartFrom.State != models.StartFromMade {
		t.Fatalf("drafted once looked at: %+v", as.StartFrom)
	}
	r, err := loadRecipe(ctx, a, as.StartFrom.RecipeID)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := loadVersion(ctx, a, r, "draft")
	if v.Doc.Lands.Kind != models.RecipeLandsProject || v.Doc.Lands.ProjectID != p.Project.ID || v.Doc.Reference.URL != link || v.Doc.Reference.AssetID != as.ID {
		t.Errorf("runs make cuts in the project; where it came from: %+v %+v", v.Doc.Lands, v.Doc.Reference)
	}
	again := must[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: "youtu.be/refvideo001"}, false)
	if !again.Existing || again.Recipe.ID != r.ID {
		t.Errorf("the same link again gives the draft back: %+v", again)
	}
	// Copy protection is refused by the import; nothing comes in or is drafted.
	drm := must[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: "https://www.youtube.com/watch?v=drmvideo001"}, false)
	runJob(t, a, drm.JobIDs[0])
	j, _ := a.Queue.Get(ctx, drm.JobIDs[0])
	if j.State != models.JobFailed {
		t.Fatalf("a copy-protected video: %s", j.State)
	}
	if n, _ := a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "source.videoId": "drmvideo001"}); n != 0 {
		t.Errorf("nothing came in")
	}
	if n, _ := a.DB.C(store.CRecipes).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()}); n != 1 {
		t.Errorf("one draft only: %d", n)
	}
}

// runJob runs one queued job here, the way `--wait` does.
func runJob(t *testing.T, a *app.App, id string) {
	t.Helper()
	w := worker.New(a)
	w.OnlyJobID = id
	w.Log = func(string, ...any) {}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatalf("run %s: %v", id, err)
	}
}
