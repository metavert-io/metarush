package todos

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/names"
	"github.com/metavert-io/metarush/internal/store"
)

// Keeping score of the evidence (D189). Every answered question about a
// person records the names it offered, what each rested on (the voice heard
// while the face was alone on screen, the looks, a name shown or said, the
// voice) and whether the answer agreed. A kind of evidence that is right less
// than MuteBelow of the time, once it has MuteAfter answers in the
// workspace, is muted: it no longer makes MetaRush suggest a name (a name it
// points at can still be one of the quick picks).

const (
	// MuteAfter: a kind of evidence is judged once it has this many answers.
	MuteAfter = 10
	// MuteBelow: a kind right less often than this is muted.
	MuteBelow = 0.8
)

// Grade works out the guesses of a question about a person from its answer:
// the person it named (personID, or a new name), or no (the suggestion is
// not them; the other names it offered are left unjudged). Picks without
// evidence ("talks in this clip", "in this project") are not guesses.
func Grade(t *models.Todo, personID, name string, no bool) []models.TodoGuess {
	if !models.PersonQuestion(t.Kind) {
		return nil
	}
	var out []models.TodoGuess
	for i, s := range t.Suggestions {
		if len(s.Basis) == 0 || s.Value == "no" {
			continue
		}
		g := models.TodoGuess{Name: s.Value, PersonID: s.PersonID, Basis: slices.Clone(s.Basis)}
		if s.Value == "yes" || (i == 0 && t.Context.PersonName != "" && (s.PersonID != "" && s.PersonID == t.Context.PersonID || names.Fold(s.Value) == names.Fold(t.Context.PersonName))) {
			g.Main, g.Name = true, t.Context.PersonName
			if g.PersonID == "" {
				g.PersonID = t.Context.PersonID
			}
		}
		if slices.Contains(g.Basis, models.BasisVoiceCue) {
			g.Seconds = t.Context.Seconds
		}
		switch {
		case no && !g.Main:
			continue // "not the suggestion" says nothing about the others
		case no:
			g.Agreed = false
		case personID != "" && g.PersonID != "":
			g.Agreed = personID == g.PersonID
		default:
			g.Agreed = name != "" && names.Fold(name) == names.Fold(g.Name)
		}
		out = append(out, g)
	}
	return out
}

// Record keeps a question's guesses with it (and marks it scored).
func Record(ctx context.Context, a *app.App, todoID string, gs []models.TodoGuess) error {
	set := bson.M{"scored": true}
	if len(gs) > 0 {
		set["guesses"] = gs
	}
	if err := a.DB.Update(ctx, store.CTodos, a.WorkspaceID(), todoID, bson.M{"$set": set}); err != nil {
		return err
	}
	forget(a.WorkspaceID())
	return nil
}

// Scored records the guesses of an answered question about a person: Grade,
// then Record. Questions of other kinds are left alone.
func Scored(ctx context.Context, a *app.App, t *models.Todo, personID, name string, no bool) {
	if t == nil || !models.PersonQuestion(t.Kind) {
		return
	}
	_ = Record(ctx, a, t.ID, Grade(t, personID, name, no))
}

var (
	reTile  = regexp.MustCompile(`under this face`)
	reLower = regexp.MustCompile(`^A title shows`)
	reTitle = regexp.MustCompile(`^A title before`)
	reVoice = regexp.MustCompile(`only one on screen while .+ is talking|on screen by itself while .+ speaks`)
	reLooks = regexp.MustCompile(`looks like .+ in other clips`)
	rePick  = regexp.MustCompile(`\((on screen while this voice talks|says “[^”]*”|\d+% (voice )?match|talking then|shown in the video|said in the video|looks \d+% alike)\)$`)
)

