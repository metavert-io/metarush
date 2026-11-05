package mattes

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
)

// Room for making a cut-out on this computer, on each disk for what lands
// there: in the folder it works in, the matte and the cut-out being made (a
// WebM at up to about 12 Mbit/s with its matte: 1.6 MB a second of the
// stretch); where it keeps copies of footage, the clip when it isn't there
// yet and the cut-out it keeps; and a margin on each disk. Two that are one
// disk are counted together.
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

// diskOf names the disk that holds dir (false: not known): the device of the
// folder, or of the nearest one above it that exists.
var diskOf = func(dir string) (uint64, bool) {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if fi, err := os.Stat(d); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				return uint64(st.Dev), true
			}
			return 0, false
		}
		if d == filepath.Dir(d) {
			return 0, false
		}
	}
}

// roomPart is what lands in one folder.
type roomPart struct {
	dir   string
	where string // in words: "where it works", "where it keeps copies of footage"
	need  int64
}

// roomParts are what making the cut-out puts where, here.
func roomParts(a *app.App, m *models.Matte) []roomPart {
	cutOut := int64(max(0, m.To-m.From) * roomPerSecond)
	parts := []roomPart{{dir: a.Cfg.Scratch.Dir, where: "where it works", need: cutOut}}
	if a.Cache != nil {
		keep := cutOut // the cut-out, kept as a copy here once made
		if !a.Cache.Has(m.Media, m.MediaSize) {
			keep += m.MediaSize // the clip, fetched here first
		}
		parts = append(parts, roomPart{dir: a.Cache.Dir, where: "where it keeps copies of footage", need: keep})
	}
	return parts
}

// roomCheck says, before anything is made, when a disk of this computer
// hasn't the room for its part of a cut-out: how much that disk needs and
// how much it has free.
func roomCheck(a *app.App, m *models.Matte) error {
	type disk struct {
		dir    string
		wheres []string
		need   int64
	}
	var disks []*disk
	byDev := map[uint64]*disk{}
	for _, p := range roomParts(a, m) {
		if p.dir == "" {
			continue
		}
		dev, ok := diskOf(p.dir)
		if d := byDev[dev]; ok && d != nil {
			d.need += p.need
			d.wheres = append(d.wheres, p.where)
			continue
		}
		d := &disk{dir: p.dir, wheres: []string{p.where}, need: p.need + roomMargin}
		disks = append(disks, d)
		if ok {
			byDev[dev] = d
		}
	}
	for _, d := range disks {
		free, ok := freeBytes(d.dir)
		if !ok || free >= d.need {
			continue // can't be told: the making says so if it runs out
		}
		where := ""
		if len(disks) > 1 {
			where = " on the disk " + joinAnd(d.wheres)
		}
		return apperr.New(apperr.Storage, "There isn't room on this computer to make the cut-out: it needs about %s free%s, and %s is.", bytesWords(d.need), where, bytesWords(free)).
			WithHint("Make room on this computer (Settings → Storage can empty the copies of footage kept here), then make the cut-out again.").
			WithDetails(map[string]any{"needBytes": d.need, "freeBytes": free, "folder": d.dir})
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
