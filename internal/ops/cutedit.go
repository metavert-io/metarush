package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// The cut's editing verbs (`metarush cut add|move|trim|split|delete|delete-range
// |remove-words`, D57): small edits of the latest version, naming clips the way
// the cut sheet does (or by number), written as typed ops and checked, confirmed
// and saved like every other edit. A dry run (or a call without a token outside
// the dashboard) shows the change in plain words with its warnings and a
// confirm token; the same call with the token saves it as a new version.

// CutEditCommon are the fields every editing verb takes.
type CutEditCommon struct {
	ProjectID    string `json:"projectId" jsonschema:"description=Project ID or its exact name."`
	Branch       string `json:"branch,omitempty" jsonschema:"description=Default main."`
	DryRun       bool   `json:"dryRun,omitempty" jsonschema:"description=Only check: the change in plain words\\, warnings and a confirm token."`
	ConfirmToken string `json:"confirmToken,omitempty" jsonschema:"description=The token from a dry run of exactly this change."`
	Instruction  string `json:"instruction,omitempty" jsonschema:"maxLength=2000,description=What was asked (recorded with the new version)."`
	Rationale    string `json:"rationale,omitempty" jsonschema:"maxLength=4000"`
	AllowUnlock  bool   `json:"allowUnlock,omitempty" jsonschema:"description=Permit changing locked clips. Only when the person explicitly asked."`
}

// CutEditOut is an editing verb's change, checked (dry run) or saved.
type CutEditOut struct {
	ProjectID      string                 `json:"projectId"`
	Branch         string                 `json:"branch"`
	Summary        string                 `json:"summary" jsonschema:"description=The change in one plain sentence."`
	Changes        []string               `json:"changes" jsonschema:"description=What changes\\, in plain words (as a cut sheet check lists them)."`
	DurationBefore float64                `json:"durationBefore"`
	Duration       float64                `json:"duration" jsonschema:"description=The video's length after the change (seconds)."`
	NoChange       bool                   `json:"noChange"`
	Saved          bool                   `json:"saved" jsonschema:"description=The change is in the cut as a new version."`
	Problems       []cutsheet.Problem     `json:"problems" jsonschema:"description=Problems (errors) and notes (warnings) about the change\\, in plain words."`
	Removals       []cutsheet.WordRemoval `json:"removals,omitempty" jsonschema:"description=remove-words: the stretches taken out\\, or with cleanup the suggestions (numbered from 1 in this order)."`
	Plan           json.RawMessage        `json:"plan" jsonschema:"type=object,description=The edit plan (typed ops against the latest version)."`
	Check          *EditCheckOut          `json:"check,omitempty"`
	Applied        *EditApplyOut          `json:"applied,omitempty"`
}

// Edit is the edit itself (outputs that say more embed it: cut.flashes, cut.names).
func (o *CutEditOut) Edit() *CutEditOut { return o }

// CutAddIn puts footage into the latest cut as a new clip.
type CutAddIn struct {
	CutEditCommon
	Footage  string `json:"footage" jsonschema:"description=The footage: its short name in the cut sheet\\, its title\\, file name or ID."`
	From     string `json:"from,omitempty" jsonschema:"description=Where the clip starts in the footage: a time (0:12.4 or 12.4) or quoted words to start on. Default: the beginning."`
	To       string `json:"to,omitempty" jsonschema:"description=Where it ends: a time or words to end on. Default: the end of the footage."`
	Position string `json:"position,omitempty" jsonschema:"description=Where it goes: end (default)\\, start\\, after <clip>\\, before <clip>\\, or a number (1 = the first place)."`
	Name     string `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64,description=The new clip's name (default: made from the footage's name)."`
	Muted    bool   `json:"muted,omitempty" jsonschema:"description=Without its own sound."`
}

// CutMoveIn moves a clip to another place in the latest cut.
type CutMoveIn struct {
	CutEditCommon
	Clip     string `json:"clip" jsonschema:"description=The clip: its name in the cut sheet or its number (1 = the first)."`
	Position string `json:"position" jsonschema:"description=start\\, end\\, after <clip>\\, before <clip>\\, or a number (the place it ends up at)."`
}

// CutTrimIn changes where a clip starts or ends in its footage (or slides the
// footage under it).
type CutTrimIn struct {
	CutEditCommon
	Clip  string   `json:"clip" jsonschema:"description=The clip: its name in the cut sheet or its number."`
	Start string   `json:"start,omitempty" jsonschema:"description=The new start: a time in its footage (0:12.4)\\, a change (+0.5 = half a second later\\, -0.5 = earlier) or quoted words to start on."`
	End   string   `json:"end,omitempty" jsonschema:"description=The new end: a time\\, a change (+0.5 = later\\, -0.5 = earlier) or words to end on."`
	Roll  bool     `json:"roll,omitempty" jsonschema:"description=Move the cut instead: the neighbouring clip's edge moves with it\\, so the video keeps its length."`
	Slip  *float64 `json:"slip,omitempty" jsonschema:"minimum=-36000,maximum=36000,description=Slide the footage under the clip by this many seconds (positive: later in the recording); its length and place stay the same."`
}

// CutSplitIn cuts a clip in two.
type CutSplitIn struct {
	CutEditCommon
	At   string `json:"at" jsonschema:"description=Where: a time on the video's timeline (0:12.4)\\; with clip\\, a time in that clip's footage or quoted words to split just before."`
	Clip string `json:"clip,omitempty" jsonschema:"description=The clip (name or number); then at is in its footage."`
	Name string `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64,description=The second part's name (default: the clip's name with -2)."`
}

// CutDeleteIn removes clips from the latest cut.
type CutDeleteIn struct {
	CutEditCommon
	Clips []string `json:"clips" jsonschema:"minItems=1,description=The clips to remove (names or numbers)."`
}

