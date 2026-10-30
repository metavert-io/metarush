package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Changed words a version plays, checked now (D309, D311): the consent behind
// each change (new words in the person's voice, with that service), its voice
// copy, and the consents behind a matched mouth. A consent withdrawn or ended
// since the change went in is said wherever the version is checked or goes,
// and holds the version back from going out (a person can't send it out
// anyway: the words come out of the cut first).

// SaidHeard is a change a version plays (its nested cuts' included).
type SaidHeard struct {
	editspec.SaidPlay
	// Who says the new words; Words what is said now; Before what was said.
	Who, Words, Before string
	// Change is its record (nil when there is none).
	Change *models.SpokenChange
	// Withdrawn says why the new words may no longer be heard ("" while they may).
	Withdrawn string
	// Elsewhere: the consent holds for the change's own project but doesn't
	// cover the version's (Withdrawn says so).
	Elsewhere bool
	// Stray: the cut plays its new sound or matched mouth as a track or clip
	// of its own, not where the words were said (only a version saved before
	// that was refused can).
	Stray bool
	// Mouth: the matched mouth plays too; MouthWithdrawn says why it may no
	// longer be shown ("" while it may).
	Mouth          bool
	MouthWithdrawn string
}

// SaidHeardIn lists the changes a plan plays, each checked now: through
// their mix.said entries, and any other play of a change's new sound or
// matched mouth (as a track or a clip of its own, which no edit can save any
// more, but a version saved before may have). spec is the version's own
// (what its mix says about its own changes when a record is missing);
// projectID the version's project, which every consent must cover too (a
// cut nesting another project's cut is that project's video).
func SaidHeardIn(ctx context.Context, a *app.App, plan *editspec.RenderPlan, spec *editspec.Spec, projectID string) ([]SaidHeard, error) {
	plays := editspec.SaidPlays(plan)
	srcs := editspec.PlanSourceIDs(plan)
	ids, assets := bson.A{}, bson.A{}
	for _, p := range plays {
		ids = append(ids, p.ChangeID)
	}
	for _, id := range srcs {
		assets = append(assets, id)
	}
	if len(ids) == 0 && len(assets) == 0 {
		return nil, nil
	}
	own := map[string]editspec.SaidSound{}
	if spec != nil && spec.Mix != nil {
		for _, s := range spec.Mix.Said {
			own[s.ID] = s
		}
	}
	var cs []models.SpokenChange
	if err := a.DB.Find(ctx, store.CSpokenChanges, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"_id": bson.M{"$in": ids}},
		bson.M{"sound": bson.M{"$in": assets}}, bson.M{"mouth.assetId": bson.M{"$in": assets}}}}, &cs); err != nil {
		return nil, err
	}
	byID := map[string]*models.SpokenChange{}
	for i := range cs {
		byID[cs[i].ID] = &cs[i]
	}
	shown := map[string]bool{}
	for _, id := range srcs {
		shown[id] = true
	}
	// Plays of a change's sound or mouth outside its own place.
	stray := map[string]bool{}
	heard := map[string]bool{}
	for _, p := range plays {
		heard[p.ChangeID] = true
	}
	for _, c := range cs {
		if heard[c.ID] {
			continue
		}
		from, to, ok := editspec.AssetPlays(plan, c.Sound)
		if !ok && c.Mouth != nil {
			from, to, ok = editspec.AssetPlays(plan, c.Mouth.AssetID)
		}
		if ok {
			plays = append(plays, editspec.SaidPlay{ChangeID: c.ID, From: from, To: to})
			stray[c.ID], heard[c.ID] = true, true
		}
	}
	if len(plays) == 0 {
		return nil, nil
	}
	sort.SliceStable(plays, func(i, j int) bool { return plays[i].From < plays[j].From })
	out := make([]SaidHeard, 0, len(plays))
	for _, p := range plays {
		h := SaidHeard{SaidPlay: p, Change: byID[p.ChangeID], Stray: stray[p.ChangeID]}
		if s, ok := own[p.ChangeID]; ok {
			h.Who, h.Words, h.Before = s.Who, s.Words, s.Before
		}
		c := h.Change
		if c == nil {
			h.Withdrawn = "MetaRush has no record of these changed words here, so nothing says the person agreed to them."
			out = append(out, h)
			continue
		}
		h.Who, h.Words, h.Before = firstNonEmpty(c.Speaker.Name, h.Who), c.Words, c.Before
		why, err := saidWithdrawn(ctx, a, c)
		if err != nil {
			return nil, err
		}
		if why == "" && projectID != "" && projectID != c.ProjectID {
			if why, err = notCoveredHere(ctx, a, []string{c.ConsentID}, projectID, firstNonEmpty(c.Speaker.Name, "The speaker")+"'s consent for new words in their voice"); err != nil {
				return nil, err
			}
			h.Elsewhere = why != ""
		}
		h.Withdrawn = why
		if m := c.Mouth; m != nil && m.AssetID != "" && shown[m.AssetID] {
			h.Mouth = true
			if h.MouthWithdrawn, err = mouthWithdrawn(ctx, a, c, projectID); err != nil {
				return nil, err
			}
		}
		out = append(out, h)
	}
	return out, nil
}

