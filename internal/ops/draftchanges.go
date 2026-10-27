package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Change drafts (D248): changes that aren't a new version of a cut, kept until
// a person approves them. Only the operations listed here may be drafted, and
// each lists every field a draft of it may hold and how that field reads, so
// the review desk shows exactly what approving does: every project, version
// and clip by its name, every word in full, every object part by part. A
// field not listed refuses the draft, and so does every operation not listed
// (with a plain reason). None of these calls a paid service; each draft is
// priced anyway, when saved and again when approved (draftPrice), so a change
// that would cost money is never approved unseen.

// How a field of a change reads on the desk.
type fieldKind int

const (
	fText    fieldKind = iota // people's words, in full, in quotes
	fChoice                   // one of a few words (an enum)
	fBool                     // yes or no
	fNumber                   // a number
	fSeconds                  // a length in seconds
	fTexts                    // a list of people's words, every one in full
	fProject                  // a project by ID or name: its name (it must exist)
	fVersion                  // a version by ID: "version 3 of “Garden walk”"
	fClip                     // footage by ID, title or file name: its title
	fPicture                  // a picture or font in the resources: its name
	fAt                       // a moment in the video: "at 0:12.5"
	fCard                     // a title or end card of the house style, part by part
)

type changeField struct {
	key, label string
	kind       fieldKind
}

// changeDraft is one operation a change draft may hold.
type changeDraft struct {
	// title is what it does ("Add a house rule"); did what it did ("Added a
	// house rule"); text names the field whose words follow them.
	title, did, text string
	// content: it changes Content Settings (a project's own, or the default's
	// when it names no project).
	content bool
	fields  []changeField
}

var (
	projectField = changeField{"projectId", "Project", fProject}
	ruleChecks   = []changeField{{"check", "Checked in every quality check", fBool}, {"measure", "What the quality check measures", fChoice}, {"from", "Where it came from", fText},
		{"promise", "A promise made to the client (a version that breaks it waits before it goes out)", fBool}}
)

// changeDrafts is the allowlist (D248).
var changeDrafts = map[string]changeDraft{
	"comment.add": {title: "Add a review comment", did: "Added a review comment", text: "body",
		fields: []changeField{projectField, {"revisionId", "On", fVersion}, {"assetId", "On the clip", fClip}, {"at", "At", fAt}, {"body", "Comment", fText}}},
	"revision.note": {title: "Write a version's note", did: "Wrote the version's note", text: "note",
		fields: []changeField{{"id", "Version", fVersion}, {"note", "Note", fText}}},
	"cut.rename": {title: "Rename a cut", did: "Renamed the cut", text: "title",
		fields: []changeField{projectField, {"branch", "Cut", fText}, {"title", "New name", fText}}},
	"asset.update": {title: "Rename a clip", did: "Renamed the clip", text: "title",
		fields: []changeField{{"id", "Clip", fClip}, {"title", "New name", fText}}},
	"project.rules.add": {title: "Add a rule to a project", did: "Added the rule", text: "text",
		fields: append([]changeField{projectField, {"text", "Rule", fText}}, ruleChecks...)},
	"content.rules.add": {title: "Add a house rule", did: "Added the house rule", text: "text", content: true,
		fields: append([]changeField{projectField, {"text", "House rule", fText}}, ruleChecks...)},
	"content.signoffs.add": {title: "Add a house sign-off", did: "Added the house sign-off", text: "text", content: true,
		fields: []changeField{projectField, {"text", "Sign-off", fText}}},
	"transcript.spelling.add": {title: "Put a word on the spelling list", did: "Put the word on the spelling list", text: "word", content: true,
		fields: []changeField{projectField, {"word", "Always written", fText}, {"heardAs", "Transcripts may write it", fTexts}, {"note", "Note", fText},
			{"fixExisting", "Also correct the transcripts that write it another way", fBool}}},
	"transcript.spelling.remove": {title: "Take a word off the spelling list", did: "Took the word off the spelling list", text: "word", content: true,
		fields: []changeField{projectField, {"word", "Word", fText}}},
	"content.style.set": {title: "Change the house style", did: "Changed the house style", content: true,
		fields: []changeField{projectField, {"captions", "Captions", fChoice}, {"captionLook", "Caption look", fChoice}, {"captionSize", "Caption size", fChoice},
			{"captionPosition", "Caption position", fChoice}, {"nameBarLook", "Name bar look", fChoice}, {"nameBarHeight", "Name bar height", fChoice},
			{"nameBarAnimate", "Name bar comes in", fChoice}, {"lowerThird", "Name titles' look", fChoice}, {"framing", "Framing", fChoice},
			{"cutBlend", "Sound blend at straight cuts", fSeconds}, {"levelSpeech", "Voices evened out", fChoice},
			{"intro", "Intro card", fCard}, {"endCard", "End card", fCard}, {"clear", "Settings cleared", fTexts}}},
	"brand.set": {title: "Change the brand", did: "Changed the brand", content: true,
		fields: []changeField{projectField, {"primary", "Primary colour", fText}, {"accent", "Accent colour", fText}, {"text", "Text colour", fText},
			{"background", "Background colour", fText}, {"font", "Font", fText}, {"fontResource", "Font file", fPicture},
			{"logo", "Main logo", fPicture}, {"logoDark", "Logo for dark backgrounds", fPicture}, {"logoLight", "Logo for light backgrounds", fPicture},
			{"logoIcon", "Square icon", fPicture}, {"logoWordmark", "Wordmark", fPicture}, {"fromLogo", "Colours taken from the logo", fBool}}},
}