// CutDeleteRangeIn removes a stretch of the video; the rest closes up.
type CutDeleteRangeIn struct {
	CutEditCommon
	From string `json:"from" jsonschema:"description=Where the stretch starts on the video's timeline (0:12.4 or 12.4)."`
	To   string `json:"to" jsonschema:"description=Where it ends."`
}

// CutRemoveWordsIn takes words out of the latest cut, cutting in the pauses around them.
type CutRemoveWordsIn struct {
	CutEditCommon
	Words   string   `json:"words,omitempty" jsonschema:"description=The words to take out\\, as said (\"um\\, anyway\"); small transcription slips are fine."`
	Clip    string   `json:"clip,omitempty" jsonschema:"description=Only in this clip (name or number)."`
	Which   int      `json:"which,omitempty" jsonschema:"minimum=0,description=When the words are said more than once: which time (1 = the first)."`
	All     bool     `json:"all,omitempty" jsonschema:"description=Every time the words are said."`
	Cleanup bool     `json:"cleanup,omitempty" jsonschema:"description=Suggest ums\\, words said twice and false starts (a dry run lists them; saving takes those in pick\\, or all of them)."`
	Pick    []int    `json:"pick,omitempty" jsonschema:"description=With cleanup: the suggestions to take (1 = the first listed); empty = all."`
	From    *float64 `json:"from,omitempty" jsonschema:"minimum=0,description=With clip: the words heard from this moment of its footage (source seconds; a word counts when its middle is inside)."`
	To      *float64 `json:"to,omitempty" jsonschema:"minimum=0,description=With clip and from: up to this moment."`
}

// CutPausesIn shortens long pauses between words in the latest cut (D236):
// every pause heard inside a clip that is longer than LongerThan seconds is
// shortened to To seconds, the cut landing in the middle of the quiet. A dry
// run lists them, numbered; saving takes all of them, or those in Pick.
type CutPausesIn struct {
	CutEditCommon
	LongerThan float64 `json:"longerThan,omitempty" jsonschema:"minimum=0,maximum=30,description=Pauses longer than this many seconds are shortened (as they play; default 1)."`
	To         float64 `json:"to,omitempty" jsonschema:"minimum=0,maximum=10,description=What each is shortened to\\, in seconds (default 0.5; less than longerThan)."`
	Clip       string  `json:"clip,omitempty" jsonschema:"description=Only in this clip (name or number)."`
	Pick       []int   `json:"pick,omitempty" jsonschema:"description=The pauses to shorten (1 = the first listed); empty = all."`
}

// editTarget is the latest version a verb edits, with what it needs to name things.
type editTarget struct {
	project *models.Project
	branch  string
	rev     *models.Revision
	spec    *editspec.Spec
	facts   *dbFacts
	shots   []editspec.ShotInfo
	info    *sheetInfo
}

func loadEditTarget(ctx context.Context, a *app.App, c CutEditCommon) (*editTarget, error) {
	p, err := resolveProject(ctx, a, c.ProjectID)
	if err != nil {
		return nil, err
	}
	branch := c.Branch
	if branch == "" {
		branch = "main"
	}
	head := p.Branches[branch].Head
	if head == "" {
		return nil, apperr.New(apperr.NotFound, "%s has no cut yet.", p.Name).WithHint("Make a first cut (for example from a cut sheet), then edit it.")
	}
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &r); err != nil {
		return nil, versionNotFound(head)
	}
	facts, err := loadFacts(ctx, a, p.ID, &r.Spec)
	if err != nil {
		return nil, err
	}
	return &editTarget{project: p, branch: branch, rev: &r, spec: &r.Spec, facts: facts, shots: editspec.Shots(&r.Spec, facts), info: newSheetInfo(ctx, a)}, nil
}

// clip finds a clip by its name or number.
func (t *editTarget) clip(ref string) (int, error) {
	if i := clipIndex(t.spec, ref); i >= 0 {
		return i, nil
	}
	var names []string
	for _, c := range t.spec.Clips {
		names = append(names, c.ID)
	}
	return -1, apperr.New(apperr.NotFound, "There's no clip %q in the latest cut.", strings.TrimSpace(ref)).
		WithHint(fmt.Sprintf("Name a clip (%s) or give its number (1–%d).", strings.Join(names, ", "), len(t.spec.Clips)))
}

// who is a clip in words ("Ada's clip", "the “Kitchen” shot").
func (t *editTarget) who(i int) string {
	if i >= 0 && i < len(t.shots) {
		return t.shots[i].Who
	}
	return "the clip"
}

// lockedEnding is the ID of the cut's final clip when it is locked.
func (t *editTarget) lockedEnding() string {
	if n := len(t.spec.Clips); n > 0 && t.spec.Clips[n-1].Locked {
		return t.spec.Clips[n-1].ID
	}
	return ""
}

// insertWhere says where a new clip goes, for summaries.
func (t *editTarget) insertWhere(after, before string) string {
	switch {
	case before != "" && clipIndex(t.spec, before) == 0:
		return "at the start"
	case before != "" && before == t.lockedEnding():
		return "before the locked ending (" + t.who(clipIndex(t.spec, before)) + ")"
	case before != "":
		return "before " + t.who(clipIndex(t.spec, before))
	case after != "" && clipIndex(t.spec, after) < len(t.spec.Clips)-1:
		return "after " + t.who(clipIndex(t.spec, after))
	}
	return "at the end"
}

func (t *editTarget) length() float64 { return specLength(t.spec) }

func specLength(s *editspec.Spec) float64 {
	_, total := editspec.Timeline(s)
	if s.Output.FPS == 0 {
		return 0
	}
	return float64(total) / float64(s.Output.FPS)
}

