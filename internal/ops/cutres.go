package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Cut-level changes that involve resources and other projects' cuts:
// "update to latest" (resource pins and nested cuts move to their newest
// versions, explicitly), nesting another project's cut (D84), copying one in
// as a starting point (no live link), and putting the brand kit into a cut.
// Each is a checked plan like the other cut verbs.

// CutUpdateIn brings the cut's pins up to date.
type CutUpdateIn struct {
	CutEditCommon
	// Resources are the short names to update (empty: all); Cuts the nested
	// clips (empty: all). Only limits it to resources or cuts.
	Resources []string `json:"resources,omitempty"`
	Cuts      []string `json:"cuts,omitempty" jsonschema:"description=Nested clips (names or numbers)."`
	Only      string   `json:"only,omitempty" jsonschema:"enum=resources,enum=cuts"`
}

// CutNestIn puts another project's cut into this cut as one clip.
type CutNestIn struct {
	CutEditCommon
	From     string `json:"from" jsonschema:"minLength=1,description=The project whose cut it plays (ID or name)."`
	Revision string `json:"revision,omitempty" jsonschema:"description=Which version: rN or a revision ID (default: its latest)."`
	Position string `json:"position,omitempty" jsonschema:"description=end (default: the end\\, or just before a locked ending)\\, start\\, after <clip>\\, before <clip> or a number."`
	In       string `json:"in,omitempty" jsonschema:"description=Where to start in that cut (0:03.5; default the beginning)."`
	Out      string `json:"out,omitempty" jsonschema:"description=Where to end (default the end)."`
	Name     string `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64"`
}

// CutCopyInIn copies another project's cut into this one as a starting point.
type CutCopyInIn struct {
	CutEditCommon
	From     string `json:"from" jsonschema:"minLength=1,description=The project whose cut to copy (ID or name)."`
	Revision string `json:"revision,omitempty" jsonschema:"description=rN or a revision ID (default: its latest)."`
	Position string `json:"position,omitempty" jsonschema:"description=end (default)\\, start\\, after <clip>\\, before <clip> or a number."`
}

// CutWhereIn names a project whose cut may be nested elsewhere.
type CutWhereIn struct {
	ProjectID string `json:"projectId"`
}

// CutWhereOut lists the cuts that play this project's cut.
type CutWhereOut struct {
	Uses    []UseRow `json:"uses"`
	Summary string   `json:"summary"`
}

// BrandApplyIn puts the brand kit into the latest cut.
type BrandApplyIn struct {
	CutEditCommon
}

