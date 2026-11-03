package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/breakdown"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Start from a video you like (D400, D401): a link (or a clip already here)
// becomes a recipe draft shaped like that video. The link comes in through the
// link import as a reference (someone else's video, kept to learn from: no cut
// plays it, D398); once MetaRush has looked at it — transcript and voices,
// cuts, pauses and loudness, what's on screen where a Mac can look — its card
// (internal/breakdown, D399) is drafted into a recipe sheet and saved through
// recipe.write as a draft, as the person who asked: a required footage input
// the person fills each run, chosen sections mirroring the video's sections
// with their lengths, its captions as the cut's captions, rules (its length,
// how short the hook is, captions, the voices over the music) and hints (the
// pacing, the layout pack's title card and end screen, music under the
// voices). The draft records the reference clip and its link (provenance) and
// never holds the reference's footage, words or pictures: every section is
// chosen from the person's own footage and says in MetaRush's words what that
// part does. Only a person makes it current, as always.

// RecipeFromVideoIn starts a recipe from a video.
type RecipeFromVideoIn struct {
	From string `json:"from" jsonschema:"minLength=1,maxLength=2000,description=A link to the video (YouTube\\, TikTok\\, X or another video site) or a clip already in MetaRush (its ID or name)."`
	// ProjectID: runs make new cuts there (and a linked video comes in there);
	// without one each run makes a project of its own.
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Runs make new cuts in this project (a video from a link comes in there too); without one each run makes a project of its own and the video goes to the Inbox."`
	Name      string `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$,description=The recipe's short name (default: one made from its title)."`
	Title     string `json:"title,omitempty" jsonschema:"maxLength=120,description=The recipe's title (default: Like <who>'s video)."`
	// Reference marks a clip already here as a reference too: a video someone
	// else made that a person brought in as a file. A link always comes in as one.
	Reference bool `json:"reference,omitempty" jsonschema:"description=For a clip already in MetaRush: mark it as a reference too (someone else's video\\, kept to learn from\\, that no cut plays). A video from a link always comes in as one."`
	// BrowserSignIn: download the link with this browser's sign-in (owners;
	// the link import's own choice, D66).
	BrowserSignIn string `json:"browserSignIn,omitempty" jsonschema:"enum=chrome,enum=safari,enum=firefox,enum=brave,enum=edge,description=Download the link with this browser's sign-in (for a bot check or sign-in wall)."`
}

// Where starting from a video stands (RecipeFromVideoOut.State).
const (
	FromVideoImporting = "importing" // the video is being brought in
	FromVideoLooking   = "looking"   // MetaRush is looking at it; the draft follows by itself
	FromVideoDrafted   = "drafted"   // the draft is saved
)

// RecipeFromVideoOut says where it stands: the video coming in, MetaRush
// looking at it (the draft is made by itself once it has), or the draft.
type RecipeFromVideoOut struct {
	State string `json:"state" jsonschema:"enum=importing,enum=looking,enum=drafted,description=importing: the video is being brought in; looking: MetaRush is looking at it and the draft follows by itself; drafted: the draft is saved."`
	// Clip is the video (a reference unless it was someone's own already).
	Clip *breakdown.WhatClip `json:"clip,omitempty"`
	// Card is what the video does (so far, while MetaRush still looks).
	Card *breakdown.WhatItDoes `json:"card,omitempty"`
	// Recipe and Version are the draft; Sheet its recipe sheet.
	Recipe  *RecipeRow        `json:"recipe,omitempty"`
	Version *RecipeVersionRow `json:"version,omitempty"`
	Sheet   string            `json:"sheet,omitempty"`
	// Existing: the draft was made earlier (asking again gives it back).
	Existing bool `json:"existing,omitempty"`
	// Waiting are the looks still running, in plain words; JobIDs the work
	// to follow (the import, then the looks).
	Waiting []string              `json:"waiting"`
	JobIDs  []string              `json:"jobIds"`
	Import  *linkimport.ImportOut `json:"import,omitempty"`
	Message string                `json:"message"`
	Next    string                `json:"next,omitempty"`
}

