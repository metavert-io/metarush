package mattes

import (
	"fmt"
	"syscall"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
)

// Room for making a cut-out on this computer: the clip when it isn't here
// yet, the matte and the cut-out — a WebM at up to about 12 Mbit/s with its
// matte, so 1.6 MB a second of the stretch — and a margin.
const (
	roomPerSecond = 1_600_000
	roomMargin    = 512 << 20
)

// freeBytes is the room left on the disk that holds dir (false: not known).
var freeBytes = func(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}

// roomNeeded is about how much room making the cut-out takes here.
func roomNeeded(a *app.App, m *models.Matte) int64 {
	need := int64(max(0, m.To-m.From)*roomPerSecond) + roomMargin
	if !a.Cache.Has(m.Media, m.MediaSize) {
		need += m.MediaSize
	}
	return need
}

// roomCheck says, before anything is made, when this computer hasn't the
// room for a cut-out: how much it needs and how much is free.
func roomCheck(a *app.App, m *models.Matte) error {
	free, ok := freeBytes(a.Cfg.Scratch.Dir)
	if !ok {
		return nil // it can't be told: the making says so if it runs out
	}
	if need := roomNeeded(a, m); free < need {
		return apperr.New(apperr.Storage, "There isn't room on this computer to make the cut-out: it needs about %s free, and %s is.", bytesWords(need), bytesWords(free)).
			WithHint("Make room on this computer (Settings → Storage can empty the copies of footage kept here), then make the cut-out again.").
			WithDetails(map[string]any{"needBytes": need, "freeBytes": free, "folder": a.Cfg.Scratch.Dir})
	}
	return nil
}

// bytesWords says a size plainly ("1.4 GB", "300 MB").
func bytesWords(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
}