// finishFirst checks or saves a project's first cut (a full spec), the way
// editTarget.finish does for later ones.
func finishFirst(ctx context.Context, a *app.App, p *models.Project, c CutEditCommon, verb string, spec *editspec.Spec, summary, instruction string) (*CutEditOut, error) {
	out := &CutEditOut{ProjectID: p.ID, Branch: "main", Summary: summary, Problems: []cutsheet.Problem{}}
	editspec.Normalize(spec)
	out.Duration = round3(specLength(spec))
	info := newSheetInfo(ctx, a)
	info.project = p.ID
	names := cutsheet.NameFunc(spec, info, projectAssetIDs(ctx, a, p.ID))
	out.Changes = cutsheet.Describe(&editspec.Spec{Output: spec.Output}, spec, names)
	if c.Instruction != "" {
		instruction = c.Instruction
	}
	origin := "cut-" + verb + ":cli"
	if a.Actor.Via == "dashboard" {
		origin = "cut-" + verb + ":studio"
	}
	raw, err := json.Marshal(editspec.Plan{ProjectID: p.ID, Instruction: instruction, Rationale: c.Rationale, Origin: origin, Spec: spec})
	if err != nil {
		return nil, err
	}
	out.Plan = raw
	plain := func(issues []editspec.Issue) {
		for _, is := range issues {
			out.Problems = append(out.Problems, cutsheet.Locate(is, spec, &cutsheet.SourceMap{}, info, names))
		}
	}
	if c.DryRun || (c.ConfirmToken == "" && a.Actor.Via != "dashboard") {
		chk, err := checkPlan(ctx, a, raw, c.AllowUnlock)
		if err != nil {
			return nil, err
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
		return nil, err
	}
	plain(res.Warnings)
	out.Applied, out.Saved = res, true
	return out, nil
}

// sourceCut finds another project's cut at a version.
func sourceCut(ctx context.Context, a *app.App, from, revision string, here *models.Project) (*models.Project, *models.Revision, error) {
	src, err := resolveProject(ctx, a, from)
	if err != nil {
		return nil, nil, err
	}
	if here != nil && src.ID == here.ID {
		return nil, nil, apperr.New(apperr.InvalidInput, "That's this project: a cut can't play itself.")
	}
	rev, _, err := resolveRevision(ctx, a, src, revision, "")
	if err != nil {
		return nil, nil, err
	}
	if rev == nil {
		return nil, nil, apperr.New(apperr.NotFound, "%s has no cut yet.", src.Name)
	}
	return src, rev, nil
}

func cutWords(src *models.Project, rev *models.Revision) string {
	if t := strings.TrimSpace(rev.Spec.Title); t != "" && !strings.EqualFold(t, src.Name) {
		return fmt.Sprintf("the cut “%s” from %s (version %d)", t, src.Name, rev.Number)
	}
	return fmt.Sprintf("%s's cut (version %d)", src.Name, rev.Number)
}

// linkCut lets the project use another project's cut (an item link), unless it already can.
func linkCut(ctx context.Context, a *app.App, p, src *models.Project) (bool, error) {
	view, err := projectlinks.Load(ctx, a, p.ID)
	if err != nil {
		return false, err
	}
	if view.SeesCut(src.ID) {
		return false, nil
	}
	l := &models.ProjectLink{ProjectID: p.ID, SourceID: src.ID}
	if cur, err := getLink(ctx, a, p.ID, src.ID); err == nil {
		l = cur
	}
	l.Items = append(l.Items, models.LinkItem{Kind: models.LinkCuts, ID: src.ID, Name: "the cut"})
	l.Kept = false
	if err := saveLink(ctx, a, l); err != nil {
		return false, err
	}
	a.Audit(ctx, "project.use", "project", p.ID, p.ID, map[string]any{"from": src.ID, "items": 1, "why": "nest"})
	return true, nil
}

// linkFootage lets the project use footage of another project (item links).
func linkFootage(ctx context.Context, a *app.App, p *models.Project, assets []models.Asset) (int, error) {
	view, err := projectlinks.Load(ctx, a, p.ID)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range assets {
		as := &assets[i]
		if view.SeesAsset(ctx, a, as) || len(as.ProjectIDs) == 0 {
			if len(as.ProjectIDs) == 0 && !slices.Contains(as.ProjectIDs, p.ID) {
				// Inbox footage simply joins the project.
				_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bsonAddProject(p.ID))
				n++
			}
			continue
		}
		src := as.ProjectIDs[0]
		l := &models.ProjectLink{ProjectID: p.ID, SourceID: src}
		if cur, err := getLink(ctx, a, p.ID, src); err == nil {
			l = cur
		}
		if l.Whole && !l.Kept {
			continue
		}
		l.Items = append(l.Items, models.LinkItem{Kind: models.LinkFootage, ID: as.ID, Name: as.DisplayTitle()})
		l.Kept = false
		if err := saveLink(ctx, a, l); err != nil {
			return n, err
		}
		n++
	}
	if n > 0 {
		refreshProjectCounts(ctx, a, p.ID)
	}
	return n, nil
}

