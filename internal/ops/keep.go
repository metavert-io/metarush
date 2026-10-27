package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/keep"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// What a project keeps (D166, docs/keeping.md): keep.add keeps one thing a
// conversation brought in, through the ordinary ops (a source, a logo and the
// brand kit, the brief, a rule or check, a person's note, a sign-off, an idea
// for MetaRush, a spelling), and records it so it is never offered again.
// Claude's `metarush keep …` commands become "Keep this?" cards that run it.

// KeepAddIn keeps one thing.
type KeepAddIn struct {
	ProjectID string            `json:"projectId,omitempty" jsonschema:"description=The project it is kept in (people's notes and the improvement list are the workspace's)."`
	Kind      string            `json:"kind" jsonschema:"enum=source,enum=picture,enum=brief,enum=rule,enum=promise,enum=person,enum=signoff,enum=check,enum=improve,enum=spelling"`
	Text      string            `json:"text,omitempty" jsonschema:"maxLength=600,description=The thing in plain words: the rule\\, the sign-off\\, the fact\\, the word\\, what MetaRush couldn't do\\, the address."`
	Detail    models.KeepDetail `json:"detail,omitempty"`
	// ConversationID is the conversation it came up in (set by the card).
	ConversationID string `json:"conversationId,omitempty"`
	From           string `json:"from,omitempty" jsonschema:"enum=,enum=metarush,enum=claude,enum=person"`
	// Where a rule, check or sign-off is kept (D193, D200): just this project
	// (the default), or for every project using the Content Settings it uses
	// (a house rule, a house sign-off).
	Where string `json:"where,omitempty" jsonschema:"enum=,enum=project,enum=settings,description=rule\\, check\\, promise or sign-off: just this project (default)\\, or settings: a house rule\\, promise or sign-off of the Content Settings it uses (every project using them follows it)."`
}

// KeepSettings is whose Content Settings a project uses, for the words of a
// keep card (D193): a spelling and a brand logo go there.
type KeepSettings struct {
	// Owner is the project whose settings they are ("" none set up).
	Owner     string
	OwnerName string
	// Own: they are the project's own.
	Own bool
	// Others counts the other projects using them.
	Others int
}

// KeepSettingsOf is whose Content Settings a project uses.
func KeepSettingsOf(ctx context.Context, a *app.App, projectID string) KeepSettings {
	g, r, err := content.Resolve(ctx, a, projectID)
	if err != nil || r.Owner == "" {
		return KeepSettings{}
	}
	st := KeepSettings{Owner: r.Owner, OwnerName: g.Name(r.Owner), Own: r.Owner == projectID}
	for _, id := range g.UsedBy(r.Owner) {
		if id != projectID {
			st.Others++
		}
	}
	return st
}

// settingsWords names a project's settings in a card: "this project's
// settings" or "“fernbrook”'s settings", with who else uses them.
func (st KeepSettings) words() string {
	s := "“" + st.OwnerName + "”'s settings"
	if st.Own {
		s = "this project's settings"
	}
	if st.Others > 0 {
		s += " (also used by " + pluralWord(st.Others, "project") + ")"
	}
	return s
}

// KeepAddOut is what keeping did.
type KeepAddOut struct {
	Item    models.KeepItem `json:"item"`
	Already bool            `json:"already" jsonschema:"description=It was kept before; nothing changed."`
	Summary string          `json:"summary"`
	// Jobs are work it started (a snapshot, a download, an import).
	Jobs []string `json:"jobs,omitempty"`
	// RevisionID is a version it saved (the brand kit put on the latest cut).
	RevisionID string `json:"revisionId,omitempty"`
}

// KeepListIn names a project.
type KeepListIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=Project ID or its exact name."`
	State     string `json:"state,omitempty" jsonschema:"enum=,enum=asked,enum=kept,enum=declined"`
}

