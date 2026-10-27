package assistant

import (
	"strings"

	"github.com/metavert-io/metarush/internal/version"
)

// What's new after an update (D185). A conversation records the MetaRush
// version of its latest message (Conversation.MetaRushVersion); the first
// message on a newer build tells Claude it was updated and what it can do now,
// from the CHANGELOG headlines between the two versions, once per upgrade. A
// conversation that told the person something wasn't possible checks again.

// maxWhatsNew bounds the headlines told at once.
const maxWhatsNew = 12

// whatsNewNote is the note for an update from prev to cur (both versions).
func whatsNewNote(notes version.Notes, prev, cur string) string {
	heads := whatsNewHeadlines(notes, prev, cur)
	var b strings.Builder
	b.WriteString("MetaRush was updated from " + prev + " to " + cur + ".")
	if len(heads) > 0 {
		b.WriteString(" New things you can do: " + strings.Join(heads, "; ") + ".")
	}
	b.WriteString(" If you told the person earlier that something wasn't possible, check again.")
	return b.String()
}

// whatsNewHeadlines are the plain headlines of the releases after prev up to
// cur, newest first; a build past its release (dev) adds the changes not
// released yet when prev was before that release.
func whatsNewHeadlines(notes version.Notes, prev, cur string) []string {
	curInfo := version.Describe(cur, "", "")
	prevInfo := version.Describe(prev, "", "")
	var out []string
	seen := map[string]bool{}
	add := func(s version.ReleaseNotes) {
		for _, e := range s.Entries {
			h := strings.TrimSpace(strings.TrimSuffix(e.Headline, "."))
			if h == "" || seen[h] {
				continue
			}
			seen[h] = true
			out = append(out, h)
		}
	}
	if curInfo.Dev && prevInfo.Release != curInfo.Release {
		if s := notes.Section(""); s != nil {
			add(*s)
		}
	}
	for _, s := range notes {
		if s.Release == "" {
			continue
		}
		after, ok1 := version.Compare(s.Release, prev)
		upTo, ok2 := version.Compare(s.Release, cur)
		if ok1 && ok2 && after > 0 && upTo <= 0 {
			add(s)
		}
	}
	if len(out) > maxWhatsNew {
		out = append(out[:maxWhatsNew], "and more (Settings → General lists them)")
	}
	return out
}
