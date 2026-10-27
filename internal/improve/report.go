package improve

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/models"
)

// ID prefixes.
const (
	ItemPrefix   = "imp"
	ReportPrefix = "imr"
)

// Item kinds, impacts and states.
const (
	KindFeatureGap = "feature-gap"
	KindFriction   = "friction"
	KindBug        = "bug"

	ImpactHigh   = "high"
	ImpactMedium = "medium"
	ImpactLow    = "low"

	StateNew       = "new"
	StateKept      = "kept"
	StateDismissed = "dismissed"
)

var impactWeight = map[string]float64{ImpactHigh: 3, ImpactMedium: 2, ImpactLow: 1}

// Item is a feature gap, friction point or bug found in the log, kept or
// dismissed by a person. Saving another report that finds the same item (same
// key) updates it instead of adding a copy; its state is kept.
type Item struct {
	ID          string     `bson:"_id" json:"id"`
	WorkspaceID string     `bson:"workspaceId" json:"workspaceId"`
	Key         string     `bson:"key" json:"key" jsonschema:"description=Stable name (from the title unless the report gives one)."`
	Title       string     `bson:"title" json:"title"`
	Kind        string     `bson:"kind" json:"kind" jsonschema:"enum=feature-gap,enum=friction,enum=bug"`
	Impact      string     `bson:"impact" json:"impact" jsonschema:"enum=high,enum=medium,enum=low"`
	Frequency   int        `bson:"frequency" json:"frequency" jsonschema:"description=How often it happened in the period the report looked at."`
	Score       float64    `bson:"score" json:"score" jsonschema:"description=Frequency × impact (high 3\\, medium 2\\, low 1): the ranking."`
	Description string     `bson:"description" json:"description" jsonschema:"description=Markdown."`
	Evidence    []string   `bson:"evidence" json:"evidence" jsonschema:"description=Log entries that show it."`
	State       string     `bson:"state" json:"state" jsonschema:"enum=new,enum=kept,enum=dismissed"`
	ReportIDs   []string   `bson:"reportIds" json:"reportIds"`
	Seen        int        `bson:"seen" json:"seen" jsonschema:"description=How many reports found it."`
	CreatedAt   time.Time  `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time  `bson:"updatedAt" json:"updatedAt"`
	LastSeenAt  time.Time  `bson:"lastSeenAt" json:"lastSeenAt"`
	DecidedBy   string     `bson:"decidedBy,omitempty" json:"decidedBy,omitempty"`
	DecidedAt   *time.Time `bson:"decidedAt,omitempty" json:"decidedAt,omitempty"`
}

// Report is a saved improvement report (Markdown written by Claude Code).
type Report struct {
	ID          string       `bson:"_id" json:"id"`
	WorkspaceID string       `bson:"workspaceId" json:"workspaceId"`
	Title       string       `bson:"title" json:"title"`
	Markdown    string       `bson:"markdown" json:"markdown,omitempty"`
	ItemIDs     []string     `bson:"itemIds" json:"itemIds"`
	ItemCount   int          `bson:"itemCount" json:"itemCount"`
	Warnings    []string     `bson:"warnings,omitempty" json:"warnings,omitempty"`
	CreatedAt   time.Time    `bson:"createdAt" json:"createdAt"`
	CreatedBy   models.Actor `bson:"createdBy" json:"createdBy"`
}

// ParsedItem is one "## " section of a report.
type ParsedItem struct {
	Title       string
	Key         string
	Kind        string
	Impact      string
	Frequency   int
	Evidence    []string
	Description string
}

// ParsedReport is a report's structure.
type ParsedReport struct {
	Title    string
	Items    []ParsedItem
	Warnings []string
}

var (
	metaLine = regexp.MustCompile(`^\s*[-*]\s*([A-Za-z][A-Za-z ]{0,20}):\s*(.*)$`)
	logIDRe  = regexp.MustCompile(`log_[0-9a-z]{26}`)
	slugRe   = regexp.MustCompile(`[^a-z0-9]+`)
)

// ReportFormat explains the report format (also in the metarush-improve skill).
const ReportFormat = `# <report title>

<a short overview>

## <one improvement per section, a short title>
- kind: feature-gap | friction | bug
- impact: high | medium | low
- frequency: <how many times it happened>
- evidence: log_<id>, log_<id>
- key: <optional stable name>

<what happened, why it matters, what MetaRush could add>`

// Slug makes a stable key from a title.
func Slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 80 {
		s = strings.Trim(s[:80], "-")
	}
	return s
}

// ParseReport reads a report: an optional "# " title, then one "## " section
// per item whose first lines are "- key: value" metadata.
func ParseReport(md string) (*ParsedReport, error) {
	out := &ParsedReport{}
	var cur *ParsedItem
	var desc []string
	inMeta := false
	flush := func() {
		if cur == nil {
			return
		}
		cur.Description = strings.TrimSpace(strings.Join(desc, "\n"))
		out.Items = append(out.Items, *cur)
		cur, desc = nil, nil
	}
	inFence := false
	for _, raw := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " \t")
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		switch {
		case !inFence && strings.HasPrefix(line, "## "):
			flush()
			cur = &ParsedItem{Title: strings.TrimSpace(strings.TrimPrefix(line, "## "))}
			inMeta = true
			continue
		case !inFence && strings.HasPrefix(line, "# ") && out.Title == "" && cur == nil:
			out.Title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			continue
		}
		if cur == nil {
			continue
		}
		if inMeta {
			if strings.TrimSpace(line) == "" {
				if len(desc) == 0 {
					continue
				}
			}
			if m := metaLine.FindStringSubmatch(line); m != nil {
				if applyMeta(cur, strings.ToLower(strings.TrimSpace(m[1])), strings.TrimSpace(m[2]), out) {
					continue
				}
			}
			inMeta = false
		}
		desc = append(desc, line)
	}
	flush()
	if len(out.Items) == 0 {
		return nil, fmt.Errorf("the report has no improvement items: write each one as a \"## \" section (format:\n%s)", ReportFormat)
	}
	seen := map[string]bool{}
	for i := range out.Items {
		it := &out.Items[i]
		if it.Title == "" {
			return nil, fmt.Errorf("item %d has no title", i+1)
		}
		if it.Key == "" {
			it.Key = Slug(it.Title)
		}
		if it.Key == "" {
			return nil, fmt.Errorf("item %q needs a key made of letters or digits", it.Title)
		}
		if seen[it.Key] {
			return nil, fmt.Errorf("two items have the same key %q; give one of them its own key", it.Key)
		}
		seen[it.Key] = true
		if it.Kind == "" {
			it.Kind = KindFriction
		}
		if it.Impact == "" {
			it.Impact = ImpactMedium
		}
		if it.Frequency <= 0 {
			it.Frequency = len(it.Evidence)
			if it.Frequency == 0 {
				it.Frequency = 1
			}
		}
	}
	return out, nil
}

func applyMeta(it *ParsedItem, k, v string, out *ParsedReport) bool {
	switch k {
	case "kind", "type":
		v = strings.ToLower(strings.ReplaceAll(v, " ", "-"))
		switch v {
		case KindFeatureGap, "gap", "feature", "missing-feature":
			it.Kind = KindFeatureGap
		case KindFriction:
			it.Kind = KindFriction
		case KindBug, "error":
			it.Kind = KindBug
		default:
			out.Warnings = append(out.Warnings, fmt.Sprintf("%q: unknown kind %q, using friction", it.Title, v))
			it.Kind = KindFriction
		}
	case "impact", "severity":
		v = strings.ToLower(v)
		if _, ok := impactWeight[v]; !ok {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%q: unknown impact %q, using medium", it.Title, v))
			v = ImpactMedium
		}
		it.Impact = v
	case "frequency", "count", "seen":
		n, err := strconv.Atoi(strings.Fields(v + " ")[0])
		if err != nil || n < 0 {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%q: frequency %q is not a number", it.Title, v))
			return true
		}
		it.Frequency = n
	case "evidence", "entries", "log":
		for _, id := range logIDRe.FindAllString(v, -1) {
			if len(it.Evidence) < 50 {
				it.Evidence = append(it.Evidence, id)
			}
		}
	case "key", "id", "slug":
		it.Key = Slug(v)
	default:
		return false
	}
	return true
}

// Score ranks an item: frequency × impact.
func Score(frequency int, impact string) float64 {
	w := impactWeight[impact]
	if w == 0 {
		w = impactWeight[ImpactMedium]
	}
	return float64(frequency) * w
}

// Markdown writes items as a Markdown document (improve export).
func Markdown(title string, items []Item, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "Exported %s. Ranked by how often each happened × its impact.\n", now.Format("2006-01-02"))
	kindName := map[string]string{KindFeatureGap: "Missing feature", KindFriction: "Friction", KindBug: "Problem"}
	for _, it := range items {
		fmt.Fprintf(&b, "\n## %s\n", it.Title)
		fmt.Fprintf(&b, "- kind: %s (%s)\n- impact: %s\n- frequency: %d\n- state: %s\n", it.Kind, kindName[it.Kind], it.Impact, it.Frequency, it.State)
		if len(it.Evidence) > 0 {
			fmt.Fprintf(&b, "- evidence: %s\n", strings.Join(it.Evidence, ", "))
		}
		fmt.Fprintf(&b, "- key: %s\n", it.Key)
		if it.Description != "" {
			fmt.Fprintf(&b, "\n%s\n", it.Description)
		}
	}
	return b.String()
}