// KeepListOut is what a project's conversations offered to keep.
type KeepListOut struct {
	ProjectID string            `json:"projectId"`
	Items     []models.KeepItem `json:"items"`
	Summary   string            `json:"summary"`
}

// KeepDeclineIn declines a thing: it is never offered again in the project.
type KeepDeclineIn struct {
	ProjectID string            `json:"projectId,omitempty"`
	Item      string            `json:"item,omitempty" jsonschema:"description=The record (kep_…)\\, or kind and text."`
	Kind      string            `json:"kind,omitempty" jsonschema:"enum=,enum=source,enum=picture,enum=brief,enum=rule,enum=promise,enum=person,enum=signoff,enum=check,enum=improve,enum=spelling"`
	Text      string            `json:"text,omitempty"`
	Detail    models.KeepDetail `json:"detail,omitempty"`
}

// projectKinds are kinds kept in a project (the others are the workspace's).
var projectKinds = map[string]bool{models.KeepSource: true, models.KeepPicture: true, models.KeepBrief: true, models.KeepRule: true,
	models.KeepSignoff: true, models.KeepCheck: true, models.KeepSpelling: true, models.KeepPromise: true}

func validKind(k string) bool {
	for _, x := range models.KeepKinds {
		if x == k {
			return true
		}
	}
	return false
}

// KeepScope is the project a kind is kept in ("" for the workspace's kinds).
func KeepScope(kind, projectID string) string {
	if projectKinds[kind] {
		return projectID
	}
	return ""
}

// callOp runs another op as the same caller, decoding its output.
func callOp[T any](ctx context.Context, a *app.App, name string, in any) (*T, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	out, err := Call(ctx, a, name, b, CallerFrom(ctx))
	if err != nil {
		return nil, err
	}
	t, ok := out.(*T)
	if !ok {
		return nil, fmt.Errorf("%s returned %T", name, out)
	}
	return t, nil
}

// callRaw runs another op as the same caller, returning its output as JSON.
func callRaw(ctx context.Context, a *app.App, name string, in any) (map[string]any, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	out, err := Call(ctx, a, name, b, CallerFrom(ctx))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	ob, _ := json.Marshal(out)
	_ = json.Unmarshal(ob, &m)
	return m, nil
}