// ChangeDraftOps lists the changes a change draft may hold (for tests and docs).
func ChangeDraftOps() []string {
	out := make([]string, 0, len(changeDrafts))
	for k := range changeDrafts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// changeShown is what a change draft shows: its title and lines, its
// project, the Content Settings it changes and what approving must still do.
type changeShown struct {
	title     string
	lines     []string
	projectID string
	target    *models.DraftTarget
	expect    *models.DraftExpect
}

// describeChange checks a change draft's input against its operation's
// allowlist entry and says what approving does: a title, every field in
// words with each thing named, the Content Settings it changes (resolved now:
// approving changes those, whichever are the default then), and for the
// brand and a spelling that corrects existing transcripts, what approving
// would change, measured by a dry run.
func describeChange(ctx context.Context, a *app.App, op *Op, spec changeDraft, input map[string]any) (*changeShown, error) {
	known := map[string]changeField{}
	for _, f := range spec.fields {
		known[f.key] = f
	}
	var unknown []string
	for k := range input {
		if _, ok := known[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, apperr.New(apperr.InvalidInput, "A draft of this change can't hold every setting it was given, so it isn't saved: leave out %s, or ask the person.", strings.Join(unknown, ", ")).
			WithDetails(map[string]any{"op": op.Name, "fields": unknown, "reason": "draft_field_not_shown"})
	}
	sh := &changeShown{}
	if ref, _ := input["projectId"].(string); strings.TrimSpace(ref) != "" {
		p, err := resolveProject(ctx, a, ref)
		if err != nil {
			return nil, err
		}
		sh.projectID = p.ID
		input["projectId"] = p.ID // approving acts on the project shown, whatever its name becomes
	}
	if spec.content {
		t, err := contentTarget(ctx, a, sh.projectID)
		if err != nil {
			return nil, err
		}
		sh.target = t
		input["projectId"] = t.ProjectID // these settings, even if another project's become the default
		if sh.projectID == "" {
			sh.projectID = t.ProjectID
		}
		sh.lines = append(sh.lines, targetWords(ctx, a, t))
	}
	for _, f := range spec.fields {
		v, ok := input[f.key]
		if !ok || v == nil || f.key == "projectId" && spec.content {
			continue
		}
		words, pid, norm, err := fieldWordsFor(ctx, a, f, v, sh.projectID)
		if err != nil {
			return nil, err
		}
		if norm != nil {
			input[f.key] = norm // approving acts on the thing shown, whatever its name becomes
		}
		if sh.projectID == "" && pid != "" {
			sh.projectID = pid
		}
		if words != "" && op.Name != "brand.set" {
			sh.lines = append(sh.lines, f.label+": "+words)
		}
	}
	switch op.Name {
	case "brand.set":
		lines, kit, err := brandPreview(ctx, a, input)
		if err != nil {
			return nil, err
		}
		sh.lines = append(sh.lines, lines...)
		sh.expect = &models.DraftExpect{Kit: kit}
	case "transcript.spelling.add":
		if fix, _ := input["fixExisting"].(bool); fix {
			line, places, err := spellingPreview(ctx, a, sh.target.ProjectID, input)
			if err != nil {
				return nil, err
			}
			sh.lines = append(sh.lines, line)
			sh.expect = &models.DraftExpect{Fixes: true, Places: places}
		}
	}
	sh.title = spec.title
	if s, _ := input[spec.text].(string); strings.TrimSpace(s) != "" {
		sh.title += ": " + q(clipWords(s, 80)) // the full words are in the lines
	}
	if len(sh.lines) == 0 {
		sh.lines = []string{sh.title + "."}
	}
	return sh, nil
}

// contentTarget resolves the Content Settings a change names (a project's
// own, or the default's when it names none) to the record it changes.
func contentTarget(ctx context.Context, a *app.App, projectID string) (*models.DraftTarget, error) {
	t, _, err := content.EditTarget(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	out := &models.DraftTarget{ProjectID: t.ProjectID, Name: t.ProjectName, Default: t.Default}
	if rec, err := content.Record(ctx, a, t.ProjectID); err == nil && rec != nil {
		out.ContentID = rec.ID
	}
	return out, nil
}

// targetWords says which Content Settings a change goes to, by name, and who
// else uses them.
func targetWords(ctx context.Context, a *app.App, t *models.DraftTarget) string {
	s := "Where: the Content Settings of " + q(t.Name)
	g, err := content.Load(ctx, a)
	var users []string
	if err == nil {
		for _, id := range g.UsedBy(t.ProjectID) {
			users = append(users, q(g.Name(id)))
		}
	}
	switch {
	case g != nil && g.DefaultOwner() == t.ProjectID && len(users) > 0:
		s += ", the default (new projects use them), also used by " + joinAnd(users)
	case g != nil && g.DefaultOwner() == t.ProjectID:
		s += ", the default (new projects use them)"
	case len(users) > 0:
		s += ", also used by " + joinAnd(users)
	}
	return s + "."
}

// recheckChange measures again, at approval, what a change draft would do,
// and refuses when it would now do more or other than was shown (D248).
func recheckChange(ctx context.Context, a *app.App, d *models.Draft, input map[string]any) error {
	stays := "The draft stays waiting: ask the app for a new one, or make the change yourself."
	if t := d.Target; t != nil {
		now, err := contentTarget(ctx, a, t.ProjectID)
		if err != nil {
			return apperr.New(apperr.Conflict, "The Content Settings this draft changes (%s's) can't be changed from here any more. %s", q(t.Name), stays).WithDetails(map[string]any{"cause": apperr.From(err).Message})
		}
		if t.ContentID != "" && now.ContentID != t.ContentID {
			return apperr.New(apperr.Conflict, "%s's Content Settings were replaced since this draft was saved. %s", q(t.Name), stays)
		}
	}
	ex := d.Expect
	if ex == nil {
		return nil
	}
	if ex.Kit != nil {
		lines, kit, err := brandPreview(ctx, a, input)
		if err != nil {
			return err
		}
		var differ []string
		for _, k := range kitParts {
			if kit[k.key] != ex.Kit[k.key] {
				differ = append(differ, k.label)
			}
		}
		if len(differ) > 0 {
			return apperr.New(apperr.Conflict, "The brand changed since this draft was saved: approving would now set %s differently from what it shows. %s", joinAnd(differ), stays).
				WithDetails(map[string]any{"now": lines})
		}
	}
	if ex.Fixes {
		line, places, err := spellingPreview(ctx, a, d.Target.ProjectID, input)
		if err != nil {
			return err
		}
		shown := map[string]bool{}
		for _, p := range ex.Places {
			shown[p] = true
		}
		more := 0
		for _, p := range places {
			if !shown[p] {
				more++
			}
		}
		if more > 0 {
			return apperr.New(apperr.Conflict, "Since this draft was saved, the word appears in %s more in existing transcripts, which approving would correct too. %s", places1(more), stays).
				WithDetails(map[string]any{"now": line})
		}
	}
	return nil
}

func places1(n int) string {
	if n == 1 {
		return "1 place"
	}
	return fmt.Sprintf("%d places", n)
}

// kitParts are a brand's parts, in the order people read them.
var kitParts = []struct{ key, label string }{
	{"logo", "the main logo"}, {"logoDark", "the logo for dark backgrounds"}, {"logoLight", "the logo for light backgrounds"},
	{"logoIcon", "the square icon"}, {"logoWordmark", "the wordmark"}, {"primary", "the primary colour"}, {"accent", "the accent colour"},
	{"text", "the text colour"}, {"background", "the background colour"}, {"font", "the font"}, {"fontResource", "the font file"},
}

func kitMap(k *models.BrandKit) map[string]string {
	if k == nil {
		k = &models.BrandKit{}
	}
	return map[string]string{"logo": k.Logo, "logoDark": k.LogoDark, "logoLight": k.LogoLight, "logoIcon": k.LogoIcon, "logoWordmark": k.LogoWordmark,
		"primary": k.Primary, "accent": k.Accent, "text": k.Text, "background": k.Background, "font": k.Font, "fontResource": k.FontResource}
}

// brandPreview dry-runs brand.set and says every part of the brand that
// approving would change, from what to what (colours a new logo brings
// included), and the brand as it would be.
func brandPreview(ctx context.Context, a *app.App, input map[string]any) ([]string, map[string]string, error) {
	var in BrandSetIn
	if err := strictInput(input, &in); err != nil {
		return nil, nil, err
	}
	in.DryRun = true
	before, _, err := kitFor(ctx, a, in.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	out, err := brandSet(ctx, a, &in)
	if err != nil {
		return nil, nil, err
	}
	was, now := kitMap(before), kitMap(&out.Kit)
	var lines []string
	for _, k := range kitParts {
		if was[k.key] == now[k.key] {
			continue
		}
		lines = append(lines, upperFirst(k.label)+": "+kitValueWords(ctx, a, k.key, was[k.key])+" → "+kitValueWords(ctx, a, k.key, now[k.key]))
	}
	if len(lines) == 0 {
		return nil, nil, apperr.New(apperr.InvalidInput, "Nothing to propose: the brand would stay as it is.")
	}
	return lines, now, nil
}

func kitValueWords(ctx context.Context, a *app.App, key, v string) string {
	if v == "" {
		return "none"
	}
	if strings.HasPrefix(key, "logo") || key == "fontResource" {
		var r models.Resource
		if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), v, &r) == nil {
			return "the picture " + q(firstNonEmpty(r.Title, r.Name))
		}
	}
	return q(v)
}

// spellingPreview finds the places in existing transcripts a new spelling
// would correct (the clips using these settings' spelling list), and says how
// many, in which videos and projects.
func spellingPreview(ctx context.Context, a *app.App, owner string, input map[string]any) (string, []string, error) {
	word, _ := input["word"].(string)
	w := transcripts.Space(word)
	e := models.SpellingEntry{Word: w, Key: transcripts.SpellingKey(w)}
	if heard, ok := input["heardAs"].([]any); ok {
		for _, h := range heard {
			if s, ok := h.(string); ok && transcripts.Space(s) != "" {
				e.HeardAs = append(e.HeardAs, transcripts.Space(s))
			}
		}
	}
	sc, err := settingsScope(ctx, a, owner)
	if err != nil {
		return "", nil, err
	}
	clips, err := transcripts.Clips(ctx, a, sc)
	if err != nil {
		return "", nil, err
	}
	places := []string{}
	var videos []string
	projects := map[string]bool{}
	inbox := false
	for i := range clips {
		as := &clips[i]
		segs, err := transcripts.LoadSegments(ctx, a, as.ID)
		if err != nil {
			return "", nil, err
		}
		hits := transcripts.SpellingHits(as, segs, []models.SpellingEntry{e})
		if len(hits) == 0 {
			continue
		}
		for _, h := range hits {
			places = append(places, h.ID)
		}
		videos = append(videos, q(as.DisplayTitle()))
		if len(as.ProjectIDs) == 0 {
			inbox = true
		}
		for _, pid := range as.ProjectIDs {
			projects[pid] = true
		}
	}
	if len(places) > maxSpellingPlaces {
		return "", nil, apperr.New(apperr.InvalidInput, "Correcting existing transcripts would change %d places: too many for one draft. Leave out fixExisting, or ask the person.", len(places))
	}
	if len(places) == 0 {
		return "Also correct existing transcripts: none write it another way today.", places, nil
	}
	var where []string
	for pid := range projects {
		where = append(where, q(projectName(ctx, a, pid)))
	}
	sort.Strings(where)
	if inbox {
		where = append(where, "footage in no project")
	}
	return fmt.Sprintf("Also correct existing transcripts: %s in %s — %s — in %s.", places1(len(places)), pluralWord(len(videos), "video"), strings.Join(videos, ", "), joinAnd(where)), places, nil
}

// maxSpellingPlaces is the most places one spelling draft corrects.
const maxSpellingPlaces = 5000

// fieldWordsFor says one field's value in words; a reference names its thing
// (and gives the project it belongs to, when it has one, and its ID to keep).
func fieldWordsFor(ctx context.Context, a *app.App, f changeField, v any, projectID string) (string, string, any, error) {
	w, pid, err := fieldWords1(ctx, a, f, v, projectID)
	if err != nil {
		return "", "", nil, err
	}
	var norm any
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		switch f.kind {
		case fClip:
			if as, err := findAsset(ctx, a, s, projectID); err == nil {
				norm = as.ID
			}
		case fPicture:
			if e, _, err := findResource(ctx, a, ResourceRefIn{Ref: s, ProjectID: projectID}); err == nil {
				norm = e.Resource.ID
			}
		case fProject:
			norm = pid
		}
	}
	return w, pid, norm, nil
}

func fieldWords1(ctx context.Context, a *app.App, f changeField, v any, projectID string) (string, string, error) {
	bad := func() (string, string, error) {
		return "", "", apperr.New(apperr.InvalidInput, "%s isn't in a form MetaRush accepts.", f.label).WithDetails(map[string]any{"field": f.key})
	}
	str, isStr := v.(string)
	switch f.kind {
	case fText, fChoice:
		if !isStr {
			return bad()
		}
		if strings.TrimSpace(str) == "" {
			return "nothing (cleared)", "", nil
		}
		if f.kind == fChoice {
			return str, "", nil
		}
		return q(str), "", nil
	case fBool:
		b, ok := v.(bool)
		if !ok {
			return bad()
		}
		if b {
			return "yes", "", nil
		}
		return "no", "", nil
	case fNumber, fSeconds:
		n, ok := v.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return bad()
		}
		if f.kind == fSeconds {
			return fmt.Sprintf("%g s", n), "", nil
		}
		return fmt.Sprintf("%g", n), "", nil
	case fAt:
		n, ok := v.(float64)
		if !ok || n < 0 {
			return bad()
		}
		return clockWords(n), "", nil
	case fTexts:
		list, ok := v.([]any)
		if !ok {
			return bad()
		}
		var parts []string
		for _, e := range list {
			s, ok := e.(string)
			if !ok {
				return bad()
			}
			parts = append(parts, q(s))
		}
		if len(parts) == 0 {
			return "none", "", nil
		}
		return strings.Join(parts, ", "), "", nil
	case fProject:
		if !isStr {
			return bad()
		}
		p, err := resolveProject(ctx, a, str)
		if err != nil {
			return "", "", err
		}
		return q(p.Name), p.ID, nil
	case fVersion:
		if !isStr {
			return bad()
		}
		r, p, err := getRevision(ctx, a, str)
		if err != nil {
			return "", "", err
		}
		words := fmt.Sprintf("version %d of %s", r.Number, q(p.Name))
		if r.DraftID != "" {
			words += " (a draft, not in the cut)"
		}
		return words, p.ID, nil
	case fClip:
		if !isStr {
			return bad()
		}
		as, err := findAsset(ctx, a, str, projectID)
		if err != nil {
			return "", "", err
		}
		return clipWords2(ctx, a, as), "", nil
	case fPicture:
		if !isStr {
			return bad()
		}
		if strings.TrimSpace(str) == "" {
			return "none (taken away)", "", nil
		}
		e, _, err := findResource(ctx, a, ResourceRefIn{Ref: str, ProjectID: projectID})
		if err != nil {
			return "", "", err
		}
		name := e.Resource.Title
		if name == "" {
			name = e.Resource.Name
		}
		return "the picture " + q(name), "", nil
	case fCard:
		m, ok := v.(map[string]any)
		if !ok {
			return bad()
		}
		return cardWords(m)
	}
	return bad()
}