func init() {
	Def("cut.update", "Update the latest cut's resources and nested cuts to their newest versions (an explicit change, shown before it is saved)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutUpdateIn) (*CutEditOut, error) {
		t, err := loadEditTarget(ctx, a, in.CutEditCommon)
		if err != nil {
			return nil, err
		}
		var ops []editspec.Op
		var said []string
		if in.Only != "cuts" {
			names := in.Resources
			if len(names) == 0 {
				names = editspec.ResourceNames(t.spec)
			}
			for _, n := range names {
				pin, ok := t.spec.Resources[n]
				if !ok {
					return nil, apperr.New(apperr.NotFound, "This cut has no resource called %s.", n).WithHint("Its resources: " + strings.Join(editspec.ResourceNames(t.spec), ", ") + ".")
				}
				r := t.facts.res.byID[pin.ID]
				if r == nil || r.Removed {
					continue
				}
				if v := r.Latest(); v != nil && v.SHA256 != pin.SHA256 {
					np := editspec.ResourcePin{ID: pin.ID, SHA256: v.SHA256}
					ops = append(ops, editspec.Op{Op: "setResource", ID: n, Resource: &np})
					said = append(said, n+" to its newest version")
				}
			}
		}
		if in.Only != "resources" {
			want := map[int]bool{}
			for _, ref := range in.Cuts {
				i, err := t.clip(ref)
				if err != nil {
					return nil, err
				}
				want[i] = true
			}
			for i, c := range t.spec.Clips {
				if !editspec.IsNested(c.AssetID) || (len(want) > 0 && !want[i]) {
					continue
				}
				pid, rid, _ := editspec.NestedRef(c.AssetID)
				ns, ok := t.facts.Nested(pid, rid)
				if !ok || ns.LatestID == "" || ns.LatestID == ns.RevisionID || ns.Cycle {
					continue
				}
				var head models.Revision
				if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), ns.LatestID, &head) != nil {
					continue
				}
				set := map[string]any{"assetId": editspec.NestedKey(ns.ProjectID, ns.LatestID)}
				note := fmt.Sprintf("%s to version %d of %s", c.ID, head.Number, firstNonEmpty(ns.Title, ns.ProjectName+"'s cut"))
				if head.Duration > 0 && c.Out > head.Duration {
					set["out"] = math.Floor(head.Duration*1000) / 1000
					if c.In >= head.Duration {
						set["in"] = 0.0
					}
					note += fmt.Sprintf(" (it is shorter now, so the clip ends at %s)", cutsheet.Clock(head.Duration))
				}
				raw, _ := json.Marshal(set)
				ops = append(ops, editspec.Op{Op: "updateClip", ID: c.ID, Set: raw})
				said = append(said, note)
			}
		}
		summary := "Everything is up to date."
		if len(said) > 0 {
			summary = "Updates " + joinAnd(said) + "."
		}
		out := &CutEditOut{}
		if len(ops) == 0 {
			out.ProjectID, out.Branch, out.Summary, out.NoChange = t.project.ID, t.branch, summary, true
			out.Changes, out.Problems = []string{}, []cutsheet.Problem{}
			out.Duration, out.DurationBefore = round3(t.length()), round3(t.length())
			raw, _ := json.Marshal(editspec.Plan{BaseRevisionID: t.rev.ID})
			out.Plan = raw
			return out, nil
		}
		return t.finish(ctx, a, in.CutEditCommon, "update", ops, summary, "Update to the latest versions", out)
	})
	Def("cut.nest", "Put another project's cut into this cut as one clip, pinned to one version (it plays as that cut does: picture, sound, narration, graphics)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutNestIn) (*CutEditOut, error) {
		p, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		src, rev, err := sourceCut(ctx, a, in.From, in.Revision, p)
		if err != nil {
			return nil, err
		}
		key := editspec.NestedKey(src.ID, rev.ID)
		clip := editspec.Clip{AssetID: key, In: 0, Out: math.Floor(rev.Duration*1000) / 1000}
		if s, ok := cutsheet.ParseClock(strings.TrimSpace(in.In)); ok {
			clip.In = s
		} else if in.In != "" {
			return nil, apperr.New(apperr.InvalidInput, "%q isn't a time in that cut (like 0:03.5).", in.In)
		}
		if s, ok := cutsheet.ParseClock(strings.TrimSpace(in.Out)); ok {
			clip.Out = s
		} else if in.Out != "" {
			return nil, apperr.New(apperr.InvalidInput, "%q isn't a time in that cut (like 0:12).", in.Out)
		}
		what := cutWords(src, rev)
		summary := "Plays " + what + "."
		// Saving lets this project use that cut (an item link); a dry run
		// judges the plan as if it already could.
		if in.DryRun || (in.ConfirmToken == "" && a.Actor.Via != "dashboard") {
			ctx = withAssumed(ctx, src.ID)
		} else if linked, err := linkCut(ctx, a, p, src); err != nil {
			return nil, err
		} else if linked {
			summary += " " + src.Name + "'s cut can be used here now."
		}
		c := in.CutEditCommon
		c.ProjectID = p.ID
		if p.Branches[firstNonEmpty(c.Branch, "main")].Head == "" {
			clip.ID = firstNonEmpty(in.Name, newClipID(&editspec.Spec{}, slugish(src.Name)))
			spec := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Output: rev.Spec.Output, Clips: []editspec.Clip{clip}}
			return finishFirst(ctx, a, p, c, "nest", spec, summary, "Nest "+what)
		}
		t, err := loadEditTarget(ctx, a, c)
		if err != nil {
			return nil, err
		}
		clip.ID = firstNonEmpty(in.Name, newClipID(t.spec, slugish(src.Name)))
		after, before, err := t.position(in.Position, "")
		if err != nil {
			return nil, err
		}
		cl := clip
		where := t.insertWhere(after, before)
		summary = "Plays " + what + " " + where + "." + strings.TrimPrefix(summary, "Plays "+what+".")
		return t.finish(ctx, a, c, "nest", []editspec.Op{{Op: "insertClip", After: after, Before: before, Clip: &cl}}, summary, "Nest "+what+" "+where, &CutEditOut{})
	})
	Def("cut.copyIn", "Copy another project's cut into this cut as a starting point: its clips, transitions, graphics, sound and narration (no live link; its footage becomes usable here)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutCopyInIn) (*CutEditOut, error) {
		p, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		src, rev, err := sourceCut(ctx, a, in.From, in.Revision, p)
		if err != nil {
			return nil, err
		}
		from := editspec.Clone(&rev.Spec)
		editspec.Normalize(from)
		// The footage it plays must be usable here.
		var assets []models.Asset
		_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bsonIn(editspec.AssetIDs(from)), &assets)
		what := cutWords(src, rev)
		summary := fmt.Sprintf("Copies %s in: %s.", what, plural(len(from.Clips), "clip"))
		if in.DryRun || (in.ConfirmToken == "" && a.Actor.Via != "dashboard") {
			ids := make([]string, 0, len(assets))
			for _, as := range assets {
				ids = append(ids, as.ID)
			}
			for _, k := range editspec.NestedKeys(from) {
				pid, _, _ := editspec.NestedRef(k)
				ids = append(ids, pid)
			}
			ctx = withAssumed(ctx, ids...)
		} else {
			if n, err := linkFootage(ctx, a, p, assets); err != nil {
				return nil, err
			} else if n > 0 {
				summary += fmt.Sprintf(" %s it uses can be used here now.", plural(n, "clip"))
			}
			for _, k := range editspec.NestedKeys(from) {
				pid, _, _ := editspec.NestedRef(k)
				var np models.Project
				if pid != p.ID && a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), pid, &np) == nil {
					_, _ = linkCut(ctx, a, p, &np)
				}
			}
		}
		c := in.CutEditCommon
		c.ProjectID = p.ID
		if p.Branches[firstNonEmpty(c.Branch, "main")].Head == "" {
			spec := editspec.Clone(from)
			spec.Title = ""
			for i := range spec.Clips {
				spec.Clips[i].Locked = false
			}
			return finishFirst(ctx, a, p, c, "copy-in", spec, summary, "Copy in "+what)
		}
		t, err := loadEditTarget(ctx, a, c)
		if err != nil {
			return nil, err
		}
		ops, err := copyInOps(t, from, in.Position)
		if err != nil {
			return nil, err
		}
		return t.finish(ctx, a, c, "copy-in", ops, summary, "Copy in "+what, &CutEditOut{})
	})
	Def("cut.where", "List the cuts that play this project's cut inside them (nested cuts)", Flags{}, func(ctx context.Context, a *app.App, in *CutWhereIn) (*CutWhereOut, error) {
		p, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		uses, err := cutUses(ctx, a, p.ID)
		if err != nil {
			return nil, err
		}
		if uses == nil {
			uses = []UseRow{}
		}
		s := "No other cut plays " + p.Name + "'s cut."
		if len(uses) > 0 {
			s = fmt.Sprintf("%s's cut plays inside %s.", p.Name, plural(len(uses), "other cut"))
		}
		return &CutWhereOut{Uses: uses, Summary: s}, nil
	})
	Def("brand.apply", "Use the brand in the latest cut — the brand of the Content Settings the project uses (its own, another project's or the default's): its colours, font and logos (a checked change)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *BrandApplyIn) (*CutEditOut, error) {
		p, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		k, owner, err := kitFor(ctx, a, p.ID)
		if err != nil {
			return nil, err
		}
		if kitEmpty(k) {
			return nil, apperr.New(apperr.NotConfigured, "There's no brand in the settings %s uses yet.", p.Name).
				WithHint("Add a logo in the Brand part of the project's Settings tab.").WithCommand("metarush brand set --project <project> --logo <file name>")
		}
		c := in.CutEditCommon
		c.ProjectID = p.ID
		return putKitOnCut(ctx, a, c, k, owner == p.ID)
	})
}