// KeepDescribe says what keeping a thing does, in plain words (cards, the CLI);
// st is whose Content Settings the project uses (a spelling and a brand logo go there).
func KeepDescribe(kind, text string, d models.KeepDetail, project string, st KeepSettings) string {
	q := func(s string) string { return "“" + strings.TrimSpace(s) + "”" }
	in := ""
	if project != "" {
		in = " of " + project
	}
	switch kind {
	case models.KeepSource:
		return "Adds " + linkWords(orDefault(d.URL, text)) + " to the sources" + in + "."
	case models.KeepPicture:
		if d.BrandKit || d.FromSite != "" {
			if st.Owner != "" && !st.Own {
				// A logo is one client's: the project gets settings of its own rather than changing others' brand.
				return "Keeps the logo in the sources" + in + " and makes it the logo of its own brand, with its colours (it gets settings of its own, starting as a copy of “" +
					st.OwnerName + "”'s): the graphics use them from now on."
			}
			return "Keeps the logo in the sources" + in + " and makes it the brand's logo, with its colours: the graphics use them from now on."
		}
		if d.File != nil && d.File.MIME == "application/pdf" {
			return "Keeps the PDF in the sources" + in + " (each page as a picture graphics can show)."
		}
		return "Keeps the picture in the sources" + in + "."
	case models.KeepBrief:
		var parts []string
		if d.Goal != "" {
			parts = append(parts, "the goal "+q(d.Goal))
		}
		if d.Audience != "" {
			parts = append(parts, "who it's for "+q(d.Audience))
		}
		if d.Tone != "" {
			parts = append(parts, "the tone "+q(d.Tone))
		}
		if len(d.Deliverables) > 0 {
			var ds []string
			for _, dl := range deliverablesOf(d.Deliverables) {
				ds = append(ds, dl.Name+" ("+map[string]string{"landscape-1080p": "widescreen", "vertical-1080p": "vertical", "square-1080": "square"}[dl.Preset]+")")
			}
			parts = append(parts, "where it goes: "+strings.Join(ds, ", "))
		}
		return "Puts " + joinPlainAnd(parts) + " in the brief" + in + "."
	case models.KeepRule:
		s := "Adds the rule " + q(text) + " to" + strings.TrimPrefix(in, " of") + "'s rules; Claude follows it in every conversation."
		if in == "" {
			s = "Adds the rule " + q(text) + " to the project's rules; Claude follows it in every conversation."
		}
		if w := MeasureWords(orDefault(d.Measure, MeasureFor(text))); w != "" {
			s += " The quality check checks that " + w + "."
		}
		return s
	case models.KeepPromise:
		w := MeasureWords(orDefault(d.Measure, MeasureFor(text)))
		if w == "" {
			return "MetaRush can't measure " + q(text) + ", so it can't keep it as a promise: keep it as a sign-off instead."
		}
		s := "Keeps " + q(text) + " as a promise made to the client" + in + ": every quality check measures that " + w + ", and a version that breaks it waits before it goes out (a person can still send it out anyway with a note)."
		return s
	case models.KeepCheck:
		s := "Checks for " + q(text) + " in every quality check" + in + "."
		if w := MeasureWords(orDefault(d.Measure, MeasureFor(text))); w != "" {
			s += " MetaRush measures it: " + w + "."
		}
		return s
	case models.KeepPerson:
		return "Adds " + q(text) + " to " + orDefault(d.Person, "the person") + "'s notes in People."
	case models.KeepSignoff:
		return "Adds the sign-off " + q(text) + " to the cut" + in + ": a reminder before it is approved, exported or delivered."
	case models.KeepImprove:
		return "Adds " + q(text) + " to MetaRush's improvement list."
	case models.KeepSpelling:
		if st.Owner != "" {
			return "Puts " + q(text) + " on the spelling list of " + st.words() + ", so transcripts write it that way."
		}
		return "Puts " + q(text) + " on the spelling list" + in + ", so transcripts write it that way."
	}
	return ""
}

func linkWords(u string) string {
	k := keep.LinkKey(u)
	if i := strings.IndexAny(k, "/?"); i > 0 && len(k) > 40 {
		return k[:i]
	}
	return k
}

// deliverablesOf fills in each deliverable's shape from its words.
func deliverablesOf(ds []models.Deliverable) []models.Deliverable {
	out := make([]models.Deliverable, 0, len(ds))
	for _, d := range ds {
		d.Name = strings.TrimSpace(d.Name)
		if d.Name == "" {
			continue
		}
		if d.Preset == "" || d.Platform == "" {
			preset, platform := DeliverableShape(d.Name)
			if d.Preset == "" {
				d.Preset = preset
			}
			if d.Platform == "" {
				d.Platform = platform
			}
		}
		out = append(out, d)
	}
	return out
}

// DeliverableShape is the shape and platform a place needs, from its words:
// email, a website, YouTube or a presentation widescreen; shorts, reels,
// TikTok, stories or social media vertical; a square feed square.
func DeliverableShape(name string) (preset, platform string) {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "square"):
		return "square-1080", "generic"
	case strings.Contains(n, "tiktok"):
		return "vertical-1080p", "tiktok"
	case strings.Contains(n, "reel") || strings.Contains(n, "instagram") || strings.Contains(n, "stories"):
		return "vertical-1080p", "reels"
	case strings.Contains(n, "short") || strings.Contains(n, "social") || strings.Contains(n, "vertical") || strings.Contains(n, "phone"):
		return "vertical-1080p", "shorts"
	case strings.Contains(n, "youtube"):
		return "landscape-1080p", "youtube"
	}
	return "landscape-1080p", "generic"
}

