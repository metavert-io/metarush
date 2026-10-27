package qa

import "github.com/metavert-io/metarush/internal/models"

// clearerVoices lists, for information, the voices this render plays made
// clearer and what that did, in plain words (measured on the clearer copies
// when they were made: the voice clarity score and the background level).
func (r *run) clearerVoices() {
	for _, s := range r.in.ClearerVoices {
		r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "clearer_voice", Basis: models.QAHeuristic, From: 0, To: r.total(), Message: s})
	}
}