// kitOps are the changes that put a brand kit on a cut: its tokens (over the
// cut's own brand) and the pins of its logos and font file.
func kitOps(ctx context.Context, a *app.App, t *editTarget, k *models.BrandKit) []editspec.Op {
	var ops []editspec.Op
	b := KitBrand(k)
	if b == nil {
		b = &editspec.Brand{}
	}
	if t.spec.Brand != nil {
		merged := *t.spec.Brand
		for _, kv := range [][2]*string{{&merged.Primary, &b.Primary}, {&merged.Accent, &b.Accent}, {&merged.Text, &b.Text}, {&merged.Background, &b.Background}, {&merged.Font, &b.Font}, {&merged.FontFile, &b.FontFile},
			{&merged.Logo, &b.Logo}, {&merged.LogoDark, &b.LogoDark}, {&merged.LogoLight, &b.LogoLight}} {
			if *kv[1] != "" {
				*kv[0] = *kv[1]
			}
		}
		b = &merged
	}
	pins := kitPins(ctx, a, k)
	names := make([]string, 0, len(pins))
	for n := range pins {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		pin := pins[n]
		if cur, ok := t.spec.Resources[n]; ok && cur == pin {
			continue
		}
		pp := pin
		ops = append(ops, editspec.Op{Op: "setResource", ID: n, Resource: &pp})
	}
	if b.FontFile != "" {
		if _, ok := pins[b.FontFile]; !ok {
			if _, pinned := t.spec.Resources[b.FontFile]; !pinned {
				b.FontFile = ""
			}
		}
	}
	dropUnpinnedLogos(b, func(n string) bool {
		_, ok := pins[n]
		_, pinned := t.spec.Resources[n]
		return ok || pinned
	})
	if !equalBrand(t.spec.Brand, b) && *b != (editspec.Brand{}) {
		ops = append(ops, editspec.Op{Op: "setBrand", Brand: b})
	}
	return ops
}

