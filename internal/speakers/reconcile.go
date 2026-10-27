package speakers

import (
	"context"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/names"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/voice"
)

// VoiceKey is the to-do group key for one voice of one detection run.
func VoiceKey(v models.AssetSpeaker) string {
	return "speaker:" + v.AssetID + ":" + v.Label + "@" + v.Run
}

// Reconcile brings an asset's speaker to-dos in line with its voices: an
// unnamed voice gets "Who is this?" (or "Is this Ada?" when it sounds like
// someone), named and brief voices get none, and questions about voices that
// no longer exist are closed.
func Reconcile(ctx context.Context, a *app.App, assetID string) error {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return err
	}
	voices, err := Voices(ctx, a, assetID, false)
	if err != nil {
		return err
	}
	projectName := ""
	if len(as.ProjectIDs) > 0 {
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), as.ProjectIDs[0], &p) == nil {
			projectName = p.Name
		}
	}
	known := projectPeople(ctx, a, as.ProjectIDs, voices)
	// Faces a person named that are on screen while a voice talks, and the
	// names voices say for themselves: suggestions, never names (D157).
	onScreen, said := faces.OnScreenWhileTalking(ctx, a, as.ID)
	cands, _, _ := candidates(ctx, a)
	// A kind of evidence that was often wrong here no longer makes MetaRush
	// suggest a name (D189): a voice that sounds like someone is then a pick.
	_, voiceMuted := todos.Muted(ctx, a)[models.BasisVoice]
	pids := as.ProjectIDs
	if pids == nil {
		pids = []string{}
	}
	var keep []string
	count := 0
	for _, v := range voices {
		if !v.Minor || v.Name != "" {
			count++
		}
	}
	for _, v := range voices {
		key := VoiceKey(v)
		if v.Name != "" {
			if _, err := todos.Close(ctx, a, key, models.ResolvedElsewhere, v.Name); err != nil {
				return err
			}
			continue
		}
		if v.Minor {
			continue // brief voices are listed but not asked about
		}
		keep = append(keep, key)
		frame := v.First
		if len(v.Samples) > 0 {
			frame = (v.Samples[0].From + v.Samples[0].To) / 2
		}
		t := models.Todo{Scope: "asset", AssetID: as.ID, ProjectIDs: pids, GroupKey: key, FreeText: true, Priority: 1,
			Context: models.TodoContext{AssetName: as.Name, AssetTitle: as.DisplayTitle(), ProjectName: projectName, Label: v.Label, Speaker: v.DisplayName(),
				Samples: v.Samples, Seconds: v.Seconds}}
		if as.Media.HasVideo {
			t.Context.FrameAt = &frame
		}
		title := as.DisplayTitle()
		heard := fmt.Sprintf("%s speaks for %s in “%s”.", v.DisplayName(), speakingTime(v.Seconds), title)
		if count > 1 {
			heard = fmt.Sprintf("We heard %d voices in “%s”. %s speaks for %s.", count, title, v.DisplayName(), speakingTime(v.Seconds))
		}
		if len(v.Suggestions) > 0 && !voiceMuted {
			best := v.Suggestions[0]
			t.Kind = models.TodoSpeakerMatch
			t.Question = fmt.Sprintf("Is this %s?", best.Name)
			t.Detail = heard + fmt.Sprintf(" It's probably %s (%d%% voice match). Listen and confirm, or tell us who it is. We'll remember the voice for your other videos.", best.Name, pct(best.Score))
			t.Context.PersonID, t.Context.PersonName, t.Context.Score = best.PersonID, best.Name, best.Score
			for i, m := range v.Suggestions {
				label := fmt.Sprintf("Yes, it's %s", m.Name)
				if i > 0 {
					label = fmt.Sprintf("%s (%d%% voice match)", m.Name, pct(m.Score))
				}
				t.Suggestions = append(t.Suggestions, models.TodoSuggestion{Label: label, Value: m.Name, PersonID: m.PersonID, Score: m.Score, Basis: []string{models.BasisVoice}})
			}
		} else {
			t.Kind = models.TodoSpeakerUnnamed
			t.Question = fmt.Sprintf("Who is %s in “%s”?", v.DisplayName(), title)
			t.Detail = heard + " Listen to a few seconds and type their name. We'll recognize their voice in your other videos."
			for _, m := range v.Suggestions { // only when going by the voice is muted
				if len(t.Suggestions) < 2 {
					t.Suggestions = append(t.Suggestions, models.TodoSuggestion{Label: fmt.Sprintf("%s (%d%% voice match)", m.Name, pct(m.Score)), Value: m.Name, PersonID: m.PersonID, Score: m.Score,
						Basis: []string{models.BasisVoice}})
				}
			}
			if len(v.Suggestions) > 0 {
				if s, ok := todos.Muted(ctx, a)[models.BasisVoice]; ok {
					t.Detail += " " + todos.MutedNote(s)
				}
			}
		}
		has := func(personID, name string) bool {
			for _, s := range t.Suggestions {
				if (personID != "" && s.PersonID == personID) || names.Fold(s.Value) == names.Fold(name) {
					return true
				}
			}
			return false
		}
		for _, p := range onScreen[v.Label] {
			if len(t.Suggestions) < 4 && !has(p.PersonID, p.Name) && !contains(v.Rejected, p.PersonID) {
				t.Suggestions = append(t.Suggestions, models.TodoSuggestion{Label: p.Name + " (on screen while this voice talks)", Value: p.Name, PersonID: p.PersonID, Basis: []string{models.BasisVoiceCue}})
			}
		}
		for _, sn := range said[v.Label] {
			id := ""
			var hits []string
			for _, c := range cands {
				if names.SameName(c.Name, sn.Name) {
					hits = append(hits, c.ID)
					if names.Fold(c.Name) == names.Fold(sn.Name) {
						hits = []string{c.ID}
						break
					}
				}
			}
			if len(hits) == 1 {
				id = hits[0]
			}
			if len(t.Suggestions) < 4 && !has(id, sn.Name) && (id == "" || !contains(v.Rejected, id)) {
				t.Suggestions = append(t.Suggestions, models.TodoSuggestion{Label: fmt.Sprintf("%s (says “%s”)", sn.Name, sn.Quote), Value: sn.Name, PersonID: id, New: id == "", Basis: []string{models.BasisSaid}})
			}
		}
		for _, p := range known {
			if len(t.Suggestions) >= 4 {
				break
			}
			dup := false
			for _, s := range t.Suggestions {
				if s.PersonID == p.ID {
					dup = true
				}
			}
			if !dup && !contains(v.Rejected, p.ID) {
				t.Suggestions = append(t.Suggestions, models.TodoSuggestion{Label: p.Name + " (in this project)", Value: p.Name, PersonID: p.ID})
			}
		}
		if _, err := todos.Upsert(ctx, a, t); err != nil {
			return err
		}
	}
	if _, err = todos.CloseStale(ctx, a, "speaker:"+as.ID+":", keep); err != nil {
		return err
	}
	// Faces alone on screen while a named voice speaks: "Is this …?" (D73).
	_, err = faces.Candidates(ctx, a, as.ID)
	return err
}

