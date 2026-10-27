package interchange

import (
	"fmt"
	"strings"
)

// WriteSRT writes the caption lines as SubRip subtitles (every editing app
// imports them as a subtitle or caption track). It returns nil when the cut
// has no captions.
func WriteSRT(tl *Timeline) []byte {
	if len(tl.Captions) == 0 {
		return nil
	}
	var b strings.Builder
	for i, c := range tl.Captions {
		text := strings.TrimSpace(cleanText(c.Text))
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTime(c.From, tl.FPS), srtTime(max(c.To, c.From+1), tl.FPS), text)
	}
	return []byte(b.String())
}

func srtTime(f, fps int) string {
	if fps <= 0 {
		fps = 30
	}
	ms := int64(f) * 1000 / int64(fps)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
