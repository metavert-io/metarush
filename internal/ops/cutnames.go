package ops

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Descriptive names for cuts and versions (D150). A cut (a project's branch)
// is named on the project, outside the edit, so renaming it never saves a
// version or undoes an approval; a version carries a short note.

// CutView is one cut of a project, by its descriptive name.
type CutView struct {
	Branch string `json:"branch"`
	// Name is what the cut is called: the name a person or MetaRush gave it,
	// else its version's title when that is descriptive, else Suggestion.
	Name   string `json:"name"`
	NameBy string `json:"nameBy,omitempty" jsonschema:"enum=,enum=metarush,enum=person,enum=title,enum=suggested,description=Who named it: a person\\, MetaRush\\, the version's own title\\, or nobody yet (MetaRush's suggestion shows)."`
	// Suggestion is MetaRush's name for it ("Main open day video") when
	// nobody named it yet.
	Suggestion     string `json:"suggestion,omitempty"`
	HeadRevisionID string `json:"headRevisionId,omitempty"`
	HeadNumber     int    `json:"headNumber,omitempty"`
}

var (
	versionTokRe = regexp.MustCompile(`^(?i)(v|r|ver|version|cut|take|draft|final|edit|copy|new|old|main|tmp|test|wip)?\d*$`)
	wordSplitRe  = regexp.MustCompile(`[\s_\-./:]+`)
)

// DescriptiveTitle reports whether a version's title names what the cut is,
// rather than repeating the project's and the cut's names and a number
// ("fernbrook main v2" is not descriptive; "Spring open house teaser" is).
func DescriptiveTitle(project, branch, title string) bool {
	skip := map[string]bool{}
	for _, w := range wordSplitRe.Split(strings.ToLower(project+" "+branch), -1) {
		skip[w] = true
	}
	n := 0
	for _, w := range wordSplitRe.Split(strings.ToLower(strings.TrimSpace(title)), -1) {
		if w == "" || skip[w] || versionTokRe.MatchString(w) || len([]rune(w)) < 3 {
			continue
		}
		n++
	}
	return n >= 2
}

// purposeWords are what a brief's goal says a video is for.
var purposeWords = []struct{ re, words string }{
	{`aware`, "awareness video"}, {`teas`, "teaser"}, {`trailer`, "trailer"}, {`promo|promot|advert|\bads?\b|campaign`, "promo"},
	{`explain|how (it|to)|walkthrough|demo`, "explainer"}, {`tutorial|lesson|teach`, "tutorial"}, {`announce|launch`, "announcement"},
	{`recap|highlight`, "highlights"}, {`testimonial|review`, "testimonial"}, {`interview`, "interview"}, {`documentar`, "documentary"},
	{`fundrais|donat|appeal`, "fundraising video"}, {`recruit|hiring|join (us|the team)`, "recruiting video"}, {`event|conference|meetup`, "event video"},
	{`welcome|introduc|\bintro\b`, "introduction"}, {`story|journey`, "story"}, {`thank`, "thank-you video"}, {`update|news`, "update"},
}

// SuggestCutName is MetaRush's name for a cut: what the brief says it is for
// ("Main open day video"), else its shape and length ("Main 45-second
// vertical cut").
func SuggestCutName(p *models.Project, branch string, head *models.Revision) string {
	prefix := ""
	switch {
	case branch == "" || branch == "main":
		if len(p.Branches) > 1 {
			prefix = "Main "
		}
	default:
		prefix = capFirstWord(strings.ReplaceAll(branch, "-", " ")) + " "
	}
	goal := strings.ToLower(p.Brief.Goal)
	for _, pw := range purposeWords {
		if goal != "" && regexp.MustCompile(pw.re).MatchString(goal) {
			return capFirstWord(prefix + pw.words)
		}
	}
	shape := ""
	if head != nil {
		switch head.Spec.Output.Preset {
		case editspec.PresetVertical:
			shape = "vertical "
		case "landscape-1080p":
			shape = "landscape "
		case "square-1080":
			shape = "square "
		}
		if d := head.Duration; d > 0 {
			if d < 90 {
				shape = fmt.Sprintf("%d-second %s", int(math.Round(d)), shape)
			} else {
				shape = fmt.Sprintf("%d-minute %s", int(math.Round(d/60)), shape)
			}
		}
	}
	if prefix == "" && shape == "" {
		return "The cut"
	}
	return capFirstWord(prefix + shape + "cut")
}

func capFirstWord(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Cuts lists a project's cuts by their names, main first.
func Cuts(ctx context.Context, a *app.App, p *models.Project) []CutView {
	var out []CutView
	var heads []string
	for _, h := range p.Branches {
		if h.Head != "" {
			heads = append(heads, h.Head)
		}
	}
	revs := map[string]*models.Revision{}
	if len(heads) > 0 {
		var list []models.Revision
		_ = a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": heads}}, &list)
		for i := range list {
			revs[list[i].ID] = &list[i]
		}
	}
	for b, h := range p.Branches {
		if h.Head == "" {
			continue
		}
		out = append(out, cutView(p, b, revs[h.Head]))
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Branch == "main") != (out[j].Branch == "main") {
			return out[i].Branch == "main"
		}
		return out[i].Branch < out[j].Branch
	})
	return out
}

