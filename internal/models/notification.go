package models

import "time"

// NotificationPrefix is the ID prefix of notifications (ntf_…).
const NotificationPrefix = "ntf"

// Notification kinds: one per meaningful outcome.
const (
	NotifyFileReady         = "file_ready"          // every derivative job of an uploaded file is terminal
	NotifyRenderFinished    = "render_finished"     // a render is available
	NotifyRenderFailed      = "render_failed"       // a render failed for good
	NotifyQADone            = "qa_done"             // a QA report was written
	NotifyAssistantProposal = "assistant_proposal"  // the dashboard assistant wrote proposals
	NotifyDuplicateSkipped  = "duplicate_skipped"   // a file was identical to an existing asset
	NotifyBatteryPaused     = "battery_paused"      // a worker started deferring heavy jobs for battery
	NotifyUploadInterrupted = "upload_interrupted"  // a browser upload stopped before finishing
	NotifySpeakers          = "speakers"            // a speaker detection someone asked for finished or failed
	NotifyRecognized        = "speakers_recognized" // a person's voice was recognized automatically in other videos (check it)
	NotifyNarrationReady    = "narration_ready"     // narration work (scratch or AI voice, cleanup, a placed recording, a voice copy) is ready to review
	NotifyNarrationFailed   = "narration_failed"    // narration work could not be done
	NotifyImportFailed      = "import_failed"       // a link import (YouTube, TikTok, X) could not be done
	NotifyOnScreen          = "on_screen"           // looking at what's on screen in a clip (someone asked) finished or failed
	NotifyDescribed         = "scenes_described"    // Claude Code wrote scene descriptions for a clip (or could not)
	NotifyGenerationReady   = "generation_ready"    // a generated video (Google Gemini) is ready in Sources
	NotifyGenerationFailed  = "generation_failed"   // a generated video could not be made (or the provider declined it)
	NotifyFixesMissed       = "fixes_missed"        // a clip was transcribed again and some of a person's fixes didn't carry over (D149)
	NotifyRemotionWaiting   = "remotion_waiting"    // a version of a Remotion source waits for a person's approval (D172)
	NotifyContentSettings   = "content_settings"    // a project's Content Settings changed by themselves: the project it followed is gone (D192)
	NotifyRecipeRun         = "recipe_run"          // a run of a recipe waits for someone, is ready for review, or stopped (D204)
	NotifyRecipePlanned     = "recipe_planned"      // a recipe planned a run by itself (new files, a weekly time); a person runs it (D218)
	NotifyAbilityWaiting    = "ability_waiting"     // a version of an ability waits for an owner's approval (D231)
	NotifyAbilityRun        = "ability_run"         // a run of an ability finished or stopped (D234)
	NotifyAutoSpent         = "auto_spent"          // what Auto spent by itself today, one note a day (D241)
	NotifyRecordingExpiring = "recording_expiring"  // a screen recording that didn't finish will have its pieces removed (D258)
	NotifyOutsideComments   = "outside_comments"    // people outside the workspace commented through a review link (D315)
	NotifyReviewLinkEnded   = "review_link_ended"   // a review link ended by itself: the media moved, rules or consent changed (D318)
	NotifyPublishStopped    = "publish_stopped"     // a post MetaRush stopped part-way couldn't go on (rules changed, kept failing) (D305)
	NotifyLateWrites        = "late_writes"         // something was written into the old database after the workspace moved (D356)
)

// Notification is a persistent, per-person message about an outcome. The
// recipient is the person who initiated the work; it shows on every machine
// that person uses. GroupKey deduplicates: one notification per outcome.
type Notification struct {
	ID          string         `bson:"_id" json:"id"`
	WorkspaceID string         `bson:"workspaceId" json:"workspaceId"`
	UserID      string         `bson:"userId" json:"userId"`
	Kind        string         `bson:"kind" json:"kind" jsonschema:"enum=file_ready,enum=render_finished,enum=render_failed,enum=qa_done,enum=assistant_proposal,enum=duplicate_skipped,enum=battery_paused,enum=upload_interrupted,enum=speakers,enum=speakers_recognized,enum=narration_ready,enum=narration_failed,enum=import_failed,enum=on_screen,enum=scenes_described,enum=generation_ready,enum=generation_failed,enum=fixes_missed,enum=remotion_waiting,enum=signin_locked,enum=content_settings,enum=recipe_run,enum=recipe_planned,enum=ability_waiting,enum=ability_run,enum=auto_spent,enum=recording_expiring,enum=publish_stopped,enum=outside_comments,enum=review_link_ended,enum=late_writes"`
	GroupKey    string         `bson:"groupKey" json:"groupKey"`
	Tone        string         `bson:"tone" json:"tone" jsonschema:"enum=success,enum=error,enum=warning,enum=info"`
	Title       string         `bson:"title" json:"title"`
	Body        string         `bson:"body,omitempty" json:"body,omitempty"`
	Link        string         `bson:"link,omitempty" json:"link,omitempty" jsonschema:"description=Dashboard path to open (deep link)."`
	ProjectID   string         `bson:"projectId,omitempty" json:"projectId,omitempty"`
	AssetID     string         `bson:"assetId,omitempty" json:"assetId,omitempty"`
	RenderID    string         `bson:"renderId,omitempty" json:"renderId,omitempty"`
	JobID       string         `bson:"jobId,omitempty" json:"jobId,omitempty"`
	Details     map[string]any `bson:"details,omitempty" json:"details,omitempty"`
	Read        bool           `bson:"read" json:"read"`
	ReadAt      *time.Time     `bson:"readAt,omitempty" json:"readAt,omitempty"`
	Machine     string         `bson:"machine,omitempty" json:"machine,omitempty" jsonschema:"description=Installation name that produced the outcome."`
	CreatedAt   time.Time      `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time      `bson:"updatedAt" json:"updatedAt"`
}
