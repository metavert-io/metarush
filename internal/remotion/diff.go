package remotion

import (
	"fmt"
	"image"
	_ "image/jpeg" // frames from the check's low-resolution pass
	_ "image/png"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/models"
)

// What a change is, in plain words (D137): which files changed, and the text
// in quotes that changed (where a video's on-screen words usually live).

var textExts = map[string]bool{".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".mts": true, ".cts": true, ".json": true,
	".md": true, ".txt": true, ".css": true, ".html": true, ".svg": true, ".yml": true, ".yaml": true, ".csv": true}

func isText(p string) bool { return textExts[strings.ToLower(path.Ext(p))] }

// wordsCall ends a line just before the name in words("NAME", …) or
// picture("NAME", …) (D209; picture names are code, not words on screen).
var wordsCall = regexp.MustCompile(`\b(?:words|picture|metarushPicture)\(\s*$`)

// isWordsHelper is a helper MetaRush writes for words or pictures as settings
// (its own code, not the project's words).
func isWordsHelper(p string) bool {
	b := path.Base(p)
	return b == "metarush-words.ts" || b == "metarush-pictures.ts"
}

// stringLit finds string literals in a line of code ("…", '…', `…`).
var stringLit = regexp.MustCompile("\"((?:[^\"\\\\]|\\\\.){2,})\"|'((?:[^'\\\\]|\\\\.){2,})'|`([^`$]{2,})`")

