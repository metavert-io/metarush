package leave

import (
	"context"
	"slices"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
)

// AI services (D290): every request names what it sends — its words (a
// prompt, a script), pictures (frames of footage and looks show faces),
// voices (a clip's sound, a sample, a consent reading) and footage (a clip
// sent as video) — and goes to one provider. The plan asks quietly (an
// estimate is not an attempt), the request asks again just before it is
// sent, and that refusal is recorded.

// RoleKinds are the kinds one file sent with an AI request carries, by its
// role (models.GenReference.Role, aiservices' input roles): fromFootage when
// it was taken from a clip.
func RoleKinds(role string, fromFootage bool) []string {
	switch role {
	case "start-frame", "end-frame", "image", "picture":
		if fromFootage {
			return []string{models.LeaveFaces, models.LeavePictures}
		}
		return []string{models.LeavePictures}
	case "look-still":
		return []string{models.LeaveFaces, models.LeavePictures}
	case "look-clip", "input-video", "footage":
		// "footage": a presenter's training take, the person talking to the camera (D273).
		return []string{models.LeaveFootage, models.LeaveFaces, models.LeaveVoices}
	case "audio", "media", "sample", "consent":
		return []string{models.LeaveVoices}
	}
	return []string{models.LeaveFootage}
}

// FileKinds are the kinds one file sent carries, by its role and its type: a
// video (lip sync's part of a clip, D279) is footage, with faces and voices,
// whatever its role.
func FileKinds(role, mime string, fromFootage bool) []string {
	if strings.HasPrefix(mime, "video/") {
		return []string{models.LeaveFootage, models.LeaveFaces, models.LeaveVoices}
	}
	return RoleKinds(role, fromFootage)
}

// Kinds is what a request sends: its words and every file's kinds, in the
// order people read them.
func Kinds(words bool, files ...[]string) []string {
	var all []string
	if words {
		all = append(all, models.LeaveWords)
	}
	for _, f := range files {
		all = append(all, f...)
	}
	out := []string{}
	for _, k := range models.LeaveKinds {
		if slices.Contains(all, k) {
			out = append(out, k)
		}
	}
	return out
}

// GenerationKinds is what a recorded AI request sends.
func GenerationKinds(g *models.Generation) []string {
	var files [][]string
	for _, r := range g.References {
		files = append(files, FileKinds(r.Role, r.MIME, r.AssetID != ""))
	}
	own := g.Options != nil && g.Options.Presenter != "" && !g.Options.Stock
	files = append(files, PresenterKinds(g.Kind, own))
	return Kinds(g.Prompt != "", files...)
}

// PresenterKinds is what a presenter's work sends beyond its files' roles
// (D273): making one sends the person's face and voice (the consent reading
// is filmed), and a clip of a person's own presenter makes their face speak
// at the service — faces, whatever sound it reads. Stock presenters and
// other work: nothing more.
func PresenterKinds(kind string, ownPresenter bool) []string {
	switch {
	case kind == "presenter-make":
		return []string{models.LeaveFootage, models.LeaveFaces, models.LeaveVoices}
	case kind == "presenter" && ownPresenter:
		return []string{models.LeaveFaces}
	}
	return nil
}

// AI is a request to an AI service as a send.
func AI(provider, providerTitle string, kinds, projects []string, what string) Send {
	return Send{Place: models.PlaceAI, To: provider, ToWords: firstNonEmpty(providerTitle, provider), Kinds: kinds, Projects: projects, What: what}
}

// CheckGeneration asks again, just before a recorded request is sent,
// whether what it sends may go to its provider.
func CheckGeneration(ctx context.Context, a *app.App, g *models.Generation, projects []string) error {
	return Check(ctx, a, AI(g.Provider, g.ProviderTitle, GenerationKinds(g), projects, "AI work: "+firstNonEmpty(g.Kind, "video")))
}

type quietKey struct{}

// Quiet marks a context whose refusals are only said (an estimate, a plan):
// nothing is recorded, as nothing was about to leave.
func Quiet(ctx context.Context) context.Context { return context.WithValue(ctx, quietKey{}, true) }

func quiet(ctx context.Context) bool { q, _ := ctx.Value(quietKey{}).(bool); return q }

// IsQuiet reports a context marked Quiet (an estimate or a plan).
func IsQuiet(ctx context.Context) bool { return quiet(ctx) }