// position turns "start", "end", "after <clip>", "before <clip>" or a number
// into an op's after/before (for a clip that is not there yet when moving is
// false; for moving, the clip itself is left out of the count). With nothing
// said and a cut that ends on a locked clip, the place is just before that
// ending (it stays last); "end" said outright still means after it.
func (t *editTarget) position(words, moving string) (after, before string, err error) {
	w := strings.TrimSpace(strings.ToLower(strings.Join(strings.Fields(words), " ")))
	w = strings.TrimPrefix(w, "to ")
	var order []string
	for _, c := range t.spec.Clips {
		if c.ID != moving {
			order = append(order, c.ID)
		}
	}
	bad := apperr.New(apperr.InvalidInput, "Say where: start, end, after <clip>, before <clip> or a place number, not %q.", words)
	switch {
	case w == "" || w == "end" || w == "the end" || w == "last":
		if len(order) > 0 {
			last := order[len(order)-1]
			if w == "" && t.lockedEnding() == last {
				return "", last, nil
			}
			return last, "", nil
		}
		return "", "", nil
	case w == "start" || w == "the start" || w == "beginning" || w == "the beginning" || w == "first":
		if len(order) > 0 {
			return "", order[0], nil
		}
		return "", "", nil
	}
	if rest, ok := strings.CutPrefix(w, "after "); ok {
		i, err := t.clip(rest)
		if err != nil {
			return "", "", err
		}
		return t.spec.Clips[i].ID, "", nil
	}
	if rest, ok := strings.CutPrefix(w, "before "); ok {
		i, err := t.clip(rest)
		if err != nil {
			return "", "", err
		}
		return "", t.spec.Clips[i].ID, nil
	}
	n, convErr := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(w, "place "), "#"))
	if convErr != nil {
		return "", "", bad
	}
	switch {
	case n < 1 || n > len(order)+1:
		return "", "", apperr.New(apperr.InvalidInput, "There are places 1 to %d, not %d.", len(order)+1, n)
	case n == 1 && len(order) > 0:
		return "", order[0], nil
	case n == 1:
		return "", "", nil
	}
	return order[n-2], "", nil
}

// sourceTime reads a time in a clip's footage: a clock time, quoted words
// (edge in: start on them; end: end on them), or with relative a change
// ("+0.5", "-0.5") from cur.
func (t *editTarget) sourceTime(assetID, value, edge string, cur float64, relative bool) (float64, error) {
	v := strings.TrimSpace(value)
	if relative && (strings.HasPrefix(v, "+") || strings.HasPrefix(v, "-")) {
		d, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "s"), 64)
		if err != nil {
			return 0, apperr.New(apperr.InvalidInput, "%q isn't a change in seconds (like +0.5 or -1.2).", value)
		}
		return math.Round((cur+d)*1000) / 1000, nil
	}
	if s, ok := cutsheet.ParseClock(v); ok {
		return s, nil
	}
	words := strings.Trim(v, "\"“”'")
	a, _ := t.facts.Asset(assetID)
	at, err := cutsheet.PhraseTime(t.facts.Transcript(assetID), words, edge, a.Duration)
	if err != nil {
		return 0, apperr.New(apperr.InvalidInput, "%s.", upperFirst(err.Error())).WithHint("Use a time like 0:12.4, or other words from the transcript.")
	}
	return at, nil
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[:1])) + string(r[1:])
}

// timelineTime reads a time on the video's timeline.
func timelineTime(value string) (float64, error) {
	if s, ok := cutsheet.ParseClock(strings.TrimSpace(value)); ok {
		return s, nil
	}
	return 0, apperr.New(apperr.InvalidInput, "%q isn't a time in the video (like 0:12.4 or 12.4).", value)
}

var idSafe = regexp.MustCompile(`[^a-z0-9_-]+`)

// newClipID is a free clip name made from a footage name.
func newClipID(s *editspec.Spec, from string) string {
	id := strings.Trim(idSafe.ReplaceAllString(strings.ToLower(from), "-"), "-_")
	if id == "" || id[0] < 'a' || id[0] > 'z' {
		id = "clip-" + id
	}
	if len(id) > 60 {
		id = strings.Trim(id[:60], "-_")
	}
	used := map[string]bool{"captions": true, "voiceover": true, "mix": true}
	for _, c := range s.Clips {
		used[c.ID] = true
	}
	for _, x := range s.Audio {
		used[x.ID] = true
	}
	for _, x := range s.Overlays {
		used[x.ID] = true
	}
	if s.Voiceover != nil {
		for _, b := range s.Voiceover.Beats {
			used[b.ID] = true
		}
	}
	if !used[id] {
		return id
	}
	return editspec.PartID(&editspec.Spec{Clips: s.Clips, Audio: s.Audio, Overlays: s.Overlays, Voiceover: s.Voiceover}, id)
}