// mergeBrief adds what a keep card brings to a brief: fields given replace
// empty or different ones, deliverables are added (one per name).
func mergeBrief(b models.Brief, d models.KeepDetail) models.Brief {
	if d.Goal != "" {
		b.Goal = d.Goal
	}
	if d.Audience != "" {
		b.Audience = d.Audience
	}
	if d.Tone != "" {
		b.Tone = d.Tone
	}
	have := map[string]bool{}
	for _, x := range b.Deliverables {
		have[keep.Norm(x.Name)] = true
	}
	for _, x := range deliverablesOf(d.Deliverables) {
		if !have[keep.Norm(x.Name)] {
			have[keep.Norm(x.Name)] = true
			b.Deliverables = append(b.Deliverables, x)
		}
	}
	platforms := map[string]bool{}
	for _, p := range b.Platforms {
		platforms[p] = true
	}
	for _, x := range b.Deliverables {
		if x.Platform != "" && x.Platform != "generic" && !platforms[x.Platform] {
			platforms[x.Platform] = true
			b.Platforms = append(b.Platforms, x.Platform)
		}
	}
	return b
}

// KeepAdd keeps one thing (D166) and records it as kept.
func KeepAdd(ctx context.Context, a *app.App, in KeepAddIn) (*KeepAddOut, error) {
	if !validKind(in.Kind) {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush doesn't know how to keep %q.", in.Kind).WithHint("Keep a source, picture, brief, rule, promise, person, signoff, check, improve or spelling.")
	}
	in.Text = strings.TrimSpace(in.Text)
	var p *models.Project
	if projectKinds[in.Kind] {
		if in.ProjectID == "" {
			return nil, invalid("Say which project to keep it in.")
		}
		var err error
		if p, err = resolveProject(ctx, a, in.ProjectID); err != nil {
			return nil, err
		}
		in.ProjectID = p.ID
	} else {
		in.ProjectID = ""
	}
	it := models.KeepItem{ProjectID: in.ProjectID, Kind: in.Kind, Text: in.Text, Detail: in.Detail, From: orDefault(in.From, "person"), ConversationID: in.ConversationID}
	it.Key = keep.Key(it.Kind, it.Text, it.Detail)
	if prev, err := keep.Find(ctx, a.DB, a.WorkspaceID(), it.ProjectID, it.Key); err == nil && prev.State == models.KeepKept {
		return &KeepAddOut{Item: *prev, Already: true, Summary: "That is already kept: " + lowerFirst(orDefault(prev.Outcome, "nothing changed."))}, nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	out := &KeepAddOut{}
	var outcome string
	switch in.Kind {
	case models.KeepSource:
		link := orDefault(in.Detail.URL, in.Text)
		if link == "" {
			return nil, invalid("Give the address to add.")
		}
		req := map[string]any{"urls": []string{link}, "projectId": p.ID}
		if in.Detail.As == "video" || in.Detail.As == "page" {
			req["as"] = in.Detail.As
		}
		res, err := callRaw(ctx, a, "add.links", req)
		if err != nil {
			return nil, err
		}
		outcome, _ = res["summary"].(string)
		out.Jobs = jobIDsIn(res)
	case models.KeepPicture:
		var err error
		if outcome, err = keepPicture(ctx, a, p, in, out); err != nil {
			return nil, err
		}
	case models.KeepBrief:
		b := mergeBrief(p.Brief, in.Detail)
		if _, err := callOp[ProjectOut](ctx, a, "project.update", ProjectUpdateIn{ID: p.ID, Brief: &b}); err != nil {
			return nil, err
		}
		outcome = "Kept in the brief of " + p.Name + "."
	case models.KeepPromise:
		// A promise made to the client (D301): a measured rule that holds delivery when broken.
		if in.Text == "" {
			return nil, invalid("Say the promise in a few words.")
		}
		if in.Where == "settings" {
			st := KeepSettingsOf(ctx, a, p.ID)
			if st.Owner == "" {
				return nil, invalid("%s uses no project's settings yet, so the promise can only be kept for the project.", p.Name)
			}
			r, _, err := AddHouseRule(ctx, a, st.Owner, HouseRuleAddIn{Text: in.Text, Measure: in.Detail.Measure, Promise: true, From: fromWords(in)})
			if err != nil {
				return nil, err
			}
			outcome = "Kept the promise “" + r.Text + "” for every project using " + strings.TrimPrefix(st.words(), "this project's settings") + ": every quality check measures it."
			if st.Own {
				outcome = "Kept the promise “" + r.Text + "” in " + p.Name + "'s settings: every quality check of every project using them measures it."
			}
			break
		}
		r, err := AddRule(ctx, a, p, RuleAddIn{ProjectID: p.ID, Text: in.Text, Measure: in.Detail.Measure, Promise: true, From: fromWords(in)})
		if err != nil {
			return nil, err
		}
		outcome = "Kept the promise “" + r.Rule.Text + "” for " + p.Name + ": every quality check measures it, and a version that breaks it waits before it goes out."
	case models.KeepRule, models.KeepCheck:
		if in.Text == "" {
			return nil, invalid("Say the rule in a few words.")
		}
		if in.Where == "settings" {
			// A house rule of the settings the project uses (D193): the person chose it.
			st := KeepSettingsOf(ctx, a, p.ID)
			if st.Owner == "" {
				return nil, invalid("%s uses no project's settings yet, so the rule can only be kept for the project.", p.Name)
			}
			r, _, err := AddHouseRule(ctx, a, st.Owner, HouseRuleAddIn{Text: in.Text, Check: in.Kind == models.KeepCheck, Measure: in.Detail.Measure, From: fromWords(in)})
			if err != nil {
				return nil, err
			}
			outcome = "Added the house rule “" + r.Text + "” to " + strings.TrimPrefix(st.words(), "this project's settings") + "; every project using them follows it."
			if st.Own {
				outcome = "Added the house rule “" + r.Text + "” to " + p.Name + "'s settings; every project using them follows it."
			}
			break
		}
		r, err := AddRule(ctx, a, p, RuleAddIn{ProjectID: p.ID, Text: in.Text, Check: in.Kind == models.KeepCheck, Measure: in.Detail.Measure, From: fromWords(in)})
		if err != nil {
			return nil, err
		}
		outcome = "Added the rule “" + r.Rule.Text + "” to " + p.Name + "."
		if in.Kind == models.KeepCheck {
			outcome = "Every quality check of " + p.Name + " now checks for “" + r.Rule.Text + "”."
		}
	case models.KeepPerson:
		who := strings.TrimSpace(in.Detail.Person)
		if who == "" || in.Text == "" {
			return nil, invalid("Say who it is about and what to note.")
		}
		person, err := resolvePerson(ctx, a, who)
		if err != nil {
			return nil, err
		}
		notes := strings.TrimSpace(person.Notes)
		if !strings.Contains(strings.ToLower(notes), strings.ToLower(in.Text)) {
			if notes != "" {
				notes += "\n"
			}
			notes += in.Text
		}
		if len([]rune(notes)) > 2000 {
			return nil, apperr.New(apperr.InvalidInput, "%s's notes are full (2,000 letters); shorten them in People first.", person.Name)
		}
		if _, err := callOp[PersonOut](ctx, a, "people.update", PersonUpdateIn{ID: person.ID, Notes: &notes}); err != nil {
			return nil, err
		}
		outcome = "Added it to " + person.Name + "'s notes."
	case models.KeepSignoff:
		if in.Text == "" {
			return nil, invalid("Say who must OK what.")
		}
		if in.Where == "settings" {
			// A house sign-off of the settings the project uses (D200): the person chose it.
			st := KeepSettingsOf(ctx, a, p.ID)
			if st.Owner == "" {
				return nil, invalid("%s uses no project's settings yet, so the sign-off can only be kept for the project.", p.Name)
			}
			s, _, err := AddHouseSignoff(ctx, a, st.Owner, in.Text)
			if err != nil {
				return nil, err
			}
			outcome = "Added the house sign-off “" + s.Text + "” to " + strings.TrimPrefix(st.words(), "this project's settings") + "; every project using them waits for it before a video goes out."
			if st.Own {
				outcome = "Added the house sign-off “" + s.Text + "” to " + p.Name + "'s settings; every project using them waits for it before a video goes out."
			}
			break
		}
		if _, _, err := AddSignoff(ctx, a, p, "", in.Text); err != nil {
			return nil, err
		}
		outcome = "Added the sign-off “" + in.Text + "” to " + p.Name + "."
	case models.KeepImprove:
		if in.Text == "" {
			return nil, invalid("Say what MetaRush couldn't do.")
		}
		res, err := callRaw(ctx, a, "improve.add", map[string]any{"title": in.Text, "description": in.Detail.Why, "from": "a conversation"})
		if err != nil {
			return nil, err
		}
		outcome, _ = res["summary"].(string)
	case models.KeepSpelling:
		if in.Text == "" {
			return nil, invalid("Say how the word is written.")
		}
		// The spelling list of the settings the project uses (D193).
		st := KeepSettingsOf(ctx, a, p.ID)
		owner := p.ID
		if st.Owner != "" {
			owner = st.Owner
		}
		req := map[string]any{"projectId": owner, "word": in.Text}
		if len(in.Detail.HeardAs) > 0 {
			req["heardAs"] = in.Detail.HeardAs
		}
		if _, err := callRaw(ctx, a, "transcript.spelling.add", req); err != nil {
			return nil, err
		}
		outcome = "Put “" + in.Text + "” on the spelling list of " + p.Name + "."
		if st.Owner != "" && !st.Own {
			outcome = "Put “" + in.Text + "” on the spelling list of " + st.words() + "."
		}
	}
	if outcome == "" {
		outcome = "Kept."
	}
	kept, err := keep.Decide(ctx, a.DB, a.WorkspaceID(), it, models.KeepKept, a.Actor, outcome)
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "keep.add", "keep", kept.ID, in.ProjectID, map[string]any{"kind": in.Kind, "conversation": in.ConversationID})
	out.Item, out.Summary = *kept, outcome
	return out, nil
}

