package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// What changed between two versions of a recipe (D212), in plain words: the
// recipe's page (each version against the one it was made from, a version
// against the current one before a person makes it current), `metarush recipe
// diff`, and Claude's cards for a draft or for making a version current
// (D211). Changes are grouped by part — what changes each run, the sections,
// rules, sign-offs and hints, and the title, the video, where runs go, the
// Content Settings, sound, graphics and pictures — each a sentence a person
// reads; nothing here names an ID, a hash or a field of the document.

// The parts a change is about (RecipeChangePart.Part).
const (
	partTitle    = "title"
	partVideo    = "video"
	partLands    = "lands"
	partSettings = "settings"
	partInputs   = "inputs"
	partSections = "sections"
	partSound    = "sound"
	partGraphics = "graphics"
	partPictures = "pictures"
	partRules    = "rules"
	partSignoffs = "signoffs"
	partHints    = "hints"
	partOther    = "other"
)

// maxChanges is how many changes one part lists before "and N more".
const maxChanges = 12

// RecipeChangePart is one part of a recipe and what changed in it, plainly.
type RecipeChangePart struct {
	Part    string   `json:"part" jsonschema:"enum=title,enum=video,enum=lands,enum=settings,enum=inputs,enum=sections,enum=sound,enum=graphics,enum=pictures,enum=rules,enum=signoffs,enum=hints,enum=other"`
	Label   string   `json:"label" jsonschema:"description=The part as the recipe's page names it (What changes each run)."`
	Changes []string `json:"changes" jsonschema:"description=Each change\\, a sentence."`
}

// RecipeDiffIn picks two versions of a recipe.
type RecipeDiffIn struct {
	Recipe string `json:"recipe" jsonschema:"minLength=1,description=Its short name\\, title or ID."`
	// To is the version looked at; From the one it is compared with.
	To   string `json:"to,omitempty" jsonschema:"description=The version to look at: its number\\, current or draft (default: the newest draft\\, else the current one)."`
	From string `json:"from,omitempty" jsonschema:"description=The version it is compared with: its number\\, current or draft (default: the one it was made from\\, else the one before it)."`
}

// RecipeDiffOut says what changed from one version of a recipe to another.
type RecipeDiffOut struct {
	Recipe string `json:"recipe" jsonschema:"description=The recipe's short name."`
	Title  string `json:"title"`
	// From is absent when To is the first version (nothing before it).
	From  *RecipeVersionRow  `json:"from,omitempty"`
	To    RecipeVersionRow   `json:"to"`
	Parts []RecipeChangePart `json:"parts"`
	// Same: the two are one version (nothing changed).
	Same bool `json:"same"`
	// RemovedRules are From's rules that To no longer keeps (said before a
	// person makes a version current, D206).
	RemovedRules []string `json:"removedRules,omitempty"`
	// Changes counts the changes in all.
	Changes int    `json:"changes"`
	Summary string `json:"summary"`
}

func init() {
	ops.Def("recipe.diff", "Say what changed between two versions of a recipe, in plain words: what changes each run, its sections, rules, sign-offs and hints, and its title, the video, where runs go, sound, graphics and pictures", ops.Flags{}, diffRecipe)
}

func diffRecipe(ctx context.Context, a *app.App, in *RecipeDiffIn) (*RecipeDiffOut, error) {
	r, err := loadRecipe(ctx, a, in.Recipe)
	if err != nil {
		return nil, err
	}
	toRef := strings.TrimSpace(in.To)
	if toRef == "" && r.Draft != "" {
		toRef = "draft"
	}
	to, err := loadVersion(ctx, a, r, toRef)
	if err != nil {
		return nil, err
	}
	vs, err := listVersions(ctx, a, r.ID)
	if err != nil {
		return nil, err
	}
	numbers := map[string]int{}
	for _, x := range vs {
		numbers[x.Hash] = x.Number
	}
	from, err := diffBase(ctx, a, r, to, in.From, vs)
	if err != nil {
		return nil, err
	}
	out := &RecipeDiffOut{Recipe: r.Name, Title: r.Title, To: versionRow(to, numbers), Parts: []RecipeChangePart{}}
	if from == nil {
		out.Summary = fmt.Sprintf("Version %d is the first version of “%s”: there is nothing before it to compare it with.", to.Number, r.Title)
		return out, nil
	}
	fr := versionRow(from, numbers)
	out.From = &fr
	if from.Hash == to.Hash {
		out.Same = true
		out.Summary = fmt.Sprintf("That is version %d itself: nothing to compare.", to.Number)
		return out, nil
	}
	out.Parts = Differences(ctx, a, &from.Doc, &to.Doc)
	out.RemovedRules = removedRules(&from.Doc, &to.Doc)
	out.Changes = countChanges(out.Parts)
	out.Summary = diffSummary(to.Number, from.Number, out.Parts, out.Changes)
	return out, nil
}