func init() {
	ops.Def("recipe.fromVideo", "Start a recipe from a video you like: a link (YouTube, TikTok, X) or a clip comes in as a reference (someone else's video, kept to learn from, never played in a cut); once MetaRush has looked at it, what it does becomes a recipe draft — your footage in sections shaped like its sections, with their lengths, captions, rules and hints — saved as a draft only a person makes current", ops.Flags{Mutating: true}, fromVideo)
	ops.OnSourceFacts(func(ctx context.Context, a *app.App, assetID, why string) {
		if why == ops.FactsAnalysis || why == ops.FactsAsked || why == ops.FactsIngested {
			DraftWhenLooked(ctx, a, assetID)
		}
	})
	// The menu and find (D269): starting from a video someone liked.
	ops.Offer("recipe.fromVideo", ops.Offering{ID: "start-from-a-video", Group: "make", Title: "Start from a video you like",
		Does:  "Brings a video you like in as a reference and drafts a recipe shaped like it, for your own footage.",
		Words: []string{"make a video like this one", "a video like this", "copy how a video is made", "reference video"},
		Needs: ops.Needs{Works: []string{"transcripts", "editing"}}})
}

func fromVideo(ctx context.Context, a *app.App, in *RecipeFromVideoIn) (*RecipeFromVideoOut, error) {
	from := strings.TrimSpace(in.From)
	if from == "" {
		return nil, apperr.New(apperr.InvalidInput, "Give a link to the video, or name a clip in MetaRush.")
	}
	if in.Name != "" && !namePattern.MatchString(in.Name) {
		return nil, apperr.New(apperr.InvalidInput, "A recipe's short name is small letters, digits and dashes (like app-teaser), not “%s”.", in.Name)
	}
	sf := models.StartFromVideo{For: a.Actor, Name: in.Name, Title: strings.TrimSpace(in.Title)}
	if in.ProjectID != "" {
		p, err := requireProject(ctx, a, in.ProjectID, "the recipe's runs go to")
		if err != nil {
			return nil, err
		}
		sf.ProjectID = p.ID
	}
	if looksLikeLink(from) {
		return fromLink(ctx, a, from, sf, in)
	}
	as, err := ops.ResolveFootage(ctx, a, from, "")
	if err != nil {
		return nil, err
	}
	return fromClip(ctx, a, as, sf, in.Reference)
}

// looksLikeLink tells a link from a clip's name.
func looksLikeLink(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "www.") ||
		(!strings.Contains(l, " ") && strings.Contains(l, ".") && strings.Contains(l, "/"))
}

// fromLink brings the video in as a reference, or takes the clip it already is.
func fromLink(ctx context.Context, a *app.App, link string, sf models.StartFromVideo, in *RecipeFromVideoIn) (*RecipeFromVideoOut, error) {
	if !strings.Contains(link, "://") {
		link = "https://" + link
	}
	l, err := linkimport.Classify(link)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush can't bring that link in: %s.", strings.TrimSuffix(err.Error(), ".")).
			WithHint("Use the link to one video on YouTube, TikTok, X or another video site.")
	}
	if l.Many() {
		return nil, apperr.New(apperr.InvalidInput, "That link is a playlist or a channel: start from one video of it.").
			WithHint("Open the video you like and use its own link.")
	}
	if as := clipFromLink(ctx, a, l); as != nil {
		return fromClip(ctx, a, as, sf, false)
	}
	imp, err := linkimport.StartFrom(ctx, a, linkimport.ImportIn{URLs: []string{link}, ProjectID: sf.ProjectID, BrowserSignIn: in.BrowserSignIn}, sf)
	if err != nil {
		return nil, err
	}
	if len(imp.Imports) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush can't bring that link in.")
	}
	row := imp.Imports[0]
	switch row.Status {
	case "refused":
		return nil, apperr.New(apperr.InvalidInput, "%s", row.Message).WithHint(row.Next).WithDetails(map[string]any{"code": row.Code})
	case "already":
		var as models.Asset
		if row.AssetID != "" && a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), row.AssetID, &as) == nil {
			return fromClip(ctx, a, &as, sf, false)
		}
	}
	out := &RecipeFromVideoOut{State: FromVideoImporting, Import: imp, JobIDs: nonNilStrings(imp.JobIDs), Waiting: []string{"bringing it in"},
		Message: "Bringing the video in as a reference: someone else's video, kept to learn from, that no cut plays. Once MetaRush has looked at it, the draft is made by itself; it shows on the Recipes tab.",
		Next:    "Follow it on the activity bar; asking again with the same link says where it stands."}
	if imp.Note != "" {
		out.Message += " " + imp.Note
	}
	return out, nil
}