// cardWords says a house style card in full, part by part.
func cardWords(m map[string]any) (string, string, error) {
	labels := map[string]string{"template": "look", "seconds": "seconds on screen", "title": "title", "subtitle": "line under it", "cta": "call to action", "handle": "handle"}
	for k := range m {
		if _, ok := labels[k]; !ok {
			return "", "", apperr.New(apperr.InvalidInput, "A card setting isn't one MetaRush shows (%s), so the draft isn't saved.", k).WithDetails(map[string]any{"field": k})
		}
	}
	var parts []string
	for _, k := range []string{"template", "title", "subtitle", "cta", "handle", "seconds"} {
		if _, ok := m[k]; !ok {
			continue
		}
		switch x := m[k].(type) {
		case string:
			if k == "template" {
				if x == "" || x == "none" {
					parts = append(parts, "taken away")
					continue
				}
				parts = append(parts, labels[k]+" "+x)
				continue
			}
			parts = append(parts, labels[k]+" "+q(x))
		case float64:
			parts = append(parts, fmt.Sprintf("%s %g", labels[k], x))
		default:
			return "", "", apperr.New(apperr.InvalidInput, "A card setting isn't in a form MetaRush accepts (%s).", k).WithDetails(map[string]any{"field": k})
		}
	}
	if len(parts) == 0 {
		return "unchanged", "", nil
	}
	return strings.Join(parts, "; "), "", nil
}

