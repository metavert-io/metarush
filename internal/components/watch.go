package components

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// Watch makes a running MetaRush see components added or removed elsewhere —
// `metarush components add` in a terminal, setup, Homebrew — (D324): when
// config.yaml (a download's tools.* setting), the choices file or a folder
// programs are looked for in (PATH's, Homebrew's: a program installed or
// removed there changes it) changes, it looks again for its programs and
// calls changed (the daemon's background work takes on what it can do now).
// every is how often it looks.
func Watch(ctx context.Context, a *app.App, every time.Duration, changed func()) {
	stamp := func() string {
		s := ""
		watched := append([]string{config.Path(a.Cfg.Home()), choice.Path(a.Cfg.Home())}, tools.SearchDirs()...)
		for _, p := range watched {
			if st, err := os.Stat(p); err == nil {
				s += st.ModTime().String() + "|" + fmt.Sprint(st.Size()) + ";"
			} else {
				s += "-;"
			}
		}
		return s
	}
	last := stamp()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := stamp()
		if now == last {
			continue
		}
		last = now
		For(a).rescan()
		if changed != nil {
			changed()
		}
	}
}