// finish checks or saves a verb's ops against the head, fills the plain
// change and returns the result.
func (t *editTarget) finish(ctx context.Context, a *app.App, c CutEditCommon, verb string, ops []editspec.Op, summary, instruction string, out *CutEditOut) (*CutEditOut, error) {
	out.ProjectID, out.Branch, out.Summary = t.project.ID, t.branch, summary
	out.DurationBefore = round3(t.length())
	next, _, err := editspec.ApplyOps(t.spec, ops, c.AllowUnlock)
	if err != nil {
		var le *editspec.LockedError
		if errors.As(err, &le) {
			return nil, t.lockedErr(le)
		}
		return nil, apperr.New(apperr.InvalidInput, "%s", plainOpError(err)).WithDetails(map[string]any{"error": err.Error()})
	}
	editspec.Normalize(next)
	out.Duration = round3(specLength(next))
	out.Problems = []cutsheet.Problem{}
	names := cutsheet.NameFunc(t.spec, t.info, projectAssetIDs(ctx, a, t.project.ID))
	plain := func(issues []editspec.Issue) {
		for _, is := range issues {
			out.Problems = append(out.Problems, cutsheet.Locate(is, next, &cutsheet.SourceMap{}, t.info, names))
		}
	}
	out.Changes = cutsheet.Describe(t.spec, next, names)
	if out.Changes == nil {
		out.Changes = []string{}
	}
	if editspec.Hash(next) == editspec.Hash(t.spec) {
		out.NoChange = true
		out.Summary = "Nothing would change: " + lowerFirst(summary)
		raw, _ := json.Marshal(editspec.Plan{BaseRevisionID: t.rev.ID})
		out.Plan = raw
		return out, nil
	}
	if c.Instruction != "" {
		instruction = c.Instruction
	}
	origin := "cut-" + verb + ":cli"
	if a.Actor.Via == "dashboard" {
		origin = "cut-" + verb + ":studio"
	}
	plan := editspec.Plan{BaseRevisionID: t.rev.ID, Instruction: instruction, Rationale: c.Rationale, Origin: origin, Ops: ops}
	if t.branch != "main" {
		plan.Branch = t.branch
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	out.Plan = raw
	if c.DryRun || (c.ConfirmToken == "" && a.Actor.Via != "dashboard") {
		chk, err := checkPlan(ctx, a, raw, c.AllowUnlock)
		if err != nil {
			return nil, t.plainErr(err)
		}
		out.Check = chk
		plain(chk.Errors)
		plain(chk.Warnings)
		if chk.Valid {
			chk.Next = "Show the person the change; if they agree, make the same change again with its confirm token to save it."
		}
		return out, nil
	}
	res, err := applyPlan(ctx, a, raw, c.ConfirmToken, c.AllowUnlock)
	if err != nil {
		return nil, t.plainErr(err)
	}
	plain(res.Warnings)
	out.Applied, out.Saved = res, true
	return out, nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToLower(string(r[:1])) + string(r[1:])
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// lockedErr says which clip is locked, in words.
func (t *editTarget) lockedErr(le *editspec.LockedError) error {
	who := "a locked clip"
	for i, c := range t.spec.Clips {
		if c.ID == le.ID {
			who = t.who(i)
		}
	}
	return apperr.New(apperr.Locked, "This would change %s, which is locked.", who).WithDetails(map[string]any{"locked": le.ID}).
		WithHint("Locked parts stay as they are unless the person asks to unlock them.")
}

// names is how the cut sheet names footage, for plain messages.
func (t *editTarget) names() func(string) string {
	return cutsheet.NameFunc(t.spec, t.info, projectAssetIDs(t.info.ctx, t.info.a, t.project.ID))
}

func (t *editTarget) plainErr(err error) error {
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		return err
	}
	switch ae.Code {
	case apperr.Locked:
		return t.lockedErr(&editspec.LockedError{ID: lockedID(ae)})
	case apperr.Validation:
		d, _ := ae.Details.(map[string]any)
		if errs, ok := d["errors"].([]editspec.Issue); ok && len(errs) > 0 {
			p := cutsheet.Locate(errs[0], t.spec, &cutsheet.SourceMap{}, t.info, t.names())
			return apperr.New(apperr.Validation, "This change can't be saved. %s", p.Message).WithDetails(ae.Details).WithHint(p.Hint)
		}
	}
	return err
}

// plainOpError turns an op's refusal ("Change 1 (splitClip): …") into a
// sentence: what is wrong, without the change's number and kind.
func plainOpError(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "): "); i >= 0 && strings.HasPrefix(msg, "Change ") {
		msg = msg[i+3:]
	}
	return upperFirst(strings.TrimSuffix(msg, ".")) + "."
}