// projectPeople are people named in the asset's projects who are not already
// a voice of this asset (quick answers for "who is this?").
func projectPeople(ctx context.Context, a *app.App, projectIDs []string, voices []models.AssetSpeaker) []models.Person {
	if len(projectIDs) == 0 {
		return nil
	}
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"projectIds": bson.M{"$in": projectIDs}}, &assets, options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(500)); err != nil {
		return nil
	}
	ids := make([]string, 0, len(assets))
	for _, as := range assets {
		ids = append(ids, as.ID)
	}
	var vs []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": bson.M{"$in": ids}, "personId": bson.M{"$nin": bson.A{nil, ""}}}, &vs,
		options.Find().SetProjection(bson.M{"personId": 1})); err != nil {
		return nil
	}
	here := map[string]bool{}
	for _, v := range voices {
		if v.PersonID != "" {
			here[v.PersonID] = true
		}
	}
	seen := map[string]bool{}
	var pids []string
	for _, v := range vs {
		if !here[v.PersonID] && !seen[v.PersonID] {
			seen[v.PersonID] = true
			pids = append(pids, v.PersonID)
		}
	}
	if len(pids) == 0 {
		return nil
	}
	var ps []models.Person
	_ = a.DB.Find(ctx, store.CPeople, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": pids}, "mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &ps,
		options.Find().SetProjection(bson.M{"name": 1}).SetSort(bson.D{{Key: "name", Value: 1}}))
	return ps
}

// DuplicateThreshold: two people whose voices are at least this similar are
// probably one person named twice.
const DuplicateThreshold = 0.70