// diffBase is the version another one is compared with: the one asked for,
// else the one it was made from, else the one before it (nil: it is the first).
func diffBase(ctx context.Context, a *app.App, r *models.Recipe, to *models.RecipeVersion, ref string, vs []models.RecipeVersion) (*models.RecipeVersion, error) {
	if strings.TrimSpace(ref) != "" {
		return loadVersion(ctx, a, r, ref)
	}
	for i := range vs {
		if to.Parent != "" && vs[i].Hash == to.Parent {
			return &vs[i], nil
		}
	}
	for i := range vs {
		if vs[i].Number == to.Number-1 {
			return &vs[i], nil
		}
	}
	return nil, nil
}

// countChanges counts the changes of every part ("and 3 more" counts 3).
func countChanges(parts []RecipeChangePart) int {
	n := 0
	for _, p := range parts {
		for _, c := range p.Changes {
			var more int
			if _, err := fmt.Sscanf(c, "and %d more", &more); err == nil {
				n += more
				continue
			}
			n++
		}
	}
	return n
}

// diffSummary says in one sentence what a version changes.
func diffSummary(to, from int, parts []RecipeChangePart, n int) string {
	if len(parts) == 0 {
		return fmt.Sprintf("Version %d makes the same video as version %d.", to, from)
	}
	var where []string
	for _, p := range parts {
		where = append(where, lowerFirst(p.Label))
	}
	return fmt.Sprintf("Version %d changes %s from version %d: %s.", to, plural(n, "thing"), from, joinAnd(where))
}

// Differences says what changed from one recipe document to another, part by
// part, in plain words (none when it makes the same video).
func Differences(ctx context.Context, a *app.App, was, now *models.RecipeDoc) []RecipeChangePart {
	out := []RecipeChangePart{}
	add := func(part, label string, changes []string) {
		if len(changes) == 0 {
			return
		}
		if len(changes) > maxChanges {
			more := len(changes) - maxChanges
			changes = append(changes[:maxChanges:maxChanges], fmt.Sprintf("and %d more", more))
		}
		out = append(out, RecipeChangePart{Part: part, Label: label, Changes: changes})
	}
	add(partTitle, "Its title", titleChanges(was, now))
	add(partVideo, "The video", videoChanges(was, now))
	add(partLands, "Where runs go", landsChanges(ctx, a, was, now))
	add(partSettings, "Content Settings", settingsChanges(ctx, a, was, now))
	add(partInputs, "What changes each run", inputChanges(ctx, a, was, now))
	add(partSections, "Its sections", sectionChanges(ctx, a, was, now))
	add(partSound, "Sound", soundChanges(ctx, a, was, now))
	add(partGraphics, "Graphics", graphicChanges(was, now))
	add(partPictures, "Pictures", pictureChanges(was, now))
	add(partRules, "Rules every run keeps", listChanges(ruleTexts(was), ruleTexts(now), "New rule", "No longer a rule"))
	add(partSignoffs, "Sign-offs", listChanges(was.Signoffs, now.Signoffs, "New sign-off", "No longer a sign-off"))
	add(partHints, "Hints for Claude", listChanges(was.Hints, now.Hints, "New hint", "No longer a hint"))
	if len(out) == 0 && !sameDoc(was, now) {
		add(partOther, "Other details", otherChanges(was, now))
	}
	return out
}