// resolveFootage finds project footage by short name, title, file name or ID.
func (t *editTarget) resolveFootage(ctx context.Context, a *app.App, ref string) (*models.Asset, error) {
	ref = strings.TrimSpace(ref)
	ids := projectAssetIDs(ctx, a, t.project.ID)
	names := cutsheet.NameFunc(t.spec, t.info, ids)
	var found []*models.Asset
	low := strings.ToLower(ref)
	for _, id := range ids {
		as := t.info.asset(id)
		if as == nil {
			continue
		}
		if id == ref || names(id) == low || strings.EqualFold(as.Name, ref) || strings.EqualFold(as.DisplayTitle(), ref) || strings.EqualFold(strings.TrimSuffix(as.Name, extOf(as.Name)), ref) {
			found = append(found, as)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		if as := t.info.asset(ref); as != nil {
			return nil, apperr.New(apperr.InvalidInput, "%s isn't part of %s yet.", as.DisplayTitle(), t.project.Name).WithHint("Add it to the project first, then add it to the cut.").
				WithCommand("metarush asset link " + as.ID + " --project " + t.project.ID)
		}
		var list []string
		for _, id := range ids {
			list = append(list, names(id))
		}
		return nil, apperr.New(apperr.NotFound, "%s has no footage called %q.", t.project.Name, ref).WithHint("The footage is: " + strings.Join(list, ", ") + ".")
	}
	return nil, apperr.New(apperr.InvalidInput, "More than one piece of footage is called %q.", ref).WithHint("Use its short name from the cut sheet's footage list.")
}

func extOf(name string) string {
	if i := strings.LastIndex(name, "."); i > 0 && len(name)-i <= 6 {
		return name[i:]
	}
	return ""
}

func init() {
	Def("cut.add", "Put footage into the latest cut as a new clip (all of it, or from a time or words to another), at the end, the start or next to a clip; dryRun checks it and gives a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutAddIn) (*CutEditOut, error) {
		t, err := loadEditTarget(ctx, a, in.CutEditCommon)
		if err != nil {
			return nil, err
		}
		as, err := t.resolveFootage(ctx, a, in.Footage)
		if err != nil {
			return nil, err
		}
		if !as.Media.HasVideo {
			return nil, apperr.New(apperr.InvalidInput, "%s has no picture, so it can't be a clip.", as.DisplayTitle()).WithHint("Sound without picture goes in the cut's sound (music, narration).")
		}
		// The footage's facts (duration, transcript) for times and words.
		probe := editspec.Clone(t.spec)
		probe.Clips = append(probe.Clips, editspec.Clip{ID: "probe", AssetID: as.ID, In: 0, Out: 1})
		if t.facts, err = loadFacts(ctx, a, t.project.ID, probe); err != nil {
			return nil, err
		}
		from, to := 0.0, as.Media.Duration
		if in.From != "" {
			if from, err = t.sourceTime(as.ID, in.From, "in", 0, false); err != nil {
				return nil, err
			}
		}
		if in.To != "" {
			if to, err = t.sourceTime(as.ID, in.To, "end", 0, false); err != nil {
				return nil, err
			}
		}
		if to <= from {
			return nil, apperr.New(apperr.InvalidInput, "The clip would end (%s) before it starts (%s).", cutsheet.Clock(to), cutsheet.Clock(from))
		}
		after, before, err := t.position(in.Position, "")
		if err != nil {
			return nil, err
		}
		id := in.Name
		if id == "" {
			names := cutsheet.NameFunc(t.spec, t.info, projectAssetIDs(ctx, a, t.project.ID))
			id = newClipID(t.spec, names(as.ID))
		} else if clipIndex(t.spec, id) >= 0 {
			return nil, apperr.New(apperr.InvalidInput, "There is already a clip called %q.", id).WithHint("Choose another name, or leave the name out.")
		}
		clip := editspec.Clip{ID: id, AssetID: as.ID, In: round3(from), Out: round3(to), Muted: in.Muted}
		op := editspec.Op{Op: "insertClip", Clip: &clip, After: after, Before: before}
		where := t.insertWhere(after, before)
		summary := fmt.Sprintf("Adds %s (%s to %s, %s) %s.", as.DisplayTitle(), cutsheet.Clock(clip.In), cutsheet.Clock(clip.Out), seconds(clip.Out-clip.In), where)
		return t.finish(ctx, a, in.CutEditCommon, "add", []editspec.Op{op}, summary, fmt.Sprintf("Add %s %s", as.DisplayTitle(), where), &CutEditOut{})
	})

	Def("cut.move", "Move a clip of the latest cut to the start, the end, next to another clip or to a place number; dryRun checks it and gives a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutMoveIn) (*CutEditOut, error) {
		t, err := loadEditTarget(ctx, a, in.CutEditCommon)
		if err != nil {
			return nil, err
		}
		i, err := t.clip(in.Clip)
		if err != nil {
			return nil, err
		}
		id := t.spec.Clips[i].ID
		after, before, err := t.position(in.Position, id)
		if err != nil {
			return nil, err
		}
		if after == id || before == id {
			return nil, apperr.New(apperr.InvalidInput, "A clip can't go next to itself.")
		}
		op := editspec.Op{Op: "moveClip", ID: id, After: after, Before: before}
		where := "to the end"
		switch {
		case before != "" && (clipIndex(t.spec, before) == 0 || (i == 0 && clipIndex(t.spec, before) == 1)):
			where = "to the start"
		case before != "":
			where = "before " + t.who(clipIndex(t.spec, before))
		case after != "" && after != lastOther(t.spec, id):
			where = "after " + t.who(clipIndex(t.spec, after))
		}
		summary := fmt.Sprintf("Moves %s %s.", t.who(i), where)
		return t.finish(ctx, a, in.CutEditCommon, "move", []editspec.Op{op}, summary, fmt.Sprintf("Move %s %s", t.who(i), where), &CutEditOut{})
	})

	Def("cut.trim", "Change where a clip of the latest cut starts or ends in its footage (a time, a change like +0.5 or quoted words), roll the cut with its neighbour, or slide its footage; dryRun checks it and gives a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutTrimIn) (*CutEditOut, error) {
		t, err := loadEditTarget(ctx, a, in.CutEditCommon)
		if err != nil {
			return nil, err
		}
		i, err := t.clip(in.Clip)
		if err != nil {
			return nil, err
		}
		c := t.spec.Clips[i]
		if in.Start == "" && in.End == "" && in.Slip == nil {
			return nil, apperr.New(apperr.InvalidInput, "Say what to change: the start, the end, or how far to slide the footage.")
		}
		if in.Slip != nil && (in.Start != "" || in.End != "" || in.Roll) {
			return nil, apperr.New(apperr.InvalidInput, "Sliding the footage keeps the start and end where they are; do it on its own.")
		}
		var ops []editspec.Op
		var said []string
		mode := ""
		if in.Roll {
			mode = editspec.TrimRoll
		}
		if in.Start != "" {
			to, err := t.sourceTime(c.AssetID, in.Start, "in", c.In, true)
			if err != nil {
				return nil, err
			}
			ops = append(ops, editspec.Op{Op: "trimClip", ID: c.ID, Edge: "in", To: &to, Mode: mode})
			said = append(said, moveWords("starts", to-c.In, to))
		}
		if in.End != "" {
			to, err := t.sourceTime(c.AssetID, in.End, "end", c.Out, true)
			if err != nil {
				return nil, err
			}
			ops = append(ops, editspec.Op{Op: "trimClip", ID: c.ID, Edge: "end", To: &to, Mode: mode})
			said = append(said, moveWords("ends", to-c.Out, to))
		}
		summary := ""
		switch {
		case in.Slip != nil:
			by := *in.Slip
			ops = append(ops, editspec.Op{Op: "slipClip", ID: c.ID, By: &by})
			dir := "later"
			if by < 0 {
				dir = "earlier"
			}
			summary = fmt.Sprintf("%s shows its footage from %s %s (same length and place).", upperFirst(t.who(i)), seconds(math.Abs(by)), dir)
		case in.Roll:
			summary = fmt.Sprintf("The cut moves: %s %s, and the clip next to it moves with it so the video keeps its length.", t.who(i), strings.Join(said, " and "))
		default:
			summary = fmt.Sprintf("%s %s.", upperFirst(t.who(i)), strings.Join(said, " and "))
		}
		return t.finish(ctx, a, in.CutEditCommon, "trim", ops, summary, "Trim "+t.who(i), &CutEditOut{})
	})

	Def("cut.split", "Cut a clip of the latest cut in two, at a time on the video's timeline or at a time or words in a clip's footage; dryRun checks it and gives a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutSplitIn) (*CutEditOut, error) {
		t, err := loadEditTarget(ctx, a, in.CutEditCommon)
		if err != nil {
			return nil, err
		}
		var i int
		var at float64
		if in.Clip != "" {
			if i, err = t.clip(in.Clip); err != nil {
				return nil, err
			}
			if at, err = t.sourceTime(t.spec.Clips[i].AssetID, in.At, "in", 0, false); err != nil {
				return nil, err
			}
		} else {
			tl, err := timelineTime(in.At)
			if err != nil {
				return nil, err
			}
			if i, at, err = clipAtTimeline(t.spec, tl); err != nil {
				return nil, err
			}
		}
		c := t.spec.Clips[i]
		name := in.Name
		if name == "" {
			name = editspec.PartID(t.spec, c.ID)
		}
		op := editspec.Op{Op: "splitClip", ID: c.ID, At: &at, NewID: name}
		summary := fmt.Sprintf("Splits %s in two at %s of its footage; the second part is called %s.", t.who(i), cutsheet.Clock(round3(at)), name)
		return t.finish(ctx, a, in.CutEditCommon, "split", []editspec.Op{op}, summary, "Split "+t.who(i), &CutEditOut{})
	})

	Def("cut.delete", "Remove clips from the latest cut (by name or number); the rest closes up; dryRun checks it and gives a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutDeleteIn) (*CutEditOut, error) {
		t, err := loadEditTarget(ctx, a, in.CutEditCommon)
		if err != nil {
			return nil, err
		}
		if len(in.Clips) == 0 {
			return nil, apperr.New(apperr.InvalidInput, "Say which clips to remove.")
		}
		var ops []editspec.Op
		var who []string
		seen := map[string]bool{}
		for _, ref := range in.Clips {
			i, err := t.clip(ref)
			if err != nil {
				return nil, err
			}
			if id := t.spec.Clips[i].ID; !seen[id] {
				seen[id] = true
				ops = append(ops, editspec.Op{Op: "removeClip", ID: id})
				who = append(who, t.who(i))
			}
		}
		if len(ops) >= len(t.spec.Clips) {
			return nil, apperr.New(apperr.InvalidInput, "That would remove every clip; a cut needs at least one.")
		}
		summary := fmt.Sprintf("Removes %s.", joinAnd(who))
		return t.finish(ctx, a, in.CutEditCommon, "delete", ops, summary, "Remove "+joinAnd(who), &CutEditOut{})
	})

	Def("cut.deleteRange", "Remove a stretch of the latest cut's timeline (from one time in the video to another, across clips); the rest closes up; dryRun checks it and gives a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutDeleteRangeIn) (*CutEditOut, error) {
		t, err := loadEditTarget(ctx, a, in.CutEditCommon)
		if err != nil {
			return nil, err
		}
		from, err := timelineTime(in.From)
		if err != nil {
			return nil, err
		}
		to, err := timelineTime(in.To)
		if err != nil {
			return nil, err
		}
		if to <= from {
			return nil, apperr.New(apperr.InvalidInput, "The stretch must end (%s) after it starts (%s).", cutsheet.Clock(to), cutsheet.Clock(from))
		}
		total := t.length()
		if from >= total {
			return nil, apperr.New(apperr.InvalidInput, "The video is only %s long.", seconds(total))
		}
		to = math.Min(to, total)
		op := editspec.Op{Op: "deleteRange", From: &from, To: &to}
		summary := fmt.Sprintf("Cuts out %s to %s of the video (%s); what comes after moves up.", cutsheet.Clock(round3(from)), cutsheet.Clock(round3(to)), seconds(to-from))
		return t.finish(ctx, a, in.CutEditCommon, "delete-range", []editspec.Op{op}, summary, fmt.Sprintf("Cut out %s to %s", cutsheet.Clock(round3(from)), cutsheet.Clock(round3(to))), &CutEditOut{})
	})

	Def("cut.removeWords", "Take words out of the latest cut, cutting in the pauses around them: quoted words, a selection of a clip's words, or suggested ums, repeats and false starts (cleanup); dryRun checks it (and lists suggestions) with a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutRemoveWordsIn) (*CutEditOut, error) {
		t, err := loadEditTarget(ctx, a, in.CutEditCommon)
		if err != nil {
			return nil, err
		}
		tx := t.facts.Transcript
		out := &CutEditOut{}
		var picked []cutsheet.WordRemoval
		clipFilter := ""
		if in.Clip != "" {
			i, err := t.clip(in.Clip)
			if err != nil {
				return nil, err
			}
			clipFilter = t.spec.Clips[i].ID
		}
		switch {
		case in.Cleanup:
			all := cutsheet.Cleanups(t.spec, tx)
			if clipFilter != "" {
				var keep []cutsheet.WordRemoval
				for _, r := range all {
					if r.ClipID == clipFilter {
						keep = append(keep, r)
					}
				}
				all = keep
			}
			if len(all) == 0 {
				// Nothing to suggest is an answer, not a failure.
				raw, _ := json.Marshal(editspec.Plan{BaseRevisionID: t.rev.ID})
				d := round3(t.length())
				return &CutEditOut{ProjectID: t.project.ID, Branch: t.branch, Summary: "No ums, repeated words or false starts found in what the cut says.",
					Changes: []string{}, DurationBefore: d, Duration: d, NoChange: true, Problems: []cutsheet.Problem{}, Removals: []cutsheet.WordRemoval{}, Plan: raw}, nil
			}
			out.Removals = all
			picked = all
			if len(in.Pick) > 0 {
				picked = nil
				for _, n := range in.Pick {
					if n < 1 || n > len(all) {
						return nil, apperr.New(apperr.InvalidInput, "There are suggestions 1 to %d, not %d.", len(all), n)
					}
					picked = append(picked, all[n-1])
				}
			}
		case in.From != nil:
			if clipFilter == "" || in.To == nil {
				return nil, apperr.New(apperr.InvalidInput, "A stretch of words needs the clip, from and to.")
			}
			r, ok := cutsheet.RemovalFor(t.spec, tx, clipFilter, *in.From, *in.To)
			if !ok {
				return nil, apperr.New(apperr.NotFound, "No words are said there.").WithHint("Pick words in the transcript.")
			}
			picked = []cutsheet.WordRemoval{r}
		case strings.TrimSpace(in.Words) != "":
			hits, closest := cutsheet.FindWords(t.spec, tx, strings.Trim(strings.TrimSpace(in.Words), "\"“”"), clipFilter)
			switch {
			case len(hits) == 0 && closest != "":
				return nil, apperr.New(apperr.NotFound, "“%s” isn't said in the cut; the closest is “%s”.", in.Words, closest).WithHint("Use the words as the transcript has them.")
			case len(hits) == 0:
				return nil, apperr.New(apperr.NotFound, "“%s” isn't said in the cut.", in.Words).WithHint("Words are found in the transcripts of the clips the cut plays.")
			case in.All:
				picked = hits
			case in.Which > 0:
				if in.Which > len(hits) {
					return nil, apperr.New(apperr.InvalidInput, "“%s” is said %d times, not %d.", in.Words, len(hits), in.Which)
				}
				picked = []cutsheet.WordRemoval{hits[in.Which-1]}
			case len(hits) > 1:
				var where []string
				for n, h := range hits {
					where = append(where, fmt.Sprintf("%d) at %s in clip %d: “…%s %s %s…”", n+1, cutsheet.Clock(math.Round(h.At*10)/10), h.Clip, h.Before, h.Words, h.After))
				}
				return nil, apperr.New(apperr.InvalidInput, "“%s” is said %d times: %s.", in.Words, len(hits), strings.Join(where, "; ")).
					WithDetails(map[string]any{"matches": hits}).WithHint("Say which one (which: 1, 2…) or take out all of them (all).")
			default:
				picked = hits
			}
		default:
			return nil, apperr.New(apperr.InvalidInput, "Say which words to take out, or ask for suggestions (cleanup).")
		}
		ops, err := cutsheet.RemovalOps(t.spec, picked, in.AllowUnlock)
		if err != nil {
			var le *editspec.LockedError
			if errors.As(err, &le) {
				return nil, t.lockedErr(le)
			}
			return nil, apperr.New(apperr.InvalidInput, "%s", plainOpError(err))
		}
		if !in.Cleanup {
			out.Removals = picked
		}
		total := 0.0
		var quoted []string
		for _, r := range picked {
			total += r.Seconds
			if len(quoted) < 4 {
				quoted = append(quoted, "“"+r.Words+"”")
			}
		}
		what := strings.Join(quoted, ", ")
		if len(picked) > len(quoted) {
			what += fmt.Sprintf(" and %d more", len(picked)-len(quoted))
		}
		summary := fmt.Sprintf("Takes out %s (%s in all), cutting in the pauses around the words.", what, seconds(total))
		instruction := "Take out " + what
		if in.Cleanup {
			instruction = fmt.Sprintf("Remove ums and restarts (%d)", len(picked))
		}
		return t.finish(ctx, a, in.CutEditCommon, "remove-words", ops, summary, instruction, out)
	})

	Def("cut.pauses", "Shorten long pauses between words in the latest cut (longer than one length, down to another), anywhere a clip has words; a dry run lists them to hear first, saving takes all or those picked (pick) with a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutPausesIn) (*CutEditOut, error) {
		longer, keep, err := pauseLengths(in.LongerThan, in.To)
		if err != nil {
			return nil, err
		}
		t, err := loadEditTarget(ctx, a, in.CutEditCommon)
		if err != nil {
			return nil, err
		}
		all := cutsheet.Pauses(t.spec, t.facts.Transcript, longer, keep)
		if in.Clip != "" {
			i, err := t.clip(in.Clip)
			if err != nil {
				return nil, err
			}
			var only []cutsheet.WordRemoval
			for _, r := range all {
				if r.ClipID == t.spec.Clips[i].ID {
					only = append(only, r)
				}
			}
			all = only
		}
		which := fmt.Sprintf("longer than %s", seconds(longer))
		if len(all) == 0 {
			// Nothing to shorten is an answer, not a failure.
			raw, _ := json.Marshal(editspec.Plan{BaseRevisionID: t.rev.ID})
			d := round3(t.length())
			return &CutEditOut{ProjectID: t.project.ID, Branch: t.branch, Summary: fmt.Sprintf("No pauses %s between words in what the cut says.", which),
				Changes: []string{}, DurationBefore: d, Duration: d, NoChange: true, Problems: []cutsheet.Problem{}, Removals: []cutsheet.WordRemoval{}, Plan: raw}, nil
		}
		picked := all
		if len(in.Pick) > 0 {
			picked = nil
			seen := map[int]bool{}
			for _, n := range in.Pick {
				if n < 1 || n > len(all) {
					return nil, apperr.New(apperr.InvalidInput, "There are pauses 1 to %d, not %d.", len(all), n)
				}
				if !seen[n] {
					seen[n] = true
					picked = append(picked, all[n-1])
				}
			}
		}
		ops, err := cutsheet.RemovalOps(t.spec, picked, in.AllowUnlock)
		if err != nil {
			var le *editspec.LockedError
			if errors.As(err, &le) {
				return nil, t.lockedErr(le)
			}
			return nil, apperr.New(apperr.InvalidInput, "%s", plainOpError(err))
		}
		total := 0.0
		for _, r := range picked {
			total += r.Seconds
		}
		what := count(len(picked), "pause", "pauses")
		if len(picked) < len(all) {
			what = fmt.Sprintf("%d of the %d pauses", len(picked), len(all))
		}
		summary := fmt.Sprintf("Shortens %s %s to %s each (%s shorter in all).", what, which, seconds(keep), seconds(total))
		out := &CutEditOut{Removals: all}
		return t.finish(ctx, a, in.CutEditCommon, "pauses", ops, summary, fmt.Sprintf("Shorten %s %s to %s", what, which, seconds(keep)), out)
	})
}

