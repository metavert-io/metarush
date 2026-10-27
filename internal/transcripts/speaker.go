package transcripts

import (
	"context"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
)

// AssignSpeaker fixes who says a stretch of a clip ("this part is Lena, not
// Maya") through the speakers (the words move to that voice, lines split
// where the speaker changes, the voices learn only from what a person
// confirmed) and records it in the clip's history, so it can be undone. The
// fix is recorded as soon as the words have moved; the voices learn from it
// after (Clip.Then).
func AssignSpeaker(ctx context.Context, a *app.App, in speakers.AssignInput, projectID string) (*speakers.AssignResult, *models.TranscriptFix, error) {
	var res *speakers.AssignResult
	var fix *models.TranscriptFix
	_, err := Edit(ctx, a, in.AssetID, EditOptions{ProjectID: projectID, Any: true}, func(c *Clip) error {
		r, learn, err := speakers.AssignWords(ctx, a, in)
		if err != nil {
			return err
		}
		res = r
		c.Then(learn)
		if r.Moved == 0 {
			return nil // already theirs: a confirmation, nothing to undo
		}
		sf := &models.SpeakerFix{Before: r.Before, Label: r.Voice.Label, Unsure: in.Unsure, Run: r.Run, Voices: r.Voices}
		after := "someone not named yet"
		if r.Person != nil {
			sf.Name, sf.PersonID, after = r.Person.Name, r.Person.ID, r.Person.Name
		}
		f := c.record(models.TranscriptFix{Kind: models.FixSpeaker, From: round3(in.From), To: round3(in.To), Speaker: sf,
			BeforeText: whoHad(r.Before), AfterText: after})
		fix = &f
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return res, fix, nil
}

// whoHad names who had the words of a stretch.
func whoHad(runs []models.SpeakerRun) string {
	var names []string
	for _, r := range runs {
		n := r.Name
		if n == "" {
			n = models.SpeakerDisplayLabel(r.Label)
		}
		if n != "" && !containsFold(names, n) {
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}