func literals(line string) []string {
	var out []string
	for _, ix := range stringLit.FindAllStringSubmatchIndex(line, -1) {
		m := make([]string, 4)
		for g := 0; g < 4; g++ {
			if ix[2*g] >= 0 {
				m[g] = line[ix[2*g]:ix[2*g+1]]
			}
		}
		// words("END", …): the name of a constant made a setting (D168), not words on screen.
		if wordsCall.MatchString(line[:ix[0]]) {
			continue
		}
		s := m[1] + m[2] + m[3]
		if u, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`); err == nil {
			s = u
		}
		if wordy(s) {
			out = append(out, s)
		}
	}
	return out
}

// wordy keeps text people read, not module names, colors, keys or paths.
func wordy(s string) bool {
	if !utf8.ValidString(s) || len(s) < 2 || len(s) > 400 {
		return false
	}
	if !strings.ContainsAny(s, " ") && (strings.ContainsAny(s, "/#.@") || strings.HasPrefix(s, "#")) {
		return false
	}
	letters := 0
	for _, r := range s {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r > 127 {
			letters++
		}
	}
	return letters >= 2
}

// fileChanges lists the changed files with their changed lines, and the
// changed words of the text files (read gives a file's bytes).
func fileChanges(cs []Change, read func(ManifestFile) ([]byte, bool)) ([]models.RemotionFileChange, []models.RemotionWordChange) {
	var files []models.RemotionFileChange
	var words []models.RemotionWordChange
	for _, c := range cs {
		fc := models.RemotionFileChange{Path: c.Path, Change: c.Change}
		if isText(c.Path) && !isWordsHelper(c.Path) {
			var a, b []byte
			if c.Change != "added" {
				a, _ = read(c.Before)
			}
			if c.Change != "removed" {
				b, _ = read(c.After)
			}
			if utf8.Valid(a) && utf8.Valid(b) && len(a)+len(b) < 4<<20 {
				dl := cutsheet.LineDiff(string(a), string(b))
				var old, neu []cutsheet.DiffLine
				flush := func() {
					if len(old)+len(neu) > 0 {
						words = append(words, wordChanges(c.Path, old, neu)...)
					}
					old, neu = nil, nil
				}
				for _, d := range dl {
					switch d.Op {
					case "removed":
						fc.Lines++
						old = append(old, d)
					case "added":
						fc.Lines++
						neu = append(neu, d)
					default:
						flush()
					}
				}
				flush()
			}
		}
		files = append(files, fc)
	}
	if len(words) > 40 {
		words = words[:40]
	}
	return files, words
}

// wordChanges pairs the quoted text a hunk removed with what it added.
func wordChanges(file string, old, neu []cutsheet.DiffLine) []models.RemotionWordChange {
	var before, after []string
	line := 0
	for _, d := range old {
		before = append(before, literals(d.Text)...)
	}
	for _, d := range neu {
		if line == 0 {
			line = d.Right
		}
		after = append(after, literals(d.Text)...)
	}
	if line == 0 && len(old) > 0 {
		line = old[0].Left
	}
	inB := map[string]bool{}
	for _, s := range before {
		inB[s] = true
	}
	inA := map[string]bool{}
	for _, s := range after {
		inA[s] = true
	}
	var gone, came []string
	for _, s := range before {
		if !inA[s] {
			gone = append(gone, s)
		}
	}
	for _, s := range after {
		if !inB[s] {
			came = append(came, s)
		}
	}
	var out []models.RemotionWordChange
	n := max(len(gone), len(came))
	for i := 0; i < n && i < 8; i++ {
		w := models.RemotionWordChange{File: file, Line: line}
		if i < len(gone) {
			w.Before = gone[i]
		}
		if i < len(came) {
			w.After = came[i]
		}
		out = append(out, w)
	}
	return out
}

// changeWords sums a change up in words.
func changeWords(files []models.RemotionFileChange, words []models.RemotionWordChange) string {
	var parts []string
	for i, w := range words {
		if i >= 3 {
			parts = append(parts, fmt.Sprintf("%d more text %s", len(words)-3, map[bool]string{true: "change", false: "changes"}[len(words)-3 == 1]))
			break
		}
		switch {
		case w.Before != "" && w.After != "":
			parts = append(parts, "“"+clip(w.Before, 80)+"” became “"+clip(w.After, 80)+"”")
		case w.After != "":
			parts = append(parts, "new text “"+clip(w.After, 80)+"”")
		default:
			parts = append(parts, "the text “"+clip(w.Before, 80)+"” is gone")
		}
	}
	var code []string
	for _, f := range files {
		switch f.Change {
		case "added":
			code = append(code, f.Path+" is new")
		case "removed":
			code = append(code, f.Path+" was removed")
		default:
			code = append(code, f.Path+" changed")
		}
	}
	s := ""
	if len(parts) > 0 {
		s = upperFirst(joinAnd(parts)) + "."
	}
	if len(code) > 0 {
		if len(code) > 4 {
			code = append(code[:4], fmt.Sprintf("%d more files changed", len(code)-4))
		}
		if s != "" {
			s += " "
		}
		s += "In the code: " + joinAnd(code) + "."
	}
	return s
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// affected lists the compositions a change can reach: those whose files
// (through imports) include a changed file, those declared in a changed
// file, every one when the packages or their public files changed, and the
// ones that are new.
func affected(before, after []models.RemotionComposition, cs []Change, staticFiles []string) []string {
	changed := map[string]bool{}
	all := false
	publicChanged := false
	for _, c := range cs {
		changed[c.Path] = true
		switch {
		case c.Path == "package.json" || c.Path == "package-lock.json" || strings.HasPrefix(c.Path, "remotion.config.") || c.Path == "tsconfig.json":
			all = true
		case isPublic(c.Path):
			publicChanged = true
		}
	}
	if publicChanged && usesPublic(cs, staticFiles) {
		all = true
	}
	had := map[string]bool{}
	for _, c := range before {
		had[c.ID] = true
	}
	var out []string
	for _, c := range after {
		hit := all || !had[c.ID] || changed[c.File] || changed[c.Component]
		for _, f := range c.Files {
			if changed[f] {
				hit = true
				break
			}
		}
		if hit {
			out = append(out, c.ID)
		}
	}
	return out
}

// usesPublic reports a changed public/ file the code names (or any, when it
// names files it computes).
func usesPublic(cs []Change, staticFiles []string) bool {
	for _, c := range cs {
		if !isPublic(c.Path) {
			continue
		}
		rel := strings.TrimPrefix(c.Path, "public/")
		for _, s := range staticFiles {
			if s == rel || (strings.HasSuffix(s, "*") && strings.HasPrefix(rel, strings.TrimSuffix(s, "*"))) {
				return true
			}
		}
		if len(staticFiles) == 0 {
			return true
		}
	}
	return false
}

// frameDiff is how different two pictures are (0 same … 1 opposite): the
// average per-channel difference of the part of the picture that changed
// most (an 8×8 grid), so changed words in a corner count as much as a new
// background.
func frameDiff(a, b string) (float64, error) {
	ia, err := decodeFile(a)
	if err != nil {
		return 0, err
	}
	ib, err := decodeFile(b)
	if err != nil {
		return 0, err
	}
	ra, rb := ia.Bounds(), ib.Bounds()
	if ra.Dx() != rb.Dx() || ra.Dy() != rb.Dy() {
		return 1, nil
	}
	const grid = 8
	var sum [grid][grid]float64
	var n [grid][grid]int
	w, h := ra.Dx(), ra.Dy()
	if w == 0 || h == 0 {
		return 0, nil
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r1, g1, b1, _ := ia.At(ra.Min.X+x, ra.Min.Y+y).RGBA()
			r2, g2, b2, _ := ib.At(rb.Min.X+x, rb.Min.Y+y).RGBA()
			gx, gy := x*grid/w, y*grid/h
			sum[gy][gx] += math.Abs(float64(r1)-float64(r2)) + math.Abs(float64(g1)-float64(g2)) + math.Abs(float64(b1)-float64(b2))
			n[gy][gx] += 3
		}
	}
	most := 0.0
	for y := 0; y < grid; y++ {
		for x := 0; x < grid; x++ {
			if n[y][x] > 0 {
				most = math.Max(most, sum[y][x]/float64(n[y][x])/65535)
			}
		}
	}
	return most, nil
}

func decodeFile(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// framesIn lists a frame pass's pictures in frame order.
func framesIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type fr struct {
		n int
		p string
	}
	var fs []fr
	num := regexp.MustCompile(`(\d+)\.(?:jpe?g|png)$`)
	for _, e := range entries {
		if m := num.FindStringSubmatch(e.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			fs = append(fs, fr{n, filepath.Join(dir, e.Name())})
		}
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].n < fs[j].n })
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.p
	}
	return out
}

// changeThreshold is the difference above which a frame counts as changed:
// renders of the same code are the same picture (the same bytes, usually),
// so a small threshold only absorbs compression noise.
const changeThreshold = 0.01