// ReconcileDuplicates asks whether personID and someone with a very similar
// voice are the same person, and closes the questions that no longer apply
// (their voices stopped sounding alike, one of them has no voice profile any
// more, or a person already said they are different). The question is keyed
// on the two people, never on a detection run, and an answer is final.
func ReconcileDuplicates(ctx context.Context, a *app.App, personID string) error {
	p, err := loadPerson(ctx, a, personID)
	if err != nil {
		return err
	}
	people, err := LoadProfiles(ctx, a)
	if err != nil {
		return err
	}
	var me *voice.Profile
	for i := range people {
		if people[i].ID == p.ID {
			me = &people[i]
		}
	}
	if me == nil {
		// No voice profile: nobody can sound like them.
		var open []models.Todo
		if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"kind": models.TodoDuplicatePerson, "state": models.TodoOpen,
			"$or": bson.A{bson.M{"context.personId": p.ID}, bson.M{"context.otherId": p.ID}}}, &open); err != nil {
			return err
		}
		for _, t := range open {
			_, _ = todos.Close(ctx, a, t.GroupKey, models.ResolvedObsolete, "")
		}
		return nil
	}
	for _, o := range people {
		if o.ID == p.ID {
			continue
		}
		key := duplicateKey(p.ID, o.ID)
		if contains(p.Distinct, o.ID) {
			_, _ = todos.Close(ctx, a, key, models.ResolvedElsewhere, "different people")
			continue
		}
		s := voice.Score(voice.Cluster{Centroid: me.Centroid}, o).Score
		if s < DuplicateThreshold {
			_, _ = todos.Close(ctx, a, key, models.ResolvedObsolete, "")
			continue
		}
		t := models.Todo{Kind: models.TodoDuplicatePerson, Scope: "workspace", GroupKey: key, ProjectIDs: []string{}, Priority: 2,
			Question: fmt.Sprintf("Are %s and %s the same person?", p.Name, o.Name),
			Detail:   fmt.Sprintf("Their voices sound very alike (%d%% voice match). If they are one person, we'll combine them so every video uses one name. You can change this later on the People page.", pct(s)),
			Context:  models.TodoContext{PersonID: p.ID, PersonName: p.Name, OtherID: o.ID, OtherName: o.Name, Score: s},
			Suggestions: []models.TodoSuggestion{
				{Label: "No, not the same", Value: "different"},
				{Label: "Yes, combine them", Value: "same"},
			}}
		if _, err := todos.Upsert(ctx, a, t); err != nil {
			return err
		}
	}
	return nil
}

func pct(s float64) int {
	if s < 0 {
		return 0
	}
	return int(s*100 + 0.5)
}

// speakingTime says how long someone talks, in plain words.
func speakingTime(sec float64) string {
	switch {
	case sec < 1.5:
		return "about a second"
	case sec < 60:
		return fmt.Sprintf("about %d seconds", int(sec+0.5))
	default:
		m := int(sec/60 + 0.5)
		if m == 1 {
			return "about a minute"
		}
		return fmt.Sprintf("about %d minutes", m)
	}
}

// Summary says in plain words who speaks in an asset (CLI and dashboard copy);
// assetName is how the clip is shown (its title).
func Summary(assetName string, voices []models.AssetSpeaker) string {
	var named, unnamed []string
	brief := 0
	for _, v := range voices {
		switch {
		case v.Name != "":
			named = append(named, v.Name)
		case v.Minor:
			brief++
		default:
			unnamed = append(unnamed, v.DisplayName())
		}
	}
	n := len(named) + len(unnamed)
	if n == 0 && brief == 0 {
		return fmt.Sprintf("No voices found in “%s”.", assetName)
	}
	var b strings.Builder
	if n == 1 {
		fmt.Fprintf(&b, "One voice in “%s”", assetName)
	} else {
		fmt.Fprintf(&b, "%d voices in “%s”", n, assetName)
	}
	if len(named) > 0 {
		fmt.Fprintf(&b, ": %s", strings.Join(named, ", "))
	}
	if len(unnamed) > 0 {
		if len(named) > 0 {
			b.WriteString(", plus")
		} else {
			b.WriteString(":")
		}
		fmt.Fprintf(&b, " %s not named yet", strings.Join(unnamed, ", "))
	}
	b.WriteString(".")
	switch {
	case brief == 1:
		b.WriteString(" One very brief voice is too short to tell who it is.")
	case brief > 1:
		fmt.Fprintf(&b, " %d very brief voices are too short to tell who they are.", brief)
	}
	return b.String()
}