// sameDoc reports whether two documents make the same video (what a recipe
// makes is said from the rest, so it isn't compared).
func sameDoc(x, y *models.RecipeDoc) bool {
	a, b := Clone(x), Clone(y)
	a.Makes, b.Makes = "", ""
	_, h1 := Canonical(a)
	_, h2 := Canonical(b)
	return h1 == h2
}

// removedRules are the rules of was that now no longer keeps.
func removedRules(was, now *models.RecipeDoc) []string {
	keep := map[string]bool{}
	for _, r := range now.Rules {
		keep[strings.TrimSpace(r.Text)] = true
	}
	var out []string
	for _, r := range was.Rules {
		if t := strings.TrimSpace(r.Text); !keep[t] {
			out = append(out, t)
		}
	}
	return out
}

func ruleTexts(d *models.RecipeDoc) []string {
	out := make([]string, 0, len(d.Rules))
	for _, r := range d.Rules {
		out = append(out, r.Text)
	}
	return out
}

// listChanges says which of a list's lines are new and which are gone.
func listChanges(was, now []string, added, removed string) []string {
	had, has := map[string]bool{}, map[string]bool{}
	for _, x := range was {
		had[strings.TrimSpace(x)] = true
	}
	for _, x := range now {
		has[strings.TrimSpace(x)] = true
	}
	var out []string
	for _, x := range now {
		if t := strings.TrimSpace(x); t != "" && !had[t] {
			out = append(out, added+": “"+t+"”")
			had[t] = true
		}
	}
	for _, x := range was {
		if t := strings.TrimSpace(x); t != "" && !has[t] {
			out = append(out, removed+": “"+t+"”")
			has[t] = true
		}
	}
	return out
}

// nowWas says "X now (was Y)", or "X now" when Y is nothing.
func nowWas(now, was string) string {
	if was == "" {
		return now + " now"
	}
	return now + " now (was " + was + ")"
}

func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) > 1 && r[0] >= 'A' && r[0] <= 'Z' && !(r[1] >= 'A' && r[1] <= 'Z') {
		r[0] = r[0] - 'A' + 'a'
	}
	return string(r)
}

func titleChanges(was, now *models.RecipeDoc) []string {
	if w, n := strings.TrimSpace(was.Title), strings.TrimSpace(now.Title); w != n {
		return []string{nowWas("Called “"+n+"”", "“"+w+"”")}
	}
	return nil
}

// shapeWords names a document's frame: vertical, widescreen, square.
func shapeWords(d *models.RecipeDoc) string {
	shape := editspec.PresetLabel(d.Cut.Output.Preset)
	if shape == "" {
		shape = "widescreen"
		if d.Cut.Output.Width > 0 && d.Cut.Output.Height > d.Cut.Output.Width {
			shape = "vertical"
		}
	}
	if shape == "landscape" {
		shape = "widescreen"
	}
	return shape
}

// rangeWords says a length a recipe keeps to ("25–35 s", "up to 35 s"; "" none).
func rangeWords(l *models.RecipeRange) string {
	switch {
	case l == nil || (l.Min <= 0 && l.Max <= 0):
		return ""
	case l.Min > 0 && l.Max > 0 && l.Min != l.Max:
		return trimSeconds(l.Min) + "–" + seconds(l.Max)
	case l.Max > 0 && l.Min == l.Max:
		return seconds(l.Max)
	case l.Max > 0:
		return "up to " + seconds(l.Max)
	}
	return "at least " + seconds(l.Min)
}

var platformWords = map[string]string{"generic": "any platform", "youtube": "YouTube", "shorts": "YouTube Shorts", "tiktok": "TikTok", "reels": "Instagram Reels"}

var framingWords = map[string]string{"faces": "keep faces in frame", "follow": "follow the subject", "center": "keep the middle", "": "keep the middle"}