// clipFromLink is the clip already imported from a link's video, if any.
func clipFromLink(ctx context.Context, a *app.App, l linkimport.Link) *models.Asset {
	if l.Platform == "" || l.VideoID == "" {
		return nil
	}
	var as models.Asset
	if err := a.DB.C(store.CAssets).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "source.platform": l.Platform, "source.videoId": l.VideoID,
		"state": models.AssetAvailable}).Decode(&as); err != nil {
		return nil
	}
	return &as
}

// fromClip drafts from a clip now when MetaRush has looked at it, else asks
// for the draft on the clip and says what it waits for.
func fromClip(ctx context.Context, a *app.App, as *models.Asset, sf models.StartFromVideo, markRef bool) (*RecipeFromVideoOut, error) {
	switch {
	case as.Kind == "image":
		return nil, apperr.New(apperr.InvalidInput, "“%s” is a picture: start from a video.", as.DisplayTitle())
	case as.Trash != nil:
		return nil, apperr.New(apperr.Conflict, "“%s” is in the Trash: restore it first.", as.DisplayTitle())
	case !as.Media.HasVideo && !as.Media.HasAudio:
		return nil, apperr.New(apperr.InvalidInput, "“%s” has neither picture nor sound to learn from.", as.DisplayTitle())
	}
	if markRef && !as.IsReference() {
		if err := ops.MarkReference(ctx, a, as, models.ReferenceStartFrom); err != nil {
			return nil, err
		}
	}
	// A draft made from this video earlier is given back (another name asks for a new one).
	if was := as.StartFrom; was != nil && was.State == models.StartFromMade && (sf.Name == "" || sf.Name == was.Recipe) {
		if r, err := loadRecipe(ctx, a, was.RecipeID); err == nil {
			if v, err := loadVersion(ctx, a, r, fmt.Sprint(was.Version)); err == nil {
				out := &RecipeFromVideoOut{State: FromVideoDrafted, Existing: true, Waiting: []string{}, JobIDs: []string{}}
				row, vrow := recipeRow(ctx, a, r, v), versionRow(v, nil)
				out.Recipe, out.Version, out.Sheet = &row, &vrow, printVersion(ctx, a, r, v)
				if card, err := breakdown.CardOf(ctx, a, as); err == nil {
					out.Clip, out.Card = &card.Clip, &card.Card
				}
				out.Message = fmt.Sprintf("“%s” was drafted from this video already (version %d, %s).", r.Title, v.Number, stateWord(v.State))
				out.Next = "Change it on the recipe's page, or ask Claude to; a person makes it current before it runs."
				return out, nil
			}
		}
	}
	waiting, jobIDs := lookFor(ctx, a, as)
	if as.State != models.AssetAvailable || len(waiting) > 0 {
		if as.State != models.AssetAvailable {
			waiting = append([]string{"bringing it in"}, waiting...)
		}
		asked, err := ops.AskStartFrom(ctx, a, as.ID, sf)
		if err != nil {
			return nil, err
		}
		out := &RecipeFromVideoOut{State: FromVideoLooking, Waiting: waiting, JobIDs: jobIDs}
		if asked != nil && asked.State == models.StartFromMade {
			return fromClip(ctx, a, as, sf, false) // it was drafted meanwhile
		}
		if card, err := breakdown.CardOf(ctx, a, as); err == nil {
			out.Clip, out.Card = &card.Clip, &card.Card
		}
		out.Message = fmt.Sprintf("MetaRush is still looking at “%s” (%s). The draft is made by itself once it has; it shows on the Recipes tab.", as.DisplayTitle(), joinAnd(waiting))
		out.Next = "Follow it on the activity bar; asking again says where it stands."
		return out, nil
	}
	return draftNow(ctx, a, as, sf)
}

