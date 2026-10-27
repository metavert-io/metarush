package sound

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Previews for the Claude Code panel's cards (D111): what a sound change
// would do, built and checked while nothing changes — no version is saved, no
// voice is processed or paid for, no music is linked to a project and no
// beats are analysed. The card then runs the op itself with apply when a
// person confirms it.

// Part reads a part of the mix the way people name it: camera (the clips' own
// sound), narration, music or effects.
func Part(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "source", "camera", "camera-sound", "clips", "dialogue":
		return "source", true
	case "voiceover", "narration", "narrator", "vo":
		return "voiceover", true
	case "music":
		return "music", true
	case "sfx", "effects", "fx":
		return "sfx", true
	}
	return "", false
}

// PreviewLevels is sound.levels without saving: the change in words and its
// checked plan.
func PreviewLevels(ctx context.Context, a *app.App, in LevelsIn) (*LevelsOut, error) {
	in.Apply = false
	return soundLevels(ctx, a, &in)
}

// PreviewEnhance says what making a voice clearer would take: how much
// speech, about how long, and for the cloud the credits, the budget and the
// plan's credits left. Nothing is made, set up or spent.
func PreviewEnhance(ctx context.Context, a *app.App, in EnhanceIn) (*EnhanceOut, error) {
	in.Estimate, in.Apply = true, false
	return runEnhance(ctx, a, &in)
}

// PreviewMusicSet is sound.music.set without saving (and without finding the
// beats of music fitted to the video for the first time).
func PreviewMusicSet(ctx context.Context, a *app.App, in MusicSetIn) (*MusicOut, error) {
	in.Apply = false
	return musicSetWith(ctx, a, &in, false)
}

// FindMusic finds a workspace audio file by ID or title, as sound.music.add
// does (the project's first).
func FindMusic(ctx context.Context, a *app.App, projectID, ref string) (*models.Asset, error) {
	return findAudioAsset(ctx, a, projectID, ref)
}

// StudioReady says whether studio quality is set up on this computer.
func StudioReady(a *app.App) bool {
	ok, _ := (&Components{Home: a.Cfg.Home(), Tools: a.Tools}).Ready(ComponentStudio)
	return ok
}

// SetupRunning says whether clearer voices are being set up on this computer
// right now (the Sound tab's "Set it up", a terminal, or a card), so nothing
// starts a second setup beside it.
func SetupRunning(ctx context.Context, a *app.App) bool {
	n, err := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "type": jobs.TypeVoiceover, "state": models.JobRunning,
		"payload.action": ActionSetup, "lease.owner": a.Cfg.Installation.ID, "$expr": bson.M{"$gt": bson.A{"$lease.expiresAt", "$$NOW"}}})
	return err == nil && n > 0
}