func videoChanges(was, now *models.RecipeDoc) []string {
	var out []string
	if w, n := shapeWords(was), shapeWords(now); w != n {
		out = append(out, nowWas(upperFirst(n), w))
	}
	if w, n := rangeWords(was.Length), rangeWords(now.Length); w != n {
		switch {
		case n == "":
			out = append(out, "No length to keep to any more (was "+w+")")
		default:
			out = append(out, nowWas("Keeps to "+n, w))
		}
	}
	wo, no := was.Cut.Output, now.Cut.Output
	if wo.FPS != no.FPS && wo.FPS > 0 && no.FPS > 0 {
		out = append(out, fmt.Sprintf("%d frames a second now (was %d)", no.FPS, wo.FPS))
	}
	if wo.Platform != no.Platform {
		n, w := platformWords[no.Platform], platformWords[wo.Platform]
		if n == "" {
			n = "any platform"
		}
		if w == "" {
			w = "any platform"
		}
		out = append(out, nowWas("Made for "+n, w))
	}
	if framingWords[wo.Framing] != framingWords[no.Framing] {
		out = append(out, nowWas("Crops "+framingWords[no.Framing], framingWords[wo.Framing]))
	}
	if wo.Background != no.Background {
		out = append(out, "Another background colour now")
	}
	if w, n := strings.TrimSpace(was.Cut.Title), strings.TrimSpace(now.Cut.Title); w != n && n != "" {
		out = append(out, nowWas("The video's title is “"+n+"”", quoteIf(w)))
	}
	return out
}

func quoteIf(s string) string {
	if s == "" {
		return ""
	}
	return "“" + s + "”"
}

func landsChanges(ctx context.Context, a *app.App, was, now *models.RecipeDoc) []string {
	w, n := landsWords(ctx, a, was), landsWords(ctx, a, now)
	if w != n {
		return []string{nowWas("Each run makes "+n, w)}
	}
	if now.Lands.Kind == models.RecipeLandsNewProject && strings.TrimSpace(was.Lands.Name) != strings.TrimSpace(now.Lands.Name) {
		return []string{nowWas("New projects are named like “"+newProjectName(now, 1)+"”", "like “"+newProjectName(was, 1)+"”")}
	}
	return nil
}

// settingsWords names whose Content Settings runs follow.
func settingsWords(ctx context.Context, a *app.App, id string) string {
	if id == "" {
		return "the default Content Settings"
	}
	var p models.Project
	if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), id, &p) == nil {
		return "“" + p.Name + "”'s Content Settings"
	}
	return "the Content Settings of a project that was cleared out"
}

func settingsChanges(ctx context.Context, a *app.App, was, now *models.RecipeDoc) []string {
	if was.Settings == now.Settings {
		return nil
	}
	return []string{nowWas("Runs follow "+settingsWords(ctx, a, now.Settings), settingsWords(ctx, a, was.Settings))}
}

// lowerThe starts a label that starts with "The" in the middle of a sentence.
func lowerThe(s string) string {
	if strings.HasPrefix(s, "The ") {
		return "the " + s[4:]
	}
	return s
}

func inputChanges(ctx context.Context, a *app.App, was, now *models.RecipeDoc) []string {
	rows := func(d *models.RecipeDoc) map[string]RecipeInputRow {
		out := map[string]RecipeInputRow{}
		for _, r := range inputRows(ctx, a, d) {
			out[r.Name] = r
		}
		return out
	}
	wr, nr := rows(was), rows(now)
	wi := inputByName(was)
	var out []string
	for _, in := range now.Inputs {
		row := nr[in.Name]
		old, had := wr[in.Name]
		if !had {
			s := "New: " + lowerThe(row.Label) + " (" + kindWords(in.Kind)
			if row.Limits != "" {
				s += ", " + row.Limits
			}
			s += ")"
			if len(row.Goes) > 0 {
				s += ", for " + joinAnd(row.Goes)
			}
			out = append(out, s)
			continue
		}
		oin := wi[in.Name]
		name := row.Label
		if old.Label != row.Label {
			out = append(out, "“"+old.Label+"” is called “"+row.Label+"” now")
		}
		if oin.Kind != in.Kind {
			out = append(out, name+": "+nowWas(kindWords(in.Kind), kindWords(oin.Kind)))
		}
		if oin.Required != in.Required {
			if in.Required {
				out = append(out, name+": every run must give it now")
			} else {
				out = append(out, name+": a run may keep the recipe's own now")
			}
		}
		switch {
		case old.Default != row.Default:
			out = append(out, name+": "+nowWas(row.Default+" unless a run gives another", old.Default))
		case !reflect.DeepEqual(oin.Default.Items, in.Default.Items):
			what := "pictures"
			if in.Kind == models.RecipeFootage {
				what = "clips"
			}
			out = append(out, name+": other "+what+" unless a run gives others now")
		}
		if old.Limits != row.Limits {
			out = append(out, name+": "+nowWas(orWords(row.Limits, "no limit"), orWords(old.Limits, "no limit")))
		}
		if old.From != row.From {
			if row.From == "" {
				out = append(out, name+": a run no longer takes "+old.From)
			} else {
				out = append(out, name+": "+nowWas("a run takes "+row.From, ""))
			}
		}
		if !reflect.DeepEqual(old.Goes, row.Goes) {
			out = append(out, name+": "+nowWas("goes to "+orWords(joinAnd(row.Goes), "nothing"), orWords(joinAnd(old.Goes), "nothing")))
		}
	}
	for _, in := range was.Inputs {
		if _, has := nr[in.Name]; !has {
			out = append(out, "No longer changes each run: "+lowerThe(wr[in.Name].Label)+" (runs keep the recipe's own)")
		}
	}
	return out
}