// SaidHeardInVersion is SaidHeardIn for a version (nil without any changed
// words, its own or a nested cut's, so most versions skip the plan).
func SaidHeardInVersion(ctx context.Context, a *app.App, rev *models.Revision) ([]SaidHeard, error) {
	return saidHeardInRevision(ctx, a, rev)
}

func saidHeardInRevision(ctx context.Context, a *app.App, rev *models.Revision) ([]SaidHeard, error) {
	if may, err := mayPlaySaid(ctx, a, &rev.Spec); err != nil || !may {
		return nil, err
	}
	spec := rev.Spec
	facts, err := loadFactsPinned(ctx, a, rev.ProjectID, &spec, transcripts.PinsOf(rev))
	if err != nil {
		return nil, err
	}
	return SaidHeardIn(ctx, a, editspec.Resolve(&spec, facts, rev.ID, rev.ProjectID, rev.SpecHash), &rev.Spec, rev.ProjectID)
}

// mayPlaySaid: the cut has changed words of its own, nests another cut
// (which may have some), or plays a change's new sound or matched mouth some
// other way.
func mayPlaySaid(ctx context.Context, a *app.App, s *editspec.Spec) (bool, error) {
	if s.Mix != nil && len(s.Mix.Said) > 0 {
		return true, nil
	}
	in := bson.A{}
	for _, c := range s.Clips {
		if editspec.IsNested(c.AssetID) {
			return true, nil
		}
	}
	for _, id := range editspec.AssetIDs(s) {
		in = append(in, id)
	}
	if len(in) == 0 {
		return false, nil
	}
	n, err := a.DB.C(store.CSpokenChanges).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(),
		"$or": bson.A{bson.M{"sound": bson.M{"$in": in}}, bson.M{"mouth.assetId": bson.M{"$in": in}}}})
	return n > 0, err
}

// saidWithdrawn says why a change's new words may no longer be heard: its
// consent (new words, with its service, and their voice) or its voice copy
// is no longer in force. "" while they may.
func saidWithdrawn(ctx context.Context, a *app.App, c *models.SpokenChange) (string, error) {
	who := firstNonEmpty(c.Speaker.Name, "The speaker")
	if c.State == models.SpokenWithdrawn {
		return firstNonEmpty(c.Why, who+"'s consent for new words in their voice was withdrawn."), nil
	}
	// Whether it has ended goes by the database server's clock (D360), so
	// every computer holds the version back from the same moment.
	now, err := a.DB.Now(ctx)
	if err != nil {
		return "", err
	}
	var cs models.Consent
	if err := a.DB.GetByID(ctx, store.CConsents, a.WorkspaceID(), c.ConsentID, &cs); err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	} else if err != nil || !cs.HoldsNewWords(c.ProjectID, c.Provider, now) {
		return fmt.Sprintf("%s's consent for new words in their voice is no longer in force.", who), nil
	}
	var g models.Generation
	if err := a.DB.GetByID(ctx, store.CGenerations, a.WorkspaceID(), c.VoiceGeneration, &g); err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	} else if err != nil || g.Withdrawn != nil {
		return fmt.Sprintf("%s's voice copy was withdrawn.", who), nil
	}
	return "", nil
}

// mouthWithdrawn says why a change's matched mouth may no longer be shown
// in a project's video: a consent the lip sync relied on was withdrawn, has
// ended, or doesn't cover that project. "" while it may.
func mouthWithdrawn(ctx context.Context, a *app.App, c *models.SpokenChange, projectID string) (string, error) {
	m := c.Mouth
	if m == nil || m.GenerationID == "" {
		return "", nil
	}
	if m.State == models.SpokenWithdrawn {
		return firstNonEmpty(m.Why, "A consent the matched mouth relied on was withdrawn."), nil
	}
	var g models.Generation
	if err := a.DB.GetByID(ctx, store.CGenerations, a.WorkspaceID(), m.GenerationID, &g); err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	} else if err != nil {
		return "MetaRush has no record of how the mouth was matched, so nothing says the people there agreed.", nil
	}
	if g.Withdrawn != nil {
		return "The matched mouth was withdrawn.", nil
	}
	project := firstNonEmpty(g.ProjectID, c.ProjectID)
	now, err := a.DB.Now(ctx) // the server's clock, as saidWithdrawn's
	if err != nil {
		return "", err
	}
	for _, id := range g.ConsentIDs {
		var cs models.Consent
		if err := a.DB.GetByID(ctx, store.CConsents, a.WorkspaceID(), id, &cs); err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		} else if err != nil || !cs.Covers(project, now) {
			return fmt.Sprintf("%s's consent for lip sync, which the matched mouth relied on, is no longer in force.", firstNonEmpty(cs.PersonName, "Someone")), nil
		}
	}
	if projectID != "" && projectID != project {
		return notCoveredHere(ctx, a, g.ConsentIDs, projectID, "")
	}
	return "", nil
}