// putKitOnCut puts a brand kit on a project's latest cut as a checked change
// (brand.apply, and brand.set with apply); own says it is the project's own kit.
func putKitOnCut(ctx context.Context, a *app.App, c CutEditCommon, k *models.BrandKit, own bool) (*CutEditOut, error) {
	t, err := loadEditTarget(ctx, a, c)
	if err != nil {
		return nil, err
	}
	what := "the brand kit"
	if own {
		what = "this project's brand kit"
	}
	ops := kitOps(ctx, a, t, k)
	if len(ops) == 0 {
		out := &CutEditOut{ProjectID: t.project.ID, Branch: t.branch, Summary: "The cut already uses " + what + ".", NoChange: true, Changes: []string{}, Problems: []cutsheet.Problem{}}
		out.Duration, out.DurationBefore = round3(t.length()), round3(t.length())
		raw, _ := json.Marshal(editspec.Plan{BaseRevisionID: t.rev.ID})
		out.Plan = raw
		return out, nil
	}
	return t.finish(ctx, a, c, "brand", ops, "Uses "+what+": its colours, font and logos.", "Use the brand kit", &CutEditOut{})
}

func bsonAddProject(projectID string) bson.M {
	return bson.M{"$addToSet": bson.M{"projectIds": projectID}, "$set": bson.M{"updatedAt": time.Now().UTC()}}
}

func bsonIn(ids []string) bson.M { return bson.M{"_id": bson.M{"$in": ids}} }

func equalBrand(a, b *editspec.Brand) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func slugish(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), "-"))
	if s == "" {
		return "cut"
	}
	return s
}