// legacyBasis works out what the names of a question answered before D189
// rested on, from what it said (its evidence and labels).
func legacyBasis(t *models.Todo) {
	evidence := func(name, personID string) []string {
		var out []string
		add := func(b string) {
			if !slices.Contains(out, b) {
				out = append(out, b)
			}
		}
		for _, ev := range t.Context.Evidence {
			if (personID != "" && ev.PersonID != "" && ev.PersonID != personID) || (ev.PersonID == "" && names.Fold(ev.Name) != names.Fold(name)) {
				continue
			}
			switch ev.Kind {
			case "voice":
				add(models.BasisVoiceCue)
			case "looks":
				add(models.BasisLooks)
			case "said":
				add(models.BasisSaid)
			case "aside":
				add(models.BasisAside)
			case "shown":
				switch {
				case reTile.MatchString(ev.Text):
					add(models.BasisTile)
				case reLower.MatchString(ev.Text):
					add(models.BasisLower)
				case reTitle.MatchString(ev.Text):
					add(models.BasisTitle)
				}
			}
		}
		return out
	}
	for i := range t.Suggestions {
		s := &t.Suggestions[i]
		if len(s.Basis) > 0 || s.Value == "no" {
			continue
		}
		main := s.Value == "yes" || i == 0 && t.Context.PersonName != "" && strings.HasPrefix(s.Label, "Yes, it's ")
		switch t.Kind {
		case models.TodoFaceGroup:
			name, pid := s.Value, s.PersonID
			if main {
				name, pid = t.Context.PersonName, t.Context.PersonID
			}
			s.Basis = evidence(name, pid)
		case models.TodoFacePerson:
			if !main || strings.HasSuffix(t.GroupKey, ":recheck") {
				continue // "Is this really Ada?" asks about a face a person named
			}
			switch {
			case reVoice.MatchString(t.Detail):
				s.Basis = []string{models.BasisVoiceCue}
			case reLooks.MatchString(t.Detail):
				s.Basis = []string{models.BasisLooks}
			}
		case models.TodoSpeakerMatch, models.TodoSpeakerUnnamed:
			m := rePick.FindStringSubmatch(s.Label)
			switch {
			case main && t.Kind == models.TodoSpeakerMatch:
				s.Basis = []string{models.BasisVoice}
			case m == nil:
			case strings.HasPrefix(m[1], "on screen"):
				s.Basis = []string{models.BasisVoiceCue}
			case strings.HasPrefix(m[1], "says"):
				s.Basis = []string{models.BasisSaid}
			case strings.Contains(m[1], "match"):
				s.Basis = []string{models.BasisVoice}
			}
		}
	}
}

// Backfill scores the questions about people answered before MetaRush kept
// score (D189): what their names rested on is read from what they said.
// Safe to repeat: a question is scored once.
func Backfill(ctx context.Context, a *app.App) (int, error) {
	var ts []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"kind": bson.M{"$in": bson.A{models.TodoFaceGroup, models.TodoFacePerson, models.TodoSpeakerMatch, models.TodoSpeakerUnnamed}},
		"resolution": models.ResolvedAnswered, "scored": bson.M{"$ne": true}}, &ts); err != nil {
		return 0, err
	}
	n := 0
	for i := range ts {
		t := &ts[i]
		legacyBasis(t)
		answer := strings.TrimSpace(t.Answer)
		no := strings.EqualFold(answer, "no") || strings.EqualFold(answer, "no, someone else") || strings.EqualFold(answer, "n")
		yes := strings.EqualFold(answer, "yes") || strings.EqualFold(answer, "y")
		personID, name := "", answer
		if yes {
			personID, name = t.Context.PersonID, t.Context.PersonName
		}
		gs := Grade(t, personID, name, no)
		if err := Record(ctx, a, t.ID, gs); err != nil {
			return n, err
		}
		if len(gs) > 0 {
			n++
		}
	}
	return n, nil
}

// KindScore is how one kind of evidence has done in this workspace.
type KindScore struct {
	Kind string `json:"kind" jsonschema:"enum=voice_cue,enum=looks,enum=tile,enum=lower,enum=title,enum=said,enum=voice,enum=aside"`
	// Label says the kind in plain words.
	Label   string `json:"label"`
	Answers int    `json:"answers"`
	Right   int    `json:"right"`
	// Rate is Right of Answers (0 when there are none).
	Rate  float64 `json:"rate"`
	Muted bool    `json:"muted" jsonschema:"description=Right less than 80% of the time over at least 10 answers: no longer used to suggest a name (still a quick pick)."`
	// Older counts answers left out: a voice heard for less than the time
	// the voice needs now (from before D189).
	Older int    `json:"older,omitempty"`
	Words string `json:"words"`
}

// BasisLabel says a kind of evidence in plain words.
func BasisLabel(kind string) string {
	switch kind {
	case models.BasisVoiceCue:
		return "Who is talking while the face is alone on screen"
	case models.BasisLooks:
		return "How the face looks, next to faces you named"
	case models.BasisTile:
		return "The name a call app shows under the face"
	case models.BasisLower:
		return "A name in a title while they're on screen"
	case models.BasisTitle:
		return "A title just before they appear"
	case models.BasisSaid:
		return "A name said in the video"
	case models.BasisVoice:
		return "How the voice sounds, next to voices you named"
	case models.BasisAside:
		return "Left out when you named someone's other faces"
	}
	return kind
}

// basisGoing says a kind of evidence as a way of going about it, for a sentence.
func basisGoing(kind string) string {
	switch kind {
	case models.BasisVoiceCue:
		return "going by who is talking while a face is alone on screen"
	case models.BasisLooks:
		return "going by how faces look"
	case models.BasisTile:
		return "going by the names a call app shows"
	case models.BasisLower:
		return "going by names in titles"
	case models.BasisTitle:
		return "going by titles before someone appears"
	case models.BasisSaid:
		return "going by names said in the video"
	case models.BasisVoice:
		return "going by how voices sound"
	case models.BasisAside:
		return "going by faces left out of someone's other faces"
	}
	return "going by " + kind
}