func orWords(s, none string) string {
	if strings.TrimSpace(s) == "" {
		return none
	}
	return s
}

func sectionChanges(ctx context.Context, a *app.App, was, now *models.RecipeDoc) []string {
	rows := func(d *models.RecipeDoc) map[string]RecipeSectionRow {
		out := map[string]RecipeSectionRow{}
		for _, r := range sectionRows(ctx, a, d) {
			out[r.Name] = r
		}
		return out
	}
	wr, nr := rows(was), rows(now)
	ws := map[string]*models.RecipeSection{}
	for i := range was.Sections {
		ws[was.Sections[i].Name] = &was.Sections[i]
	}
	inputName := func(d *models.RecipeDoc, name string) string {
		for _, in := range d.Inputs {
			if in.Name == name {
				return inputLabel(in)
			}
		}
		return "“" + name + "”"
	}
	var out []string
	for _, s := range now.Sections {
		row := nr[s.Name]
		old, had := wr[s.Name]
		n := "“" + s.Name + "”"
		if !had {
			if s.Kind == models.RecipeChosen {
				out = append(out, "New section "+n+": "+lowerFirst(row.What))
			} else {
				out = append(out, "New section "+n+": the same every run, "+row.Length+" ("+row.What+")")
			}
			continue
		}
		prev := ws[s.Name]
		if prev.Kind != s.Kind {
			if s.Kind == models.RecipeChosen {
				out = append(out, n+": Claude chooses its moments now (was the same every run)")
			} else {
				out = append(out, n+": the same every run now (was chosen by Claude)")
			}
		}
		if s.Kind == models.RecipeChosen {
			if strings.TrimSpace(prev.Purpose) != strings.TrimSpace(s.Purpose) {
				out = append(out, n+": "+nowWas("for “"+orWords(s.Purpose, "the strongest moments")+"”", "for “"+orWords(prev.Purpose, "the strongest moments")+"”"))
			}
			if prev.Kind == s.Kind && prev.From != s.From {
				out = append(out, n+": "+nowWas("chosen from "+inputName(now, s.From), inputName(was, prev.From)))
			}
			if prev.Kind == s.Kind && !reflect.DeepEqual(prev.Count, s.Count) {
				out = append(out, n+": "+nowWas(sectionNeeds(models.RecipeSection{Count: s.Count}), sectionNeeds(models.RecipeSection{Count: prev.Count})))
			}
		} else if prev.Kind == s.Kind && old.What != row.What {
			out = append(out, n+": "+nowWas("shows "+row.What, old.What))
		}
		if old.Length != row.Length && prev.Kind == s.Kind {
			out = append(out, n+": "+nowWas(row.Length, old.Length))
		}
	}
	for _, s := range was.Sections {
		if _, has := nr[s.Name]; !has {
			out = append(out, "No more section “"+s.Name+"”")
		}
	}
	// The order of the sections both versions have.
	var wasOrder, nowOrder []string
	for _, s := range was.Sections {
		if _, ok := nr[s.Name]; ok {
			wasOrder = append(wasOrder, s.Name)
		}
	}
	for _, s := range now.Sections {
		if _, ok := wr[s.Name]; ok {
			nowOrder = append(nowOrder, s.Name)
		}
	}
	if !reflect.DeepEqual(wasOrder, nowOrder) {
		out = append(out, nowWas("In another order: "+strings.Join(quoteAll(nowOrder), ", "), strings.Join(quoteAll(wasOrder), ", ")))
	}
	return out
}