// lookFor is what MetaRush still has to look at in a clip before it drafts
// from it (in plain words) and the work to follow; it asks for a look that
// never ran (an older clip without its evidence or words).
func lookFor(ctx context.Context, a *app.App, as *models.Asset) ([]string, []string) {
	waiting := breakdown.Pending(ctx, a, as.ID)
	if len(waiting) == 0 && as.State == models.AssetAvailable {
		needWords := as.Media.HasAudio && (as.Transcript == nil || as.Transcript.State == "" || as.Transcript.State == "pending")
		needEvidence := as.Analysis[jobsEvidence] != "available"
		if needWords || needEvidence {
			if _, err := ops.CallTyped[ops.AnalyzeOut](ctx, a, "footage.analyze", ops.AnalyzeIn{AssetID: as.ID, Transcribe: &needWords}, ops.Caller{}); err == nil {
				waiting = breakdown.Pending(ctx, a, as.ID)
			}
		}
	}
	var js []models.Job
	_ = a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"assetId": as.ID, "type": bson.M{"$in": breakdown.LookTypes()},
		"state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}}, &js)
	ids := []string{}
	for _, j := range js {
		ids = append(ids, j.ID)
	}
	return waiting, ids
}

const jobsEvidence = "evidence"

// draftNow makes the draft from a clip MetaRush has looked at, through
// recipe.write, as the app's person, and records it on the clip.
func draftNow(ctx context.Context, a *app.App, as *models.Asset, sf models.StartFromVideo) (*RecipeFromVideoOut, error) {
	card, err := breakdown.CardOf(ctx, a, as)
	if err != nil {
		return nil, err
	}
	doc := DraftFromCard(&card.Card, card.Clip, sf)
	name := sf.Name
	if name == "" {
		name = shortName(doc.Title)
	}
	if name, err = freeName(ctx, a, name); err != nil {
		return nil, err
	}
	sc := newSheetContext(ctx, a)
	sheet := PrintSheet(doc, SheetOptions{Name: name, Info: ops.SheetInfo(ctx, a, homeProject(doc)), Title: sc.title})
	raw, _ := json.Marshal(RecipeWriteIn{Sheet: sheet, Note: "Drafted from what a video someone liked does: its sections and pacing, not its footage or words."})
	res, err := ops.Call(ctx, a, "recipe.write", raw, ops.CallerFrom(ctx))
	if err != nil {
		return nil, err
	}
	w := res.(*RecipeWriteOut)
	now := time.Now().UTC()
	sf.State, sf.RecipeID, sf.Recipe, sf.Version, sf.MadeAt, sf.MakingAt, sf.Problem = models.StartFromMade, w.Recipe.ID, w.Recipe.Name, w.Version.Number, &now, nil, ""
	if sf.At.IsZero() {
		sf.At = now
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"startFrom": sf, "updatedAt": now}})
	a.Audit(ctx, "recipe.fromVideo", "recipe", w.Recipe.ID, sf.ProjectID, map[string]any{"assetId": as.ID, "version": w.Version.Number, "reference": as.IsReference()})
	out := &RecipeFromVideoOut{State: FromVideoDrafted, Clip: &card.Clip, Card: &card.Card, Recipe: &w.Recipe, Version: &w.Version, Sheet: w.Sheet, Waiting: []string{}, JobIDs: []string{}}
	out.Clip.StartFrom = &sf
	out.Message = fmt.Sprintf("Drafted “%s” from what “%s” does: %s. Your own footage goes into it each run; a person makes it current before it runs.",
		w.Recipe.Title, as.DisplayTitle(), sectionsWords(doc))
	if w.Check != nil && !w.Check.OK && len(w.Check.Problems) > 0 {
		out.Message += " Its check found something to fix first: " + w.Check.Problems[0].Message
	}
	out.Next = "Look at it on the Recipes tab and change it there (or ask Claude to refine its sheet); then make it current."
	return out, nil
}

// sectionsWords says a draft's sections in a few words ("a hook, an intro, 3 parts, a call to action and an end card").
func sectionsWords(d *models.RecipeDoc) string {
	var parts []string
	body := 0
	for _, s := range d.Sections {
		if strings.HasPrefix(s.Name, "part-") || s.Name == "body" {
			body++
		}
	}
	for _, s := range d.Sections {
		switch {
		case s.Name == "hook":
			parts = append(parts, "a hook")
		case s.Name == "intro":
			parts = append(parts, "an intro")
		case s.Name == "body" || s.Name == "part-1":
			if body > 1 {
				parts = append(parts, plural(body, "part"))
			} else {
				parts = append(parts, "a body")
			}
		case s.Name == "call-to-action":
			parts = append(parts, "a call to action")
		case s.Name == "end-card":
			parts = append(parts, "an end card")
		case strings.HasPrefix(s.Name, "part-"):
		default:
			parts = append(parts, "“"+s.Name+"”")
		}
	}
	return joinAnd(parts)
}

