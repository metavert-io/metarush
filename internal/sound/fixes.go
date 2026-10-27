package sound

import (
	"context"
	"fmt"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/fixes"
)

// "Clean up voices" for a whole recording (D152, D153) is the clean engine of
// the Sound tab: DeepFilterNet and the measured polish (D89), set up on first
// use like the Sound tab's.
func init() { fixes.CleanVoices = cleanWhole }

func cleanWhole(ctx context.Context, a *app.App, in, out string, progress func(string)) (string, error) {
	eng := newEngine(a, "")
	if ok, _ := eng.Comp.Ready(ComponentClean); !ok {
		if !eng.Comp.CleanAvailable() {
			return "", fmt.Errorf("the voice cleaner isn't available for this computer")
		}
		if progress != nil {
			progress("setting up the voice cleaner (about 30 MB, once)")
		}
		if err := eng.Comp.InstallClean(ctx, func(done, total int, msg string) error { return nil }); err != nil {
			if apperr.Is(err, apperr.ToolMissing) {
				return "", err // left out by a person: its own words and the one step (D325)
			}
			return "", fmt.Errorf("the voice cleaner couldn't be set up: %w", err)
		}
		eng.Comp.Status()
	}
	if progress != nil {
		progress("cleaning up the voices")
	}
	w, err := eng.Process(ctx, "clean", in, 0, 0)
	if err != nil {
		return "", err
	}
	if err := WriteWAV(out, w.Audio, true); err != nil {
		return "", err
	}
	return eng.EngineVersion(ctx, "clean"), nil
}