// MutedNote says plainly that a kind of evidence is muted and why ("Going by
// who is talking … was right 4 of 12 times here, so …").
func MutedNote(s KindScore) string {
	g := basisGoing(s.Kind)
	return fmt.Sprintf("%s was right %d of %d times here, so MetaRush only offers those names as picks now.", strings.ToUpper(g[:1])+g[1:], s.Right, s.Answers)
}

// Scores tallies, per kind of evidence, how often the names it pointed at
// were right in this workspace's answers.
func Scores(ctx context.Context, a *app.App) ([]KindScore, error) {
	var ts []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"guesses.0": bson.M{"$exists": true}}, &ts, options.Find().SetProjection(bson.M{"guesses": 1})); err != nil {
		return nil, err
	}
	by := map[string]*KindScore{}
	for _, k := range models.BasisKinds {
		by[k] = &KindScore{Kind: k, Label: BasisLabel(k)}
	}
	for _, t := range ts {
		for _, g := range t.Guesses {
			for _, b := range g.Basis {
				s := by[b]
				if s == nil {
					continue
				}
				if b == models.BasisVoiceCue && g.Seconds > 0 && g.Seconds < models.VoiceCueSeconds {
					s.Older++
					continue
				}
				s.Answers++
				if g.Agreed {
					s.Right++
				}
			}
		}
	}
	out := make([]KindScore, 0, len(by))
	for _, k := range models.BasisKinds {
		s := by[k]
		if s.Answers > 0 {
			s.Rate = float64(s.Right) / float64(s.Answers)
		}
		s.Muted = s.Answers >= MuteAfter && s.Rate < MuteBelow
		switch {
		case s.Answers == 0:
			s.Words = "Not answered yet."
		case s.Muted:
			s.Words = fmt.Sprintf("Right %d of %d times: MetaRush no longer suggests names from it (it can still be a quick pick).", s.Right, s.Answers)
		case s.Answers < MuteAfter:
			s.Words = fmt.Sprintf("Right %d of %d times so far.", s.Right, s.Answers)
		default:
			s.Words = fmt.Sprintf("Right %d of %d times.", s.Right, s.Answers)
		}
		if s.Older > 0 {
			s.Words += fmt.Sprintf(" %d older %s left out (the voice was heard for less than %d seconds).", s.Older, plural(s.Older, "answer", "answers"), int(models.VoiceCueSeconds))
		}
		out = append(out, *s)
	}
	return out, nil
}

// scoreCache keeps each workspace's muted kinds for a few seconds: working
// out a clip's questions asks for them once per clip.
var scoreCache = struct {
	sync.Mutex
	at    map[string]time.Time
	muted map[string]map[string]KindScore
}{at: map[string]time.Time{}, muted: map[string]map[string]KindScore{}}

func forget(ws string) {
	scoreCache.Lock()
	delete(scoreCache.at, ws)
	scoreCache.Unlock()
}

// Muted are the kinds of evidence muted in this workspace, with their scores.
func Muted(ctx context.Context, a *app.App) map[string]KindScore {
	ws := a.WorkspaceID()
	scoreCache.Lock()
	if at, ok := scoreCache.at[ws]; ok && time.Since(at) < 5*time.Second {
		m := scoreCache.muted[ws]
		scoreCache.Unlock()
		return m
	}
	scoreCache.Unlock()
	out := map[string]KindScore{}
	scores, err := Scores(ctx, a)
	if err != nil {
		return out
	}
	for _, s := range scores {
		if s.Muted {
			out[s.Kind] = s
		}
	}
	scoreCache.Lock()
	scoreCache.at[ws], scoreCache.muted[ws] = time.Now(), out
	scoreCache.Unlock()
	return out
}

// CueOffAfter: after this many answers in a clip that said a face isn't
// someone whose voice was the evidence, their voice is no evidence there.
const CueOffAfter = 2

// CueOff are the people whose voice is no evidence for the faces of a clip
// (D189): two answers there said a face isn't them where it rested on their
// voice being heard while the face was alone on screen.
func CueOff(ctx context.Context, a *app.App, assetID string) map[string]bool {
	out := map[string]bool{}
	var ts []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"assetId": assetID, "guesses": bson.M{"$elemMatch": bson.M{"basis": models.BasisVoiceCue, "agreed": false}}}, &ts,
		options.Find().SetProjection(bson.M{"kind": 1, "guesses": 1})); err != nil {
		return out
	}
	n := map[string]int{}
	for _, t := range ts {
		if t.Kind != models.TodoFaceGroup && t.Kind != models.TodoFacePerson {
			continue
		}
		for _, g := range t.Guesses {
			if !g.Agreed && g.PersonID != "" && slices.Contains(g.Basis, models.BasisVoiceCue) {
				n[g.PersonID]++
			}
		}
	}
	for pid, k := range n {
		if k >= CueOffAfter {
			out[pid] = true
		}
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