func fromWords(in KeepAddIn) string {
	if in.ConversationID != "" {
		return "a conversation"
	}
	return ""
}

// jobIDsIn finds the jobs an op started in its output (add.links).
func jobIDsIn(m map[string]any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				if (k == "jobId" || k == "jobID") && vv != nil {
					if s, ok := vv.(string); ok && s != "" {
						out = append(out, s)
					}
				}
				if k == "jobs" || k == "jobIds" {
					if arr, ok := vv.([]any); ok {
						for _, j := range arr {
							switch jj := j.(type) {
							case string:
								out = append(out, jj)
							case map[string]any:
								if id, ok := jj["id"].(string); ok {
									out = append(out, id)
								}
							}
						}
						continue
					}
				}
				walk(vv)
			}
		case []any:
			for _, vv := range x {
				walk(vv)
			}
		}
	}
	walk(m)
	return dedupeStrings(out)
}

func dedupeStrings(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// keepPicture keeps a picture in the project's resources (a file dropped into
// the conversation is added; a logo a snapshot found is there already) and,
// when asked, makes it the project's brand kit logo (D165).
func keepPicture(ctx context.Context, a *app.App, p *models.Project, in KeepAddIn, out *KeepAddOut) (string, error) {
	resID := in.Detail.ResourceID
	name := ""
	switch {
	case resID != "":
		// A picture the project can use already (by its name or ID).
		view, _ := projectlinks.Load(ctx, a, p.ID)
		e, err := resources.Find(ctx, a, p.ID, view, resID)
		if err != nil {
			return "", apperr.New(apperr.NotFound, "MetaRush can't find a picture called “%s” in the sources of %s.", resID, p.Name).
				WithHint("Add it first, or name a picture the project has.").WithCommand("metarush resources list --project " + p.ID)
		}
		if e.Resource.Kind != models.ResourceImage && e.Resource.Kind != models.ResourceSVG {
			return "", apperr.New(apperr.InvalidInput, "“%s” isn't a picture.", e.Resource.Name)
		}
		resID, name = e.Resource.ID, e.Resource.Name
	case in.Detail.File != nil:
		f := in.Detail.File
		path, err := a.Cache.Ensure(ctx, a.Media, media.ObjectKey(a.WorkspaceID(), f.SHA256), f.SHA256, f.Size, nil)
		if err != nil {
			return "", apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get %s from its storage.", f.Name)
		}
		if _, err := os.Stat(path); err != nil {
			return "", err
		}
		res, err := resources.AddFile(ctx, a, path, resources.AddOptions{ProjectID: p.ID, FileName: f.Name, Source: models.ResourceSource{Kind: models.FromFile}})
		if err != nil {
			if errors.Is(err, resources.ErrNotResource) {
				return "", apperr.New(apperr.InvalidInput, "%s isn't a picture, SVG, PDF or font, so it can't be kept in the sources.", f.Name)
			}
			return "", err
		}
		if res.Resource != nil {
			resID, name = res.Resource.ID, res.Resource.Name
		}
	case in.Detail.URL != "":
		res, err := callRaw(ctx, a, "resources.fetch", map[string]any{"url": in.Detail.URL, "projectId": p.ID, "as": "picture"})
		if err != nil {
			return "", err
		}
		out.Jobs = jobIDsIn(res)
		return "Bringing the picture into the sources of " + p.Name + ".", nil
	default:
		return "", invalid("Say which picture to keep.")
	}
	if !in.Detail.BrandKit && in.Detail.FromSite == "" {
		if name == "" {
			return "Kept in the sources of " + p.Name + ".", nil
		}
		return "Kept “" + name + "” in the sources of " + p.Name + ".", nil
	}
	// The project's own brand (D193): a logo is one client's, so a project that
	// uses another's settings gets settings of its own first (a copy of what
	// it uses), rather than changing the brand of every project using them.
	st := KeepSettingsOf(ctx, a, p.ID)
	gotOwn := ""
	if !st.Own {
		if st.Owner == "" {
			// Its own, empty: What may leave asks first (D289).
			if err := leaveSwitch(ctx, a, "“"+p.Name+"”", st.Owner, p.ID); err != nil {
				return "", err
			}
		}
		if _, err := content.Own(ctx, a, p.ID, st.Owner != ""); err != nil {
			if !apperr.Is(err, apperr.Conflict) {
				return "", err
			}
			// Its own from before: What may leave asks first (D289).
			if err := leaveSwitch(ctx, a, "“"+p.Name+"”", st.Owner, p.ID); err != nil {
				return "", err
			}
			if _, err := content.Own(ctx, a, p.ID, false); err != nil {
				return "", err
			}
		}
		if st.Owner != "" {
			gotOwn = " " + p.Name + " has settings of its own now, starting as a copy of “" + st.OwnerName + "”'s."
		}
	}
	owner := p.ID
	req := map[string]any{"projectId": owner}
	if in.Detail.FromSite != "" {
		req["fromSite"] = in.Detail.FromSite
	} else {
		req["logo"] = resID
		req["fromLogo"] = true
	}
	// The project's latest cut takes the brand too, as a checked change: the
	// person confirmed it with the card (or the command), so its token is used.
	hasCut := false
	for _, b := range p.Branches {
		if b.Head != "" {
			hasCut = true
		}
	}
	whose := "the brand's logo"
	if hasCut {
		req["apply"] = true
	}
	res, err := callRaw(ctx, a, "brand.set", req)
	if err != nil {
		return "", err
	}
	if pending, _ := res["pending"].(bool); pending && hasCut {
		if tok := digString(res, "cut", "check", "confirmToken"); tok != "" {
			req["confirmToken"] = tok
			if res, err = callRaw(ctx, a, "brand.set", req); err != nil {
				return "", err
			}
		}
	}
	if rev := digString(res, "cut", "applied", "revision", "id"); rev != "" {
		out.RevisionID = rev
		return "Kept the logo in the sources of " + p.Name + " and made it " + whose + ", with its colours; the latest cut has them now (a new version)." + gotOwn, nil
	}
	return "Kept the logo in the sources of " + p.Name + " and made it " + whose + ", with its colours." + gotOwn, nil
}

// digBool reads a bool inside nested JSON objects (false when it isn't there).
func digBool(m map[string]any, path ...string) bool {
	var cur any = m
	for _, k := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur = obj[k]
	}
	b, _ := cur.(bool)
	return b
}