// pauseLengths checks the lengths `cut pauses` takes: pauses longer than
// longer seconds shortened to keep seconds (defaults 1 and 0.5).
func pauseLengths(longer, keep float64) (float64, float64, error) {
	if longer == 0 {
		longer = cutsheet.PauseLongerThan
	}
	if keep == 0 {
		keep = math.Min(cutsheet.PauseTo, longer/2)
	}
	switch {
	case longer < 0.2 || longer > 30:
		return 0, 0, apperr.New(apperr.InvalidInput, "Choose pauses longer than something between 0.2 and 30 seconds, not %s.", seconds(longer))
	case keep < 0.1:
		return 0, 0, apperr.New(apperr.InvalidInput, "Keep at least 0.1 s of each pause, so the words don't run together.")
	case keep >= longer:
		return 0, 0, apperr.New(apperr.InvalidInput, "Shorten pauses to less than they are: %s is not shorter than %s.", seconds(keep), seconds(longer))
	}
	return longer, keep, nil
}

// clipAtTimeline is the clip seen at timeline time tl (seconds) and the moment
// of its footage shown there.
func clipAtTimeline(s *editspec.Spec, tl float64) (int, float64, error) {
	n := editspec.Clone(s)
	editspec.Normalize(n)
	fps := float64(n.Output.FPS)
	placed, total := editspec.Timeline(n)
	f := int(math.Round(tl * fps))
	if f <= 0 || f >= total {
		return 0, 0, apperr.New(apperr.InvalidInput, "%s is not inside the video (it runs 0:00 to %s).", cutsheet.Clock(round3(tl)), cutsheet.Clock(round3(float64(total)/fps)))
	}
	// The clip that is on screen (overlaps belong to the incoming clip from their middle).
	idx := 0
	for i, pc := range placed {
		start := pc.From
		if i > 0 && editspec.IsOverlapTransition(pc.TransitionIn.Type) {
			start += pc.TransitionIn.DurationInFrames / 2
		}
		if f >= start {
			idx = i
		}
	}
	pc := placed[idx]
	if f == pc.From {
		return 0, 0, apperr.New(apperr.InvalidInput, "%s is exactly where a clip starts; there is nothing to split there.", cutsheet.Clock(round3(tl)))
	}
	at := pc.SourceIn + float64(f-pc.From)/fps*pc.PlaybackRate
	return idx, round3(at), nil
}