// beatsOf are a document's narration lines.
func beatsOf(d *models.RecipeDoc) []editspec.Beat {
	if d.Cut.Voiceover == nil {
		return nil
	}
	return d.Cut.Voiceover.Beats
}

// trackName names a sound track: its clip's title, else its name in the cut.
func trackName(ctx context.Context, a *app.App, t editspec.AudioTrack) string {
	var as models.Asset
	if t.AssetID != "" && a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), t.AssetID, &as) == nil {
		return "“" + shorten(as.DisplayTitle(), 40) + "”"
	}
	return "“" + t.ID + "”"
}

func soundChanges(ctx context.Context, a *app.App, was, now *models.RecipeDoc) []string {
	var out []string
	wb, nb := beatsOf(was), beatsOf(now)
	switch {
	case len(wb) == 0 && len(nb) > 0:
		out = append(out, "Narration now: "+plural(len(nb), "line"))
	case len(wb) > 0 && len(nb) == 0:
		out = append(out, "No narration now (was "+plural(len(wb), "line")+")")
	case len(wb) != len(nb):
		out = append(out, nowWas("The narration has "+plural(len(nb), "line"), plural(len(wb), "line")))
	}
	byID := map[string]editspec.Beat{}
	for _, b := range wb {
		byID[b.ID] = b
	}
	said := 0
	for i, b := range nb {
		old, ok := byID[b.ID]
		if !ok || strings.TrimSpace(old.Text) == strings.TrimSpace(b.Text) {
			continue
		}
		if said++; said > 3 {
			continue
		}
		out = append(out, fmt.Sprintf("Narration line %d says “%s” now", i+1, shorten(b.Text, 80)))
	}
	if said > 3 {
		out = append(out, fmt.Sprintf("%d more narration lines say other words now", said-3))
	}
	// Music and other sounds, by their name in the cut.
	tracks := func(d *models.RecipeDoc) map[string]editspec.AudioTrack {
		out := map[string]editspec.AudioTrack{}
		for _, t := range d.Cut.Audio {
			out[t.ID] = t
		}
		return out
	}
	wt, nt := tracks(was), tracks(now)
	kind := func(t editspec.AudioTrack) string {
		if t.Role == editspec.RoleMusic {
			return "music"
		}
		return "sound"
	}
	for _, t := range now.Cut.Audio {
		old, ok := wt[t.ID]
		switch {
		case !ok:
			out = append(out, "New "+kind(t)+": "+trackName(ctx, a, t))
		case old.AssetID != t.AssetID:
			out = append(out, "The "+kind(t)+" "+trackName(ctx, a, old)+" is "+trackName(ctx, a, t)+" now")
		case !reflect.DeepEqual(old, t):
			out = append(out, "The "+kind(t)+" "+trackName(ctx, a, t)+" plays differently now")
		}
	}
	for _, t := range was.Cut.Audio {
		if _, ok := nt[t.ID]; !ok {
			out = append(out, "No more "+kind(t)+" "+trackName(ctx, a, t))
		}
	}
	on := func(c *editspec.Captions) bool { return c != nil && c.Enabled }
	wc, nc := was.Cut.Captions, now.Cut.Captions
	switch {
	case !on(wc) && on(nc):
		out = append(out, "Captions are on now")
	case on(wc) && !on(nc):
		out = append(out, "Captions are off now")
	case on(wc) && on(nc) && wc.Position != nc.Position:
		out = append(out, nowWas("Captions at the "+orWords(nc.Position, "bottom"), "the "+orWords(wc.Position, "bottom")))
	case on(wc) && on(nc) && !reflect.DeepEqual(wc, nc):
		out = append(out, "The captions look different now")
	}
	if !jsonEqual(was.Cut.Mix, now.Cut.Mix) {
		out = append(out, "The sound mix is different now")
	}
	return out
}