// digString reads a string inside nested JSON objects ("" when it isn't there).
func digString(m map[string]any, path ...string) string {
	var cur any = m
	for _, k := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = obj[k]
	}
	s, _ := cur.(string)
	return s
}

func init() {
	Def("keep.add", "Keep something a conversation brought in: a source, a logo and the brand kit, the brief, a rule or a check, a person's note, a sign-off, an idea for MetaRush, a spelling; it is never offered again", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *KeepAddIn) (*KeepAddOut, error) { return KeepAdd(ctx, a, *in) })
	Def("keep.list", "What a project's conversations offered to keep: asked, kept and declined", Flags{},
		func(ctx context.Context, a *app.App, in *KeepListIn) (*KeepListOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			items, err := keep.List(ctx, a.DB, a.WorkspaceID(), p.ID, in.State)
			if err != nil {
				return nil, err
			}
			out := &KeepListOut{ProjectID: p.ID, Items: items}
			kept := 0
			for _, it := range items {
				if it.State == models.KeepKept {
					kept++
				}
			}
			out.Summary = fmt.Sprintf("%s: %s offered, %d kept.", p.Name, pluralWord(len(items), "thing"), kept)
			return out, nil
		})
	Def("keep.decline", "Decline something a conversation offered to keep: it is never offered again in the project", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *KeepDeclineIn) (*KeepAddOut, error) {
			var it models.KeepItem
			if in.Item != "" {
				prev, err := keep.Get(ctx, a.DB, a.WorkspaceID(), in.Item)
				if err != nil {
					return nil, apperr.New(apperr.NotFound, "MetaRush has nothing like that to decline.")
				}
				it = *prev
			} else {
				if !validKind(in.Kind) {
					return nil, invalid("Say what to decline.")
				}
				pid := ""
				if projectKinds[in.Kind] {
					p, err := resolveProject(ctx, a, in.ProjectID)
					if err != nil {
						return nil, err
					}
					pid = p.ID
				}
				it = models.KeepItem{ProjectID: pid, Kind: in.Kind, Text: strings.TrimSpace(in.Text), Detail: in.Detail, From: "person"}
			}
			if it.State == models.KeepKept {
				return &KeepAddOut{Item: it, Already: true, Summary: "That is kept already; take it out where it is kept."}, nil
			}
			d, err := keep.Decide(ctx, a.DB, a.WorkspaceID(), it, models.KeepDeclined, a.Actor, "")
			if err != nil {
				return nil, err
			}
			a.Audit(ctx, "keep.decline", "keep", d.ID, d.ProjectID, map[string]any{"kind": d.Kind})
			return &KeepAddOut{Item: *d, Summary: "Not kept; MetaRush won't offer it again."}, nil
		})
}
