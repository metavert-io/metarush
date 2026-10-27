package sound

import (
	"context"
	"os"
	"runtime"

	"github.com/metavert-io/metarush/internal/components"
)

// Clearer and studio voices are components (D324): the registry asks this
// package whether they are here, sets them up and removes them.
func init() {
	comp := func(e *components.Env) *Components { return &Components{Home: e.Home(), Tools: e.Tools} }
	progress := func(say func(string)) Progress {
		return func(_, _ int, msg string) error {
			if msg != "" {
				say(msg)
			}
			return nil
		}
	}
	components.Provide("clean-voice", components.Provided{
		Detect: func(_ context.Context, e *components.Env) components.Found {
			c := comp(e)
			st := c.cleanStatus()
			f := components.Found{Ready: st.Installed, Version: st.Version, Path: st.Path}
			if !c.CleanAvailable() {
				f.NotHere = "The voice cleaner has no build for this kind of computer; the cloud can still make voices clearer."
			}
			return f
		},
		Step: func(ctx context.Context, e *components.Env, say func(string)) error {
			return comp(e).InstallClean(ctx, progress(say))
		},
		Remove: func(_ context.Context, e *components.Env) error {
			c := comp(e)
			if err := os.RemoveAll(c.dir("voice-cleaner")); err != nil {
				return err
			}
			return os.RemoveAll(c.dir("voice-score"))
		},
	})
	components.Provide("studio-voice", components.Provided{
		Detect: func(_ context.Context, e *components.Env) components.Found {
			c := comp(e)
			st := c.studioStatus()
			f := components.Found{Ready: st.Installed, Version: st.Version, Path: st.Path, Detail: st.Device}
			switch {
			case runtime.GOOS == "darwin" && runtime.GOARCH != "arm64":
				f.NotHere = "Studio voices need an Apple silicon Mac."
			case !st.Installed && c.StudioRecorded() != "":
				f.Partly = "It was set up only in part, or for an earlier MetaRush."
			}
			return f
		},
		Step: func(ctx context.Context, e *components.Env, say func(string)) error {
			_, err := comp(e).InstallStudio(ctx, progress(say))
			return err
		},
		Remove: func(_ context.Context, e *components.Env) error { return os.RemoveAll(comp(e).studioDir()) },
	})
}