// notCoveredHere says which of these consents doesn't cover a project (the
// one whose video plays the changed words): "" when they all do. what names
// the consent ("" says whose consent for lip sync).
func notCoveredHere(ctx context.Context, a *app.App, consentIDs []string, projectID, what string) (string, error) {
	now, err := a.DB.Now(ctx) // the server's clock, as saidWithdrawn's
	if err != nil {
		return "", err
	}
	for _, id := range consentIDs {
		var cs models.Consent
		if err := a.DB.GetByID(ctx, store.CConsents, a.WorkspaceID(), id, &cs); err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		} else if err == nil && cs.Covers(projectID, now) {
			continue
		}
		var p models.Project
		name := "this project"
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p) == nil && p.Name != "" {
			name = "“" + p.Name + "”"
		}
		if what == "" {
			what = firstNonEmpty(cs.PersonName, "Someone") + "'s consent for lip sync, which the matched mouth relied on,"
		}
		return fmt.Sprintf("%s doesn't cover %s.", what, name), nil
	}
	return "", nil
}

// saidHolds are what holds a version back while it plays changed words whose
// consent is no longer in force (never sent out anyway: take them out first).
func saidHolds(p *models.Project, rev *models.Revision, heard []SaidHeard) []GateHold {
	var out []GateHold
	for _, h := range heard {
		why, what := h.Withdrawn, fmt.Sprintf("At %s %s says “%s” (words changed with AI in their voice copy)", shortClock(h.From), firstNonEmpty(h.Who, "the speaker"), h.Words)
		if why == "" && h.Mouth && h.MouthWithdrawn != "" {
			why, what = h.MouthWithdrawn, fmt.Sprintf("At %s the mouth is matched to “%s” with AI", shortClock(h.From), h.Words)
		}
		if why == "" {
			continue
		}
		says := fmt.Sprintf("%s, but %s So it can't go out: take them out of the cut and make a new version.", what, lowerFirst(why))
		gh := GateHold{ShipHold: models.ShipHold{Key: "consent:said:" + h.ChangeID + "@" + rev.ID, Kind: models.HoldConsent, Says: says}}
		if h.Elsewhere {
			gh.Says = fmt.Sprintf("%s, in a cut this one plays, but %s So it can't go out: record their consent for this project too, or play a version of that cut without them.", what, lowerFirst(why))
		} else if h.Stray {
			gh.Says = fmt.Sprintf("%s, played as a track or clip of its own, but %s So it can't go out: take it out of the cut and make a new version.", what, lowerFirst(why))
		} else if h.Nested == "" && h.Change != nil && h.Change.ProjectID == p.ID {
			gh.Paths = []GatePath{{Label: "Take them out", Op: "cut.say.undo", Input: map[string]any{"id": h.ChangeID, "branch": orBranch(rev.Branch)},
				Command: fmt.Sprintf("metarush cut say undo %s --branch %s", h.ChangeID, orBranch(rev.Branch)), People: true}}
		} else {
			gh.Says = fmt.Sprintf("%s, in a cut this one plays, but %s So it can't go out: take them out of that cut, then play its new version here.", what, lowerFirst(why))
		}
		out = append(out, gh)
	}
	return out
}

// changedNote is a WebVTT file's note on the changed words its video plays
// (D311), saying whether each one's consent still holds.
func changedNote(heard []SaidHeard) string {
	var cs []transcripts.Changed
	for _, h := range heard {
		c := transcripts.Changed{Who: h.Who, Words: h.Words, Before: h.Before}
		switch {
		case h.Change == nil && h.Withdrawn == "":
			// not known (couldn't be looked up)
		case h.Elsewhere:
			c.Consent = "elsewhere"
		case h.Withdrawn != "" || (h.Mouth && h.MouthWithdrawn != ""):
			c.Consent = "withdrawn"
		default:
			c.Consent = "held"
		}
		cs = append(cs, c)
	}
	return transcripts.ChangedNote(cs)
}

// changedNoteOf is changedNote for a version (its own changes as its mix
// says, consent not known, when they couldn't be looked up).
func changedNoteOf(ctx context.Context, a *app.App, rev *models.Revision) string {
	heard, err := saidHeardInRevision(ctx, a, rev)
	if err != nil && rev.Spec.Mix != nil {
		heard = nil
		for _, c := range rev.Spec.Mix.Said {
			heard = append(heard, SaidHeard{SaidPlay: editspec.SaidPlay{ChangeID: c.ID}, Who: c.Who, Words: c.Words, Before: c.Before})
		}
	}
	return changedNote(heard)
}