// DraftWhenLooked makes the draft asked for from a clip once MetaRush has
// looked at it (D401): from the hook on the clip's analyses, on whichever
// computer finished the last one, as the person who asked — claimed waiting →
// making by check-and-set, so it is made once (a claim nobody finished in ten
// minutes is taken again).
func DraftWhenLooked(ctx context.Context, a *app.App, assetID string) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil || as.StartFrom == nil {
		return
	}
	if st := as.StartFrom.State; st != models.StartFromWaiting && st != models.StartFromMaking {
		return
	}
	if as.State != models.AssetAvailable {
		return
	}
	if waiting, _ := lookFor(ctx, a, &as); len(waiting) > 0 {
		return
	}
	claim := bson.M{"_id": as.ID, "workspaceId": a.WorkspaceID(), "$or": bson.A{bson.M{"startFrom.state": models.StartFromWaiting},
		bson.M{"$and": bson.A{bson.M{"startFrom.state": models.StartFromMaking}, store.StaleBefore("startFrom.makingAt", 10*time.Minute)}}}}
	res, err := a.DB.C(store.CAssets).UpdateOne(ctx, claim, bson.M{"$set": bson.M{"startFrom.state": models.StartFromMaking}, "$currentDate": bson.M{"startFrom.makingAt": true}})
	if err != nil || res.ModifiedCount == 0 {
		return
	}
	sf := *as.StartFrom
	ia := *a
	ia.Actor = sf.For
	if _, err := draftNow(ctx, &ia, &as, sf); err != nil {
		problem := apperr.From(err).Message
		_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"startFrom.state": models.StartFromFailed, "startFrom.problem": problem},
			"$unset": bson.M{"startFrom.makingAt": ""}})
	}
}

