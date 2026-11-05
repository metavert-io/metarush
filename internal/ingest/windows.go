package ingest

import (
	"errors"
	"strings"

	"github.com/metavert-io/metarush/internal/wsl"
)

// Paths people type inside WSL (D520, D521): a Windows path (C:\Users\Sam\
// Videos\clip.mp4, in quotes as File Explorer's "Copy as path" gives it) is
// opened where WSL shows it (/mnt/c/Users/Sam/…), and files that come from
// the Windows side say that they come in more slowly.

// TypedPaths are the paths a person typed, as this computer opens them;
// one that can't be opened from Ubuntu (another Linux's files, a network
// folder, a drive WSL doesn't show) is a failed result in plain words.
// Outside WSL every path comes back as it was.
func TypedPaths(typed []string) ([]string, []Result) {
	out := make([]string, 0, len(typed))
	var bad []Result
	for _, t := range typed {
		p, err := wsl.HereLinuxPath(t)
		if err != nil {
			bad = append(bad, Result{Path: t, Name: typedBase(t), Status: "failed", Error: TypedPathText(err)})
			continue
		}
		out = append(out, p)
	}
	return out, bad
}

// TypedPath is one typed path as this computer opens it (TypedPaths).
func TypedPath(typed string) (string, error) { return wsl.HereLinuxPath(typed) }

// TypedPathText says why a typed path can't be opened, and what to do.
func TypedPathText(err error) string {
	var pe *wsl.PathError
	if errors.As(err, &pe) {
		return strings.TrimSpace(pe.Plain + " " + pe.Next)
	}
	return FailText(err)
}

// typedBase is the last part of a typed path, Windows' or Linux's.
func typedBase(t string) string {
	t = strings.TrimRight(wsl.Unquote(t), `\/`)
	if i := strings.LastIndexAny(t, `\/`); i >= 0 {
		return t[i+1:]
	}
	return t
}

// SlowNote says, inside WSL, that some of the files came from the Windows
// side, which Ubuntu reads slowly ("" when none did, or outside WSL).
func SlowNote(results []Result) string {
	w := wsl.Here()
	if !w.In() {
		return ""
	}
	n := 0
	for _, r := range results {
		if r.Status != "failed" && r.Status != "skipped" && w.OnWindows(r.Path) {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return wsl.SlowFiles(n)
}
