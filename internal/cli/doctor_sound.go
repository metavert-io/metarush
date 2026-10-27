package cli

import (
	"fmt"
	"runtime"
	"strconv"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/sound"
	"github.com/metavert-io/metarush/internal/tools"
)

// soundChecks reports what makes voices clearer on this computer (D88–D91):
// whether clean and studio are set up and, for studio, how fast it measured
// when it was set up. Versions and folders are the technical lines (--details,
// --json); the plain lines say what a person can do.
func soundChecks(cfg *config.Config, ts *tools.Set, add addCheck) {
	comp := &sound.Components{Home: cfg.Home(), Tools: ts}
	for _, st := range comp.Status() {
		switch st.Name {
		case sound.ComponentClean:
			pinned := fmt.Sprintf("%s and the voice clarity score %s", st.Version, sound.DNSMOSVersion)
			switch {
			case st.Installed:
				add("clearer voices", "clean", "ok", "ready: "+pinned+" in "+st.Path, "",
					"Clean is set up: voices are made clearer on this computer, fast and free")
			case !comp.CleanAvailable():
				add("clearer voices", "clean", "info", st.Problem+" ("+runtime.GOOS+"/"+runtime.GOARCH+")", "",
					"Clean can't run on this kind of computer; Cloud can still make voices clearer")
			default:
				add("clearer voices", "clean", "info", "not set up ("+st.Problem+"): "+pinned+", "+st.Size+", set up on first use", "metarush sound setup clean",
					"Clean isn't set up yet: it sets itself up the first time a voice is made clearer ("+st.Size+")")
			}
		case sound.ComponentStudio:
			recorded := comp.StudioRecorded()
			switch {
			case st.Installed:
				// What making a voice clearer takes per second of speech, as the
				// Sound tab estimates it: the cleaned voice, the rebuild, then
				// every word checked.
				per := 2*st.Speed + 0.8
				add("clearer voices", "studio", "ok", fmt.Sprintf("ready: %s on %s, the voice model measured %s s of work per second of audio at setup; %s", st.Version, st.Device,
					strconv.FormatFloat(st.Speed, 'f', 2, 64), st.Path), "",
					fmt.Sprintf("Studio is set up and uses %s: about %s seconds of work for each second of speech (measured when it was set up)", deviceWords(st.Device), strconv.FormatFloat(per, 'f', 1, 64)))
			case recorded != "" && recorded != sound.StudioVersion:
				add("clearer voices", "studio", "warn", fmt.Sprintf("set up for %s; this MetaRush uses %s (%s)", recorded, sound.StudioVersion, st.Path), "metarush sound setup studio",
					"Studio was set up for an earlier version of MetaRush, so it isn't used now", "Set it up again on the Sound tab (about 1.8 GB).")
			case recorded != "":
				add("clearer voices", "studio", "warn", st.Problem+" ("+st.Version+", "+st.Path+")", "metarush sound setup studio",
					"Studio is set up only in part, so it can't be used", "Set it up again on the Sound tab.")
			default:
				add("clearer voices", "studio", "info", "not set up: "+st.Version+", "+st.Size, "metarush sound setup studio",
					"Studio isn't set up: it gives voices a studio sound, after a one-time download of "+st.Size, "Set it up on the Sound tab if you want it.")
			}
		}
	}
}

// deviceWords names where studio runs.
func deviceWords(device string) string {
	if device == "mps" {
		return "the graphics chip"
	}
	return "the processor"
}