func nonNilStrings(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// ---- the draft ----

// DraftFromCard drafts a recipe document from what a video does (D400): a
// required footage input each run fills, a chosen section per section of the
// video with its length, the video's shape, its captions, rules and hints, and
// where it came from. Nothing of the video itself: no footage, no words said
// or shown, no pictures — every section is the person's own footage, and says
// what that part does in MetaRush's words.
func DraftFromCard(c *breakdown.WhatItDoes, clip breakdown.WhatClip, sf models.StartFromVideo) *models.RecipeDoc {
	d := &models.RecipeDoc{Format: models.RecipeFormat, Inputs: []models.RecipeInput{}, Sections: []models.RecipeSection{}}
	d.Title = sf.Title
	if d.Title == "" {
		d.Title = draftTitle(c, clip)
	}
	d.Title = shorten(d.Title, 120)
	if sf.ProjectID != "" {
		d.Lands = models.RecipeLanding{Kind: models.RecipeLandsProject, ProjectID: sf.ProjectID}
		d.Settings = sf.ProjectID
	} else {
		d.Lands = models.RecipeLanding{Kind: models.RecipeLandsNewProject, Name: "{title} {n}"}
	}
	if c.Length >= 1 {
		d.Length = &models.RecipeRange{Min: math.Max(1, math.Floor(c.Length*0.85)), Max: math.Ceil(c.Length * 1.15)}
	}
	d.Inputs = append(d.Inputs, models.RecipeInput{Name: "footage", Kind: models.RecipeFootage, Required: true,
		Label: "Your footage: what each run chooses its moments from"})
	for _, s := range c.Sections {
		if s.Length < 0.3 {
			continue
		}
		d.Sections = append(d.Sections, models.RecipeSection{Name: s.Name, Kind: models.RecipeChosen, From: "footage", Purpose: shorten(draftPurpose(s), 300),
			Count: draftCount(s), Length: draftLength(s.Length)})
	}
	if len(d.Sections) == 0 {
		d.Sections = append(d.Sections, models.RecipeSection{Name: "video", Kind: models.RecipeChosen, From: "footage", Purpose: "The whole video, in moments of your footage.",
			Count: &models.RecipeRange{Min: 1, Max: 6}, Length: draftLength(math.Max(c.Length, 5))})
	}
	d.Cut = models.RecipeCut{Output: draftOutput(c, clip)}
	if cp := c.Captions; cp.Burned {
		cs := &editspec.Captions{Enabled: true, Style: cp.Look, Position: cp.Position, Uppercase: cp.Capitals, Source: "transcript"}
		if n := int(math.Round(cp.WordsAtATime)); n >= 1 && n <= 12 {
			cs.MaxWordsPerLine = n
		}
		if cp.Size == "large" || cp.Size == "small" {
			cs.Size = cp.Size
		}
		d.Cut.Captions = cs
	}
	d.Rules = draftRules(c)
	d.Hints = draftHints(c)
	ref := &models.RecipeReference{AssetID: clip.ID, URL: clip.Link, Platform: clip.Platform, Who: clip.Who}
	d.Reference = ref
	return d
}

// draftTitle names a draft after where its video came from, never its own
// words: "Like @handle's vertical video", else the clip's own name (a file
// the person brought in).
func draftTitle(c *breakdown.WhatItDoes, clip breakdown.WhatClip) string {
	what := "video"
	if c.Shape != "" {
		what = c.Shape + " video"
	}
	switch {
	case clip.Who != "":
		return "Like " + clip.Who + "'s " + what
	case clip.Link != "":
		return "Like a " + what + " from " + models.PlatformName(clip.Platform)
	}
	return "Like “" + shorten(clip.Title, 80) + "”"
}

// draftPurpose says what a section is for, in MetaRush's words: its kind, its
// length and what happens in it ("A hook of about 3 s: a question to camera.").
func draftPurpose(s breakdown.WhatSection) string {
	kind := map[string]string{breakdown.KindHook: "a hook", breakdown.KindIntro: "an intro", breakdown.KindCTA: "a call to action", breakdown.KindEnd: "an end card"}[s.Kind]
	if s.Kind == breakdown.KindBody {
		kind = strings.ToLower(s.Title)
		if s.Name == "body" {
			kind = "the body"
		}
	}
	return upperFirst(kind) + " of about " + seconds(roundHalf(s.Length)) + ": " + strings.TrimSuffix(s.Does, ".") + "."
}

// draftCount is how many moments a section has: a part with several shots up
// to that many (six at most), else one.
func draftCount(s breakdown.WhatSection) *models.RecipeRange {
	if s.Kind == breakdown.KindBody && s.Shots > 1 {
		return &models.RecipeRange{Min: 1, Max: float64(min(s.Shots, 6))}
	}
	return &models.RecipeRange{Min: 1, Max: 1}
}

// draftLength is a section's length range: a fifth either side, by half seconds.
func draftLength(l float64) *models.RecipeRange {
	lo := math.Max(0.5, roundHalf(l*0.8))
	hi := math.Max(lo+0.5, roundHalf(l*1.2))
	return &models.RecipeRange{Min: lo, Max: hi}
}

func roundHalf(v float64) float64 { return math.Round(v*2) / 2 }

// draftOutput is the draft's frame: the video's shape at its preset, 30 frames a
// second, for the platform it came from.
func draftOutput(c *breakdown.WhatItDoes, clip breakdown.WhatClip) editspec.Output {
	shape := editspec.Shape(c.Shape)
	if shape == "" {
		shape = editspec.ShapeVertical
	}
	out := editspec.Output{Preset: editspec.PresetFor(shape), FPS: 30}
	platform := "generic"
	switch clip.Platform {
	case models.PlatformYouTube:
		platform = "youtube"
	case models.PlatformTikTok:
		platform = "tiktok"
	}
	out.Platform = editspec.PlatformFor(shape, platform)
	if out.Platform == "tiktok" && shape != editspec.ShapeVertical {
		out.Platform = "generic"
	}
	return out
}

// draftRules are the rules the video keeps that runs should too: its length
// (measured), a short hook (a sign-off a person ticks), captions where people
// speak (measured) and the voices over the music (measured).
func draftRules(c *breakdown.WhatItDoes) []models.RecipeRule {
	var out []models.RecipeRule
	if c.Length >= 1 {
		limit := math.Ceil(c.Length * 1.15)
		if limit > 60 {
			limit = math.Ceil(limit/5) * 5
		}
		out = append(out, models.RecipeRule{Text: "Under " + seconds(limit), Measure: fmt.Sprintf("max-length:%g", limit)})
	}
	for _, s := range c.Sections {
		if s.Kind == breakdown.KindHook && s.Length > 0 {
			out = append(out, models.RecipeRule{Text: fmt.Sprintf("The hook is over within %s", seconds(math.Ceil(s.Length+0.5)))})
		}
	}
	if c.Captions.Burned {
		out = append(out, models.RecipeRule{Text: "Captions wherever someone speaks", Measure: "captions"})
	}
	if c.Sound.Bed == breakdown.BedSound && c.Sound.Below >= 6 {
		steps := math.Min(30, math.Floor(c.Sound.Below))
		out = append(out, models.RecipeRule{Text: fmt.Sprintf("The voices stay about %.0f dB over the music", steps), Measure: fmt.Sprintf("speech-over-music:%g", steps)})
	}
	return out
}

// draftHints are what guides Claude when it chooses the moments: how the video
// is paced and spoken, its captions, the layout pack's parts it uses and its
// music. MetaRush's words, never the video's.
func draftHints(c *breakdown.WhatItDoes) []string {
	out := []string{"Shaped like a video someone liked: its sections, lengths and pacing. Its footage, words and pictures aren't part of it; every section is your own footage."}
	var pace []string
	if c.Pacing.Shots > 1 {
		pace = append(pace, fmt.Sprintf("a new shot about every %s", seconds(c.Pacing.ShotSeconds)))
	}
	if c.Pacing.WordsPerMinute > 0 {
		pace = append(pace, fmt.Sprintf("about %.0f words a minute while someone speaks", c.Pacing.WordsPerMinute))
	}
	if len(pace) > 0 {
		out = append(out, "Pace it the same: "+joinAnd(pace)+".")
	}
	for _, s := range c.Sections {
		if s.Kind == breakdown.KindHook && s.Question {
			out = append(out, "Open with a question in the first seconds.")
		}
	}
	switch n := len(c.Speakers); {
	case n == 1:
		out = append(out, "One person speaks all the way through.")
	case n > 1:
		out = append(out, fmt.Sprintf("%d people speak; the first about %.0f%% of the time.", n, c.Speakers[0].Share*100))
	}
	if cp := c.Captions; cp.Burned {
		h := fmt.Sprintf("Captions burned in, about %.0f words at a time", cp.WordsAtATime)
		if cp.Capitals {
			h += ", in capitals"
		}
		out = append(out, h+".")
	}
	title, lower, label := false, false, false
	for _, g := range c.Graphics {
		switch g.Kind {
		case breakdown.GraphicTitle:
			title = true
		case breakdown.GraphicLowerThird:
			lower = true
		case breakdown.GraphicLabel:
			label = true
		}
	}
	hook := 0.0
	for _, s := range c.Sections {
		if s.Kind == breakdown.KindHook {
			hook = s.Length
		}
	}
	if title {
		out = append(out, fmt.Sprintf("Open with your layout pack's title card for about %s, over the hook.", seconds(roundHalf(math.Max(1.5, math.Min(hook, 4))))))
	}
	if lower {
		out = append(out, "Name people with your layout pack's lower third.")
	}
	if label {
		out = append(out, "A name or handle stays on screen all the way through.")
	}
	if c.End != nil {
		out = append(out, fmt.Sprintf("End on your layout pack's end screen for about %s.", seconds(roundHalf(c.End.To-c.End.From))))
	}
	switch c.Sound.Bed {
	case breakdown.BedSound:
		h := fmt.Sprintf("Music under the voices, about %.0f dB quieter than the words", c.Sound.Below)
		if c.Sound.Throughout {
			h += ", and on to the end"
		}
		out = append(out, h+".")
	case breakdown.BedNone:
		out = append(out, "No music under the voices: the pauses stay quiet.")
	}
	return out
}

// stateWord says a version's state plainly.
func stateWord(s string) string {
	switch s {
	case models.RecipeCurrent:
		return "current"
	case models.RecipeDraft:
		return "a draft"
	}
	return "an earlier version"
}
