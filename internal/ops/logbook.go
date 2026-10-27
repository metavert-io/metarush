package ops

import (
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
)

// The activity log and notifications tell about failed jobs with the same
// plain advice job details show, and the log names job types the same way.
func init() {
	logbook.JobAdvice = adviceFor
	logbook.JobLabel = JobKindLabel
	notify.JobAdvice = adviceFor
}

// JobAdvice says what to do about a failed or canceled job, in plain words.
func JobAdvice(j *models.Job) string { return adviceFor(j) }

// JobKindLabel names a job type for people ("Preview copy", "Transcription").
func JobKindLabel(typ string) string { return orName(jobKindLabel[typ], typ) }

// JobVerb is what a job of this type is doing ("Making a preview copy of").
func JobVerb(typ string) string { return jobVerb(typ) }
