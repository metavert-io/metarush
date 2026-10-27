package qa

import (
	"github.com/metavert-io/metarush/internal/models"
)

// CaptionWord is one word the caption check lists (D148): the computer wasn't
// sure of it ("unsure"), a name not on the spelling lists ("name"), or a word
// fixed in the transcript since the version was made ("fixed": the video
// still says the old one).
type CaptionWord struct {
	Kind  string
	Says  string  // plain words
	At    float64 // seconds into the video, the first time
	Times int
}

// captionWords lists the caption check's words: fixed words are worth
// fixing in a new version (a warning); unsure words and names are notes to
// look at before the video goes out.
func (r *run) captionWords() {
	if len(r.in.CaptionWords) == 0 {
		r.check("caption_words", models.QAHeuristic, "")
		return
	}
	for _, w := range r.in.CaptionWords {
		sev, kind := models.QAInfo, "caption_"+w.Kind
		if w.Kind == "fixed" {
			sev = models.QAWarning
		}
		r.add(&models.QAFinding{Severity: sev, Kind: kind, Basis: models.QAHeuristic, From: w.At, To: w.At, Message: w.Says})
	}
	r.check("caption_words", models.QAHeuristic, "", "caption_fixed")
}