// copyInOps copies a cut's clips (with fresh names), its graphics and sounds
// (moved to where the clips land) and its narration (lines tied to the copied
// clips) into the target after position, with its resource pins.
func copyInOps(t *editTarget, from *editspec.Spec, position string) ([]editspec.Op, error) {
	after, before, err := t.position(position, "")
	if err != nil {
		return nil, err
	}
	used := map[string]bool{"captions": true, "voiceover": true, "mix": true}
	for _, c := range t.spec.Clips {
		used[c.ID] = true
	}
	for _, o := range t.spec.Overlays {
		used[o.ID] = true
	}
	for _, x := range t.spec.Audio {
		used[x.ID] = true
	}
	if t.spec.Voiceover != nil {
		for _, b := range t.spec.Voiceover.Beats {
			used[b.ID] = true
		}
	}
	rename := map[string]string{}
	fresh := func(id string) string {
		n := id
		for k := 2; used[n]; k++ {
			n = fmt.Sprintf("%s-%d", strings.TrimRight(id, "-"), k)
		}
		used[n] = true
		rename[id] = n
		return n
	}
	var ops []editspec.Op
	prev := after
	for i, c := range from.Clips {
		cl := c
		cl.ID = fresh(c.ID)
		cl.Locked = false
		op := editspec.Op{Op: "insertClip", Clip: &cl}
		switch {
		case i == 0 && before != "":
			op.Before = before
		case i == 0 && prev == "" && len(t.spec.Clips) > 0:
			op.Before = t.spec.Clips[0].ID
		default:
			op.After = prev
		}
		ops = append(ops, op)
		prev = cl.ID
	}
	// Where the first copied clip lands on the timeline: graphics, sounds and
	// clock-time narration move with the clips.
	next, _, err := editspec.ApplyOps(t.spec, ops, true)
	if err != nil {
		return nil, err
	}
	editspec.Normalize(next)
	placed, _ := editspec.Timeline(next)
	offset := 0.0
	for _, pc := range placed {
		if pc.ID == rename[from.Clips[0].ID] {
			offset = float64(pc.From) / float64(next.Output.FPS)
		}
	}
	pinRename := map[string]string{}
	for _, n := range editspec.ResourceNames(from) {
		pin := from.Resources[n]
		name := n
		if cur, ok := t.spec.Resources[n]; ok && cur != pin {
			for k := 2; ; k++ {
				name = fmt.Sprintf("%s-%d", n, k)
				if _, taken := t.spec.Resources[name]; !taken {
					break
				}
			}
		}
		pinRename[n] = name
		if cur, ok := t.spec.Resources[name]; !ok || cur != pin {
			pp := pin
			ops = append(ops, editspec.Op{Op: "setResource", ID: name, Resource: &pp})
		}
	}
	for _, o := range from.Overlays {
		ov := o
		ov.ID = fresh(o.ID)
		ov.At = round3(o.At + offset)
		ov.Locked = false
		if len(pinRename) > 0 {
			ov.Params = renameResourceParams(ov, pinRename)
		}
		ops = append(ops, editspec.Op{Op: "addOverlay", Overlay: &ov})
	}
	for _, x := range from.Audio {
		tr := x
		tr.ID = fresh(x.ID)
		tr.At = round3(x.At + offset)
		tr.Locked = false
		ops = append(ops, editspec.Op{Op: "addAudio", Track: &tr})
	}
	if v := from.Voiceover; v != nil && !from.Continuous() {
		for _, b := range v.Beats {
			bt := b
			bt.ID = fresh(b.ID)
			bt.Locked = false
			switch bt.Anchor.Kind {
			case editspec.AnchorClip:
				bt.Anchor.ClipID = rename[b.Anchor.ClipID]
			case editspec.AnchorMoment:
				if b.Anchor.ClipID != "" {
					bt.Anchor.ClipID = rename[b.Anchor.ClipID]
				}
			case editspec.AnchorTime:
				if b.Anchor.At != nil {
					at := round3(*b.Anchor.At + offset)
					bt.Anchor.At = &at
				}
			}
			ops = append(ops, editspec.Op{Op: "addBeat", Beat: &bt})
		}
	}
	return ops, nil
}

// renameResourceParams rewrites a graphic's resource names after pins were renamed.
func renameResourceParams(o editspec.Overlay, rename map[string]string) json.RawMessage {
	t, ok := editspec.TemplateByID(o.Template)
	if !ok || len(t.ResourceParams) == 0 {
		return o.Params
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(o.Params, &m) != nil {
		return o.Params
	}
	for _, p := range t.ResourceParams {
		var v string
		if json.Unmarshal(m[p], &v) != nil {
			continue
		}
		name, page, ok := editspec.ParseResourceRef(v)
		if !ok {
			continue
		}
		if n, ok := rename[name]; ok && n != name {
			if page > 0 {
				n += fmt.Sprintf("#%d", page)
			}
			m[p], _ = json.Marshal(n)
		}
	}
	b, _ := json.Marshal(m)
	return b
}

var _ = resources.FromHere