func cutView(p *models.Project, branch string, head *models.Revision) CutView {
	h := p.Branches[branch]
	v := CutView{Branch: branch, HeadRevisionID: h.Head}
	if head != nil {
		v.HeadNumber = head.Number
	}
	switch {
	case strings.TrimSpace(h.Title) != "":
		v.Name, v.NameBy = h.Title, h.TitleBy
	case head != nil && DescriptiveTitle(p.Name, branch, head.Spec.Title):
		v.Name, v.NameBy = head.Spec.Title, "title"
	default:
		v.Suggestion = SuggestCutName(p, branch, head)
		v.Name, v.NameBy = v.Suggestion, "suggested"
	}
	return v
}

// CutName is what a project's cut is called (CutView.Name).
func CutName(ctx context.Context, a *app.App, p *models.Project, branch string) string {
	if branch == "" {
		branch = "main"
	}
	h := p.Branches[branch]
	if strings.TrimSpace(h.Title) != "" {
		return h.Title
	}
	var head *models.Revision
	if h.Head != "" {
		var r models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), h.Head, &r) == nil {
			head = &r
		}
	}
	return cutView(p, branch, head).Name
}

// versionNote is a new version's note: the change's own, else what was
// asked (its first clause), else the changes in words, and who wrote it.
func versionNote(plan *editspec.Plan, diff *editspec.Diff, via string) (string, string) {
	by := models.NoteByPerson
	if via == models.ViaAssistant || via == models.ViaAssistantAuto || strings.HasPrefix(plan.Origin, "assistant:") {
		by = models.NoteByClaude
	}
	if n := oneLine(plan.Note); n != "" {
		return clipNote(n), by
	}
	if in := oneLine(plan.Instruction); in != "" {
		// The first sentence of what was asked, as a phrase.
		if i := strings.IndexAny(in, ".!?;\n"); i > 0 {
			in = in[:i]
		}
		return clipNote(notePhrase(in)), models.NoteByMetaRush
	}
	if len(plan.Ops) > 0 {
		return clipNote(notePhrase(editspec.OpsSummary(plan.Ops))), models.NoteByMetaRush
	}
	if plan.Spec != nil && plan.BaseRevisionID == "" {
		return "the first cut", models.NoteByMetaRush
	}
	_ = diff
	return "", ""
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func notePhrase(s string) string {
	if s == "" {
		return s
	}
	// A name or "I" keeps its capital; a phrase that starts with quoted words
	// (a clip's name) or anything but a letter stays as it is.
	w := []rune(strings.Fields(s)[0])
	if string(w) == "I" || (len(w) > 1 && unicode.IsUpper(w[0]) && !unicode.IsLower(w[1])) {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	if !unicode.IsUpper(r) {
		return s
	}
	return string(unicode.ToLower(r)) + s[size:]
}

func clipNote(s string) string {
	r := []rune(s)
	if len(r) <= 80 {
		return strings.TrimRight(s, " ,")
	}
	cut := string(r[:79])
	if i := strings.LastIndex(cut, " "); i > 40 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,") + "…"
}

// CutRenameIn names a project's cut.
type CutRenameIn struct {
	ProjectID string `json:"projectId" jsonschema:"minLength=1,description=The project (ID or name)."`
	Branch    string `json:"branch,omitempty" jsonschema:"description=Which cut (default: the main one)."`
	Title     string `json:"title,omitempty" jsonschema:"maxLength=120"`
	Suggested bool   `json:"suggested,omitempty" jsonschema:"description=Use MetaRush's suggested name."`
}

// RevisionNoteIn changes a version's note.
type RevisionNoteIn struct {
	ID   string `json:"id" jsonschema:"minLength=1,description=The version (rev_…)."`
	Note string `json:"note" jsonschema:"maxLength=80,description=A few words saying what this version changed (empty removes the note)."`
}

func init() {
	Def("cut.rename", "Name a project's cut descriptively (“Main open day video”), or take MetaRush's suggestion; no new version is saved and approvals stay", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutRenameIn) (*CutView, error) {
		p, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		b := in.Branch
		if b == "" {
			b = "main"
		}
		h, ok := p.Branches[b]
		if !ok || h.Head == "" {
			return nil, apperr.New(apperr.NotFound, "%s has no cut to name yet.", upperFirst(branchWords(p.Name, b)))
		}
		var head *models.Revision
		var r models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), h.Head, &r) == nil {
			head = &r
		}
		title, by := oneLine(in.Title), models.TitleByPerson
		if in.Suggested {
			if title != "" {
				return nil, invalid("Give a name, or take the suggested one, not both.")
			}
			title, by = SuggestCutName(p, b, head), models.TitleByMetaRush
		}
		if title == "" {
			return nil, invalid("Give the cut a name.")
		}
		if n := len([]rune(title)); n > 120 {
			return nil, invalid("That name is %d characters long; a cut's name can be up to 120.", n)
		}
		if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), p.ID, bson.M{"$set": bson.M{"branches." + b + ".title": title, "branches." + b + ".titleBy": by, "updatedAt": time.Now().UTC()}}); err != nil {
			return nil, err
		}
		prev := cutView(p, b, head).Name
		a.Audit(ctx, "cut.rename", "project", p.ID, p.ID, map[string]any{"branch": b, "title": title, "previous": prev, "by": by})
		h.Title, h.TitleBy = title, by
		p.Branches[b] = h
		v := cutView(p, b, head)
		return &v, nil
	})
	Def("revision.note", "Change a version's note: a few words saying what it changed (“flicker fixes and headline cards”); the version itself never changes", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *RevisionNoteIn) (*RevisionSummary, error) {
		var r models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), in.ID, &r); err != nil {
			return nil, versionNotFound(in.ID)
		}
		note := oneLine(in.Note)
		if n := len([]rune(note)); n > 80 {
			return nil, invalid("That note is %d characters long; a version's note can be up to 80.", n)
		}
		upd := bson.M{"$set": bson.M{"note": note, "noteBy": models.NoteByPerson}}
		if note == "" {
			upd = bson.M{"$unset": bson.M{"note": "", "noteBy": ""}}
		}
		if err := a.DB.Update(ctx, store.CRevisions, a.WorkspaceID(), r.ID, upd); err != nil {
			return nil, err
		}
		a.Audit(ctx, "revision.note", "revision", r.ID, r.ProjectID, map[string]any{"note": note, "previous": r.Note})
		r.Note, r.NoteBy = note, models.NoteByPerson
		if note == "" {
			r.NoteBy = ""
		}
		var p models.Project
		_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), r.ProjectID, &p)
		s := summarizeRev(a, r, p.Branches[r.Branch].Head)
		return &s, nil
	})
}