func clockWords(sec float64) string {
	m := int(sec) / 60
	s := sec - float64(m*60)
	return fmt.Sprintf("%d:%04.1f", m, s)
}

func projectName(ctx context.Context, a *app.App, id string) string {
	if p := projectOf(ctx, a, id); p != nil {
		return p.Name
	}
	return "a project"
}

// changeDone says in words what an approved change did: "Added the house
// rule “…” to the default Content Settings".
func changeDone(d *models.Draft) string {
	spec, ok := changeDrafts[d.Op]
	if !ok {
		return "Made the change: " + strings.TrimSuffix(d.Title, ".") + "."
	}
	s := spec.did
	var in map[string]any
	_ = json.Unmarshal([]byte(d.Input), &in)
	if t, _ := in[spec.text].(string); strings.TrimSpace(t) != "" {
		s += " " + q(t)
	}
	if d.Target != nil {
		s += " in the Content Settings of " + q(d.Target.Name)
	}
	return s + "."
}

// draftPrice is what approving a draft costs at outside services. None of
// the changes a draft may hold calls a paid service (a test keeps paidOps off
// both lists), so it is nothing — said as what it is, not as an estimate.
// It is worked out when the draft is saved and again when it is approved, and
// a draft that would cost money is refused (D248).
func draftPrice(d *models.Draft) costs.Estimate {
	e := costs.Estimate{About: "nothing", Parts: []costs.Part{}}
	switch d.Kind {
	case models.DraftVersion:
		e.Notes = []string{"Approving moves a version into the cut; MetaRush makes its review copy on this computer. No paid outside service is used."}
	default:
		e.Notes = []string{"This change doesn't use a paid outside service."}
	}
	return e
}

// paidOps are the changes that spend money at an outside service; no draft
// may hold one (a test keeps the allowlists apart from them).
var paidOps = []string{"generate.shot", "generate.again", "generate.extend", "generate.edit", "voiceover.tts", "sound.enhance", "recipe.run", "recipe.again", "recipe.continue",
	"ai.make"}

// PaidOps lists them (for tests).
func PaidOps() []string { return append([]string(nil), paidOps...) }

// clipWords2 names a clip so two with the same title can be told apart: its
// title, its file name and the projects it is in.
func clipWords2(ctx context.Context, a *app.App, as *models.Asset) string {
	s := q(as.DisplayTitle())
	var extra []string
	if as.Name != "" && as.Name != as.DisplayTitle() {
		extra = append(extra, "file "+q(as.Name))
	}
	var in []string
	for _, pid := range as.ProjectIDs {
		in = append(in, q(projectName(ctx, a, pid)))
	}
	sort.Strings(in)
	if len(in) > 0 {
		extra = append(extra, "in "+joinAnd(in))
	} else {
		extra = append(extra, "in no project")
	}
	return s + " (" + strings.Join(extra, ", ") + ")"
}
