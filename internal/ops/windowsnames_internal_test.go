package ops

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/winname"
)

// What MetaRush writes into a folder people open from Windows keeps
// Windows' rules (D523): delivered videos and their files are named from a
// slug, whatever a project or a version is called, and the footage a
// timeline hands to an editing app keeps its title as far as Windows allows.
func TestFilesForWindowsKeepWindowsNames(t *testing.T) {
	for _, name := range []string{"CON", "Q&A: what's next?", "Café — Ana Müller (版本 2)", `a\b/c:d*e?f"g<h>i|j`, "...", "", "NUL"} {
		for _, title := range []string{"Teaser", "AUX", "trailing dot.", strings.Repeat("é", 80)} {
			base := orSlug(slug(name, 40), "project") + "_" + orSlug(slug(title, 48), "cut") + "_r3_vertical-1080p_31s"
			for _, f := range []string{base + ".mp4", base + ".json", base + ".notes.md", base + ".es.srt", base + ".credits.md"} {
				if !winname.Kept(f) {
					t.Errorf("%q isn't a name Windows keeps", f)
				}
			}
		}
	}
	for title, want := range map[string]string{
		"Kitchen chat":           "Kitchen chat.mov",
		"CON":                    "_CON.mov",
		`Q&A: what's next?`:      "Q&A- what's next-.mov",
		"take. ":                 "take.mov",
		strings.Repeat("é", 130): strings.Repeat("é", 120) + ".mov",
	} {
		got := readableName(&models.Asset{Name: "IMG_0001.mov", Title: title})
		if got != want || !utf8.ValidString(got) || !winname.Kept(got) {
			t.Errorf("%q: %q, want %q", title, got, want)
		}
	}
}
