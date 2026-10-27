package voiceover

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
)

// Narration for one clip ("Add voiceover" on a single shot): the lines are
// written, recorded or voiced for that clip only and slot into the script
// where it plays.

var reLeadMarkup = regexp.MustCompile(`^\s*(\[[^\]]*\]|\([^)]*\))`)

// startWithClip makes the first paragraph of a script start with the clip
// (0.3 s in) unless it already has its own start point. Once one paragraph has
// a start point, ParseScript has every later one without its own follow the
// line before it (rather than spreading them over the cut's other shots).
func startWithClip(text, clipID string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if !hasAnchorMarkup(t) {
			lines[i] = fmt.Sprintf("[@%s+0.3] %s", clipID, t)
		}
		break
	}
	return strings.Join(lines, "\n")
}

// hasAnchorMarkup reports whether a paragraph's leading markup has a start
// point ([…]) and not only delivery notes ((…)).
func hasAnchorMarkup(t string) bool {
	rest := t
	for {
		m := reLeadMarkup.FindStringSubmatchIndex(rest)
		if m == nil {
			return false
		}
		if strings.HasPrefix(rest[m[2]:m[3]], "[") {
			return true
		}
		rest = rest[m[1]:]
	}
}

// parseForClip puts lines parsed for one clip into the script: lines that
// start over that clip are replaced, the new ones go where the clip plays, and
// every other line stays.
func parseForClip(ctx context.Context, a *app.App, rc *revCtx, clipIdx int, out *ParseOut) (*ParseOut, error) {
	pc := rc.clips[clipIdx]
	c := rc.spec.Clips[clipIdx]
	var kept []editspec.Beat
	replaced, pos := 0, 0
	v := editspec.Voiceover{}
	if rc.spec.Voiceover != nil {
		v = *editspec.Clone(&editspec.Spec{Voiceover: rc.spec.Voiceover}).Voiceover
		for i, b := range v.Beats {
			rb := rc.beats[i]
			if rb.Problem == "" && rb.Start >= pc.From && rb.Start < pc.From+pc.DurationInFrames {
				replaced++
				continue
			}
			kept = append(kept, b)
			if rb.Problem == "" && rb.Start < pc.From {
				pos = len(kept)
			}
		}
	}
	used := map[string]bool{}
	for _, x := range rc.spec.Clips {
		used[x.ID] = true
	}
	for _, x := range rc.spec.Audio {
		used[x.ID] = true
	}
	for _, x := range rc.spec.Overlays {
		used[x.ID] = true
	}
	for _, b := range kept {
		used[b.ID] = true
	}
	n := 0
	for k := range out.Beats {
		for {
			n++
			id := fmt.Sprintf("line-%d", n)
			if !used[id] {
				out.Beats[k].ID, used[id] = id, true
				break
			}
		}
	}
	merged := append(append(append([]editspec.Beat{}, kept[:pos]...), out.Beats...), kept[pos:]...)
	v.Beats = merged
	name := fmt.Sprintf("shot %d", clipIdx+1)
	if c.Label != "" {
		name = quote(c.Label)
	}
	instruction := fmt.Sprintf("Narration for %s: %s", name, plural(len(out.Beats), "line"))
	res, err := planCheck(ctx, a, rc.rev, instruction, []editspec.Op{{Op: "setScript", Voiceover: &v}})
	if err != nil {
		return nil, err
	}
	out.Result = res
	out.Summary = fmt.Sprintf("%s for %s read from the script", plural(len(out.Beats), "narration line"), name)
	if replaced > 0 {
		out.Summary += fmt.Sprintf(" (in place of the %s that started over it)", plural(replaced, "line"))
	}
	out.Summary += "."
	return out, nil
}