func lastOther(s *editspec.Spec, id string) string {
	for i := len(s.Clips) - 1; i >= 0; i-- {
		if s.Clips[i].ID != id {
			return s.Clips[i].ID
		}
	}
	return ""
}

// moveWords says how an edge moved: "now starts 0.5 s later (at 0:12.4 in its footage)".
func moveWords(verb string, delta, at float64) string {
	switch {
	case math.Abs(delta) < 0.0005:
		return fmt.Sprintf("still %s at %s", verb, cutsheet.Clock(round3(at)))
	case delta > 0:
		return fmt.Sprintf("now %s %s later (at %s in its footage)", verb, seconds(delta), cutsheet.Clock(round3(at)))
	}
	return fmt.Sprintf("now %s %s earlier (at %s in its footage)", verb, seconds(-delta), cutsheet.Clock(round3(at)))
}

func seconds(v float64) string {
	v = math.Round(v*100) / 100
	return strconv.FormatFloat(v, 'f', -1, 64) + " s"
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("cut.removeWords", Offering{ID: "remove-ums", Group: "edit", Title: "Remove ums", Input: map[string]any{"cleanup": true},
		Does:  "Suggests the ums, repeats and false starts to take out of the cut, each heard first.",
		Words: []string{"filler words", "uh", "stutters"}, Needs: Needs{Works: []string{"transcripts", "editing"}}})
	Offer("cut.pauses", Offering{ID: "tighten-pauses", Group: "edit", Title: "Tighten pauses",
		Does:  "Shortens long pauses between words anywhere in the cut, each one heard first.",
		Words: []string{"dead air", "long silences", "tighten the pacing"}, Needs: Needs{Works: []string{"transcripts", "editing"}}})
}
