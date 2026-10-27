package qa

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// GeneratedClip is what QA knows about an AI-generated clip the cut shows (D127).
type GeneratedClip struct {
	Provider string // Google Gemini
	Likeness bool   // it shows a real person's likeness (with their consent)
	// Presenter: a presenter reads lines on camera in it (D277).
	Presenter bool
}

// aiGenerated notes, for the person's information, where the video shows
// AI-generated footage and that platforms ask people to say so: one note for
// the whole video, never a warning or an error. Export notes carry the same
// list with what each site asks for.
func (r *run) aiGenerated() {
	if len(r.in.Generated) == 0 {
		return
	}
	type span struct{ from, to float64 }
	var spans []span
	var clips []string
	providers := map[string]bool{}
	likeness := false
	presenterSpans := 0
	for _, c := range r.plan.Clips {
		g, ok := r.in.Generated[c.AssetID]
		if !ok {
			continue
		}
		from := float64(c.From) / r.fps
		spans = append(spans, span{from, float64(c.From+c.DurationInFrames) / r.fps})
		if g.Presenter {
			presenterSpans++
		}
		if !slices.Contains(clips, c.ID) {
			clips = append(clips, c.ID)
		}
		if g.Provider != "" {
			providers[g.Provider] = true
		}
		likeness = likeness || g.Likeness
	}
	if len(spans) == 0 {
		return
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
	var when []string
	for i, s := range spans {
		if i == 4 {
			when = append(when, "…")
			break
		}
		when = append(when, clock(s.from)+" to "+clock(s.to))
	}
	var names []string
	for p := range providers {
		names = append(names, p)
	}
	sort.Strings(names)
	shots := "1 AI-generated shot"
	if len(spans) != 1 {
		shots = fmt.Sprintf("%d AI-generated shots", len(spans))
	}
	switch {
	case presenterSpans == len(spans) && presenterSpans == 1:
		shots = "an AI presenter reading lines on camera"
	case presenterSpans == len(spans):
		shots = fmt.Sprintf("an AI presenter reading lines on camera, %d times", presenterSpans)
	case presenterSpans > 0:
		shots += fmt.Sprintf(", %d of them an AI presenter reading lines", presenterSpans)
	}
	msg := fmt.Sprintf("This video has %s (%s)", shots, strings.Join(when, ", "))
	if len(names) > 0 {
		msg += ", made with " + strings.Join(names, " and ")
	}
	msg += ". Say so when you post it: YouTube, TikTok, Instagram and Facebook ask for it, and the notes saved with each export list what to do."
	if likeness {
		msg += " It shows a real person's likeness made with AI, so always say so."
	}
	if presenterSpans > 0 {
		msg += " A presenter made with AI is never the person speaking live: say that too."
	}
	r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "ai_generated", Basis: models.QAHeuristic, From: spans[0].from, To: spans[len(spans)-1].to, ClipIDs: clips, Message: msg})
}

// SaidFacts is a change of words the cut plays (D311): where (seconds on
// the cut's timeline), who says what in place of what, and why it may no
// longer be heard ("" while its consent holds), and the same for a mouth
// matched to the new words when that plays too.
type SaidFacts struct {
	ChangeID           string
	From, To           float64
	Who, Words, Before string
	// Nested: a cut played in this one has it.
	Nested         bool
	Withdrawn      string
	Mouth          bool
	MouthWithdrawn string
}

// changedWords notes, for the person's information, every place the video
// says words someone never said: words changed in the speaker's own voice
// copy (D311), with what was said and what is said now. Like AI-generated
// footage it is never a warning; platforms ask people to say so. Words (or a
// matched mouth) whose consent is no longer in force are a problem: the
// version can't go out until they come out of the cut (D309).
func (r *run) changedWords() {
	for _, c := range r.said() {
		who := c.Who
		if who == "" {
			who = "the speaker"
		}
		f := &models.QAFinding{Severity: models.QAInfo, Kind: "changed_words", Basis: models.QAHeuristic, From: c.From, To: c.To}
		switch {
		case c.Words == "": // a nested cut's, not looked up
			f.Message = fmt.Sprintf("At %s a cut played in this one says words changed with AI in the speaker's own voice copy. Say so when you post it: the notes saved with each export list it.", clock(c.From))
		case c.Withdrawn != "":
			f.Severity, f.Kind = models.QAError, "changed_words_withdrawn"
			f.Message = fmt.Sprintf("At %s %s says “%s”, words changed with AI in their voice copy, but %s Take them out of the cut (Undo) and make a new version: it can't go out as it is.",
				clock(c.From), who, c.Words, lowerFirst(c.Withdrawn))
		case c.Mouth && c.MouthWithdrawn != "":
			f.Severity, f.Kind = models.QAError, "changed_words_withdrawn"
			f.Message = fmt.Sprintf("At %s the mouth is matched to “%s” with AI, but %s Take the change out of the cut (Undo) and make a new version: it can't go out as it is.",
				clock(c.From), c.Words, lowerFirst(c.MouthWithdrawn))
		default:
			f.Message = fmt.Sprintf("At %s %s says “%s”, made with AI in their own voice copy with their consent; they said “%s”. Say so when you post it: the notes saved with each export list it.",
				clock(c.From), who, c.Words, c.Before)
		}
		if c.Nested && c.Words != "" {
			f.Message = strings.Replace(f.Message, "”, ", "” (in a cut played in this one), ", 1)
		}
		r.add(f)
	}
}

// said is the changed words the cut plays: as given, or the cut's own as
// its mix says (once each, where first heard).
func (r *run) said() []SaidFacts {
	if r.in.Said != nil {
		return r.in.Said
	}
	byID := map[string]editspec.SaidSound{}
	if r.in.Spec != nil && r.in.Spec.Mix != nil {
		for _, c := range r.in.Spec.Mix.Said {
			byID[c.ID] = c
		}
	}
	var out []SaidFacts
	for _, p := range editspec.SaidPlays(r.plan) {
		c, ok := byID[p.ChangeID]
		if !ok || p.Nested != "" {
			out = append(out, SaidFacts{ChangeID: p.ChangeID, From: p.From, To: p.To, Nested: p.Nested != ""})
			continue
		}
		out = append(out, SaidFacts{ChangeID: c.ID, From: p.From, To: p.To, Who: c.Who, Words: c.Words, Before: c.Before})
	}
	return out
}
