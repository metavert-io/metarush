package sound

import (
	"context"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/voiceover"
)

func init() {
	ops.Def("sound.show", "Show a cut's sound in plain words: how loud each part and each person is, clearer voices (and how much they helped), the automatic help, music and the ways to make voices clearer on this computer", ops.Flags{},
		func(ctx context.Context, a *app.App, in *voiceover.RevisionIn) (*ShowOut, error) {
			return soundShow(ctx, a, in.RevisionID)
		})
	ops.Def("sound.levels", "Change how loud things are: camera sound, narration, music, effects, single people over their speech, single clips, music getting quieter when someone talks, voices evened out, smooth cuts, overall loudness and each music track's level (checks the plan; apply makes it a new version)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *LevelsIn) (*LevelsOut, error) {
			return soundLevels(ctx, a, in)
		})
	ops.Def("sound.preview", "Hear a sound change before saving it — or every voice as recorded (A/B): the plan the preview plays and the mix in plain words (nothing is saved)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *LevelsIn) (*PreviewOut, error) {
			return soundPreview(ctx, a, in)
		})
	ops.Def("sound.enhance", "Make a voice clearer — a person over their speech, the narration, a recording or everyone — on this computer (clean, studio) or with ElevenLabs (cloud, priced first and budgeted); the recordings are kept, results are cached; estimate says what it takes (checks the plan; apply makes it a new version)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *EnhanceIn) (*EnhanceOut, error) {
			return runEnhance(ctx, a, in)
		})
	ops.Def("sound.compare", "Compare voices before and after they were made clearer: the voice clarity score, the background noise and warnings when it went too far", ops.Flags{},
		func(ctx context.Context, a *app.App, in *CompareIn) (*CompareOut, error) {
			return soundCompare(ctx, a, in)
		})
	ops.Def("sound.music.add", "Put music under a cut: timed to the video by default (it starts with it and ends with it on a beat, with a smooth fade; a shorter track fades early or loops) or placed by hand; its level and whether it gets quieter when someone talks (checks the plan; apply makes it a new version)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *MusicAddIn) (*MusicOut, error) { return musicAdd(ctx, a, in) })
	ops.Def("sound.music.set", "Change a music track: fit it to the video or time it by hand (in, out and where it starts), its level, getting quieter when someone talks, looping, or take it out (checks the plan; apply makes it a new version)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *MusicSetIn) (*MusicOut, error) { return musicSet(ctx, a, in) })
	ops.Def("sound.setup", "Set up clearer voices on this computer: the voice cleaner and the clarity score (clean, about 30 MB) or studio quality (about 1.8 GB) — people only", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *SetupIn) (*SetupOut, error) { return soundSetup(ctx, a, in) })
}

func stepTracked(ctx context.Context, done, total int, msg string) error {
	return voiceover.Step(ctx, done, total, msg)
}

func trackSetupWork(ctx context.Context, a *app.App, run func(context.Context) (*SetupOut, error)) (*SetupOut, error) {
	return voiceover.TrackWork(ctx, a, ActionSetup, "", "", run)
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("sound.enhance",
		ops.Offering{ID: "clearer-voices", Group: "fix", Title: "Clearer voices", Input: map[string]any{"mode": "clean"},
			Does:  "Makes voices clearer on this computer, with less room and background noise; free.",
			Words: []string{"background noise", "echo", "improve audio"}, Needs: ops.Needs{Works: []string{"editing"}}},
		ops.Offering{ID: "studio-voices", Group: "fix", Title: "Studio voices", Input: map[string]any{"mode": "studio"},
			Does:  "Makes voices sound as if recorded in a studio, on this computer: slower, and stronger than clearer voices.",
			Words: []string{"podcast sound", "studio quality"}, Needs: ops.Needs{Works: []string{"editing"}, Setup: "studio-voices"}},
		ops.Offering{ID: "cloud-voices", Group: "fix", Title: "Voices cleaned in the cloud", Input: map[string]any{"mode": "cloud"},
			Does:  "Separates voices from everything else with ElevenLabs; priced first.",
			Words: []string{"voice isolation", "isolate the voice"}, Needs: ops.Needs{Keys: []string{"elevenlabs"}, Priced: true, Leaves: "the voices' sound leaves this computer"}})
	ops.Offer("sound.music.add", ops.Offering{ID: "fitted-music", Group: "make", Title: "Music fitted to the cut",
		Does:  "Puts music under the cut that ends on a phrase or a beat and gets quieter when someone talks.",
		Words: []string{"background music", "soundtrack"}, Needs: ops.Needs{Works: []string{"editing"}}})
	ops.RegisterReady("studio-voices", func(_ context.Context, a *app.App) (bool, string) {
		return StudioReady(a), "the studio voice cleaner, set up once by a person on this computer"
	})
}
