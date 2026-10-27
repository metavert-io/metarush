package interchange

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadCorpus reads every timeline under INTERCHANGE_CORPUS (a folder of
// files exported from other editors, kept outside the repository) and logs
// what came through; it fails only when a reader panics. Run by hand:
//
//	INTERCHANGE_CORPUS=~/timelines go test -run TestReadCorpus -v ./internal/interchange/
func TestReadCorpus(t *testing.T) {
	root := os.Getenv("INTERCHANGE_CORPUS")
	if root == "" {
		t.Skip("set INTERCHANGE_CORPUS to a folder of timelines")
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		data := []byte(nil)
		switch {
		case d.IsDir() && strings.HasSuffix(name, ".fcpxmld"):
			data, err = os.ReadFile(filepath.Join(p, "Info.fcpxml"))
		case d.IsDir():
			return nil
		case strings.Contains(p, ".fcpxmld"+string(filepath.Separator)):
			return nil
		default:
			switch strings.ToLower(filepath.Ext(name)) {
			case ".otio", ".fcpxml", ".xml", ".edl":
				data, err = os.ReadFile(p)
			default:
				return nil
			}
		}
		if err != nil {
			t.Logf("%s: %v", p, err)
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		r, err := ReadTimeline(data, ReadOptions{Name: name})
		if err != nil {
			t.Logf("FAIL %s: %v", rel, err)
			return nil
		}
		f := Flatten(r)
		t.Logf("ok   %s", rel+" "+summarize(r))
		t.Logf("     → %d shots, %d sounds, %d chapters, %d notes, %.3f s; lost: %q", len(f.Shots), len(f.Sounds), len(f.Chapters), len(f.Notes), f.Length, f.Lost)
		return nil
	})
}

func summarize(r *Read) string {
	clips, joins, titles, missing := 0, 0, 0, 0
	for _, group := range [][]*Lane{r.Video, r.Audio} {
		for _, l := range group {
			joins += len(l.Joins)
			for _, p := range l.Pieces {
				switch p.Kind {
				case PieceClip:
					clips++
					if s := r.Source(p.Source); s == nil || (s.Path == "" && s.File == "") {
						missing++
					}
				case PieceTitle, PieceGenerator:
					titles++
				}
			}
		}
	}
	chapters := 0
	for _, m := range r.Markers {
		if m.Chapter {
			chapters++
		}
	}
	return fmt.Sprintf("%s %.3f fps start %.3f length %.3f: %d video lanes, %d sound lanes, %d clips (%d without a file), %d transitions, %d titles, %d markers (%d chapters), %d unread %v %v",
		r.FormatName, r.FPS, r.Start, r.Length, len(r.Video), len(r.Audio), clips, missing, joins, titles, len(r.Markers), chapters, len(r.Unread), r.Unread, r.Warnings)
}