func jsonEqual(x, y any) bool {
	a, _ := json.Marshal(x)
	b, _ := json.Marshal(y)
	return string(a) == string(b)
}

// paramsOf reads a graphic's fields.
func paramsOf(o *editspec.Overlay) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(o.Params, &m)
	return m
}

func graphicChanges(was, now *models.RecipeDoc) []string {
	wo := map[string]*editspec.Overlay{}
	for i := range was.Cut.Overlays {
		wo[was.Cut.Overlays[i].ID] = &was.Cut.Overlays[i]
	}
	seen := map[string]bool{}
	var out []string
	for i := range now.Cut.Overlays {
		o := &now.Cut.Overlays[i]
		seen[o.ID] = true
		old := wo[o.ID]
		if old == nil {
			out = append(out, "New graphic: "+graphicWords(o))
			continue
		}
		g := upperFirst(graphicWords(o))
		sameKind := old.Template == o.Template && (old.Motion == nil) == (o.Motion == nil) && (o.Motion == nil || old.Motion.Name == o.Motion.Name)
		if !sameKind {
			out = append(out, upperFirst(graphicWords(old))+" is "+graphicWords(o)+" now")
			continue
		}
		if o.Motion != nil && !reflect.DeepEqual(old.Motion, o.Motion) {
			out = append(out, g+" is another version now")
		}
		wp, np := paramsOf(old), paramsOf(o)
		keys := map[string]bool{}
		for k := range wp {
			keys[k] = true
		}
		for k := range np {
			keys[k] = true
		}
		var names []string
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		looks := false
		for _, k := range names {
			if reflect.DeepEqual(wp[k], np[k]) {
				continue
			}
			ws, wok := wp[k].(string)
			ns, nok := np[k].(string)
			f := fieldWords[k]
			if f == "" {
				f = k
			}
			switch {
			case wok && nok:
				out = append(out, g+"'s "+f+": "+nowWas("“"+shorten(ns, 80)+"”", "“"+shorten(ws, 80)+"”"))
			case nok && wp[k] == nil:
				out = append(out, g+"'s "+f+": “"+shorten(ns, 80)+"” now")
			default:
				looks = true
			}
		}
		if looks {
			out = append(out, g+" looks different now")
		}
		if old.At != o.At || old.Duration != o.Duration || !reflect.DeepEqual(old.Span, o.Span) {
			out = append(out, g+" shows at another time now")
		}
	}
	for i := range was.Cut.Overlays {
		if o := &was.Cut.Overlays[i]; !seen[o.ID] {
			out = append(out, "No more "+graphicWords(o))
		}
	}
	return out
}

func pictureChanges(was, now *models.RecipeDoc) []string {
	names := map[string]bool{}
	for k := range was.Cut.Resources {
		names[k] = true
	}
	for k := range now.Cut.Resources {
		names[k] = true
	}
	var keys []string
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		w, had := was.Cut.Resources[k]
		n, has := now.Cut.Resources[k]
		switch {
		case !had:
			out = append(out, "Uses the picture “"+k+"” now")
		case !has:
			out = append(out, "No longer uses the picture “"+k+"”")
		case w.ID != n.ID:
			out = append(out, "“"+k+"” is another picture now")
		case w.SHA256 != n.SHA256:
			out = append(out, "The picture “"+k+"” is another version now")
		}
	}
	return out
}

// otherChanges names what else differs when nothing above does.
func otherChanges(was, now *models.RecipeDoc) []string {
	var out []string
	if !jsonEqual(was.Cut.Brand, now.Cut.Brand) {
		out = append(out, "The brand on the cut is different now")
	}
	if !jsonEqual(was.Bindings, now.Bindings) {
		out = append(out, "Where the inputs go is different now")
	}
	if strings.TrimSpace(was.Cut.Notes) != strings.TrimSpace(now.Cut.Notes) {
		out = append(out, "The cut's notes are different now")
	}
	if len(out) == 0 {
		out = append(out, "Details MetaRush keeps for the cut are different now (what it pins, where it came from)")
	}
	return out
}
