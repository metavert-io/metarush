package resources

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// TagInput is what a tagger sees: the resource's file name, kind and the
// version just added (with its file on this machine).
type TagInput struct {
	Name    string // the file name it arrived under
	Kind    string
	Version *models.ResourceVersion
	Path    string // the stored file on this machine
	// Pages are the version's page pictures on this machine, in order: a
	// PDF's pages, or a website snapshot's desktop, phone and full-page
	// screenshots (empty when there are none).
	Pages   []string
	Palette int // distinct colour groups (pictures)
	// Tools are this machine's programs (a tagger may run an allowlisted one,
	// such as the on-device visual index's helper).
	Tools *tools.Set
}

// Tagger suggests tags. Taggers run when a version is added and on refresh;
// what they return goes to Resource.AutoTags (people's own tags are never
// touched). The on-device visual index registers one (internal/visual: what
// Apple Vision sees in a picture, on a Mac) and a TextFinder for the words in
// it (Version.Text, which search reads).
type Tagger interface {
	Name() string
	Tags(ctx context.Context, in TagInput) []models.ResourceTag
}

// TextFinder reads text in a picture (OCR).
type TextFinder interface {
	Text(ctx context.Context, in TagInput) (string, error)
}

var (
	hookMu      sync.RWMutex
	taggers     = []Tagger{filenameTagger{}, pictureTagger{}, madeTagger{}}
	textFinders []TextFinder
)

// madeTagger names what MetaRush made from a repository (D106).
type madeTagger struct{}

func (madeTagger) Name() string { return "made" }

func (madeTagger) Tags(_ context.Context, in TagInput) []models.ResourceTag {
	if in.Version == nil || in.Version.Source.MadeWith == nil {
		return nil
	}
	why := "MetaRush made it from a repository"
	switch in.Version.Source.MadeWith.Kind {
	case MakeCode:
		return []models.ResourceTag{{Tag: "code", Source: "made", Confidence: 0.95, Why: "it is a code slide " + why[8:]}}
	case MakeDiagram:
		return []models.ResourceTag{{Tag: "diagram", Source: "made", Confidence: 0.95, Why: "it is a diagram MetaRush drew"}}
	case MakeStats:
		return []models.ResourceTag{{Tag: "stats", Source: "made", Confidence: 0.95, Why: "it is a card about a repository"}}
	case MakeAI:
		if in.Version.Source.MadeWith.Edit {
			return []models.ResourceTag{{Tag: "ai-edited", Source: "made", Confidence: 1, Why: "an AI model changed it, making nothing new"}}
		}
		return []models.ResourceTag{{Tag: models.TagAIGenerated, Source: "made", Confidence: 1, Why: "an AI service made it"}}
	case "ability":
		return []models.ResourceTag{{Tag: "ability", Source: "made", Confidence: 0.95, Why: "an ability made it"}}
	}
	return nil
}

// RegisterTagger adds a tagger (internal/visual registers the visual index's).
func RegisterTagger(t Tagger) {
	hookMu.Lock()
	defer hookMu.Unlock()
	taggers = append(taggers, t)
}

// RegisterTextFinder adds a text finder (internal/visual registers one).
func RegisterTextFinder(t TextFinder) {
	hookMu.Lock()
	defer hookMu.Unlock()
	textFinders = append(textFinders, t)
}

// AutoTags runs every tagger and merges what they suggest (the most
// confident reason per tag), sorted by tag.
func AutoTags(ctx context.Context, in TagInput) []models.ResourceTag {
	hookMu.RLock()
	ts := append([]Tagger{}, taggers...)
	hookMu.RUnlock()
	best := map[string]models.ResourceTag{}
	for _, t := range ts {
		for _, tag := range t.Tags(ctx, in) {
			tag.Tag = strings.ToLower(strings.TrimSpace(tag.Tag))
			if tag.Tag == "" {
				continue
			}
			if cur, ok := best[tag.Tag]; !ok || tag.Confidence > cur.Confidence {
				best[tag.Tag] = tag
			}
		}
	}
	out := make([]models.ResourceTag, 0, len(best))
	for _, t := range best {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return out
}

// FindText runs the registered text finders.
func FindText(ctx context.Context, in TagInput) string {
	hookMu.RLock()
	fs := append([]TextFinder{}, textFinders...)
	hookMu.RUnlock()
	for _, f := range fs {
		if s, err := f.Text(ctx, in); err == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// filenameTagger reads keywords in the file name.
type filenameTagger struct{}

func (filenameTagger) Name() string { return "filename" }

var nameKeywords = []struct {
	re  *regexp.Regexp
	tag string
}{
	{regexp.MustCompile(`(^|[^a-z])(logo|logos|logotype|wordmark|brandmark|lockup)([^a-z]|$)`), "logo"},
	{regexp.MustCompile(`(^|[^a-z])(icon|icons|favicon|glyph|appicon)([^a-z]|$)`), "icon"},
	{regexp.MustCompile(`(^|[^a-z])(screenshot|screen ?shot|screencap|screen ?capture|capture d.?écran|bildschirmfoto)([^a-z]|$)`), "screenshot"},
	{regexp.MustCompile(`(^|[^a-z])(diagram|chart|graph|flowchart|architecture|schematic|infographic)([^a-z]|$)`), "diagram"},
	{regexp.MustCompile(`(^|[^a-z])(photo|img|dsc|pxl|dji|gopr|portrait|headshot)([^a-z]|$)`), "photo"},
	{regexp.MustCompile(`(^|[^a-z])(slide|slides|deck|presentation|keynote)([^a-z]|$)`), "slides"},
}

func (filenameTagger) Tags(_ context.Context, in TagInput) []models.ResourceTag {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(in.Name), filepath.Ext(in.Name)))
	name = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(name)
	var out []models.ResourceTag
	for _, k := range nameKeywords {
		if k.re.MatchString(name) {
			out = append(out, models.ResourceTag{Tag: k.tag, Source: "filename", Confidence: 0.8, Why: "the file name says " + k.tag})
		}
	}
	return out
}

// pictureTagger looks at the picture: a see-through background with a few
// colours looks like a logo; a small square with a see-through background
// like an icon; a screen-shaped, opaque, colourful picture like a screenshot.
type pictureTagger struct{}

func (pictureTagger) Name() string { return "picture" }

// Screen sizes people take screenshots at (and their Retina doubles).
var screenSizes = [][2]int{{1280, 720}, {1280, 800}, {1366, 768}, {1440, 900}, {1512, 982}, {1536, 864}, {1600, 900}, {1680, 1050}, {1728, 1117},
	{1920, 1080}, {1920, 1200}, {2560, 1440}, {2560, 1600}, {2880, 1800}, {3024, 1964}, {3456, 2234}, {3840, 2160},
	{1170, 2532}, {1179, 2556}, {1284, 2778}, {1290, 2796}, {1080, 2340}, {1080, 2400}, {750, 1334}, {828, 1792}, {2048, 2732}, {1668, 2388}}

func (pictureTagger) Tags(_ context.Context, in TagInput) []models.ResourceTag {
	v := in.Version
	if v == nil || v.Source.MadeWith != nil {
		return nil // what MetaRush made says what it is (madeTagger)
	}
	var out []models.ResourceTag
	w, h := v.Width, v.Height
	switch in.Kind {
	case models.ResourceImage:
		if w == 0 || h == 0 {
			return nil
		}
		ratio := float64(max(w, h)) / float64(max(1, min(w, h)))
		switch {
		case v.HasAlpha && in.Palette > 0 && in.Palette <= 8 && ratio <= 6:
			out = append(out, models.ResourceTag{Tag: "logo", Source: "picture", Confidence: 0.6, Why: "it has a see-through background and only a few colours"})
		case v.HasAlpha && ratio <= 1.15 && max(w, h) <= 1024:
			out = append(out, models.ResourceTag{Tag: "icon", Source: "picture", Confidence: 0.5, Why: "it is a small square with a see-through background"})
		}
		if v.HasAlpha && ratio <= 1.15 && max(w, h) <= 512 && in.Palette > 0 && in.Palette <= 8 {
			out = append(out, models.ResourceTag{Tag: "icon", Source: "picture", Confidence: 0.55, Why: "it is a small square with a see-through background"})
		}
		if !v.HasAlpha && screenLike(w, h) && (in.Palette == 0 || in.Palette > 12) {
			out = append(out, models.ResourceTag{Tag: "screenshot", Source: "picture", Confidence: 0.55, Why: "it has the size of a screen"})
		}
	case models.ResourceSVG:
		if in.Palette > 0 && in.Palette <= 6 {
			out = append(out, models.ResourceTag{Tag: "logo", Source: "picture", Confidence: 0.45, Why: "it is a vector picture with only a few colours"})
		}
	case models.ResourcePDF:
		if len(v.Pages) > 0 {
			p := v.Pages[0]
			if p.Width > p.Height {
				out = append(out, models.ResourceTag{Tag: "slides", Source: "picture", Confidence: 0.5, Why: "its pages are wider than they are tall"})
			} else {
				out = append(out, models.ResourceTag{Tag: "document", Source: "picture", Confidence: 0.5, Why: "its pages are taller than they are wide"})
			}
		}
	}
	return out
}

func screenLike(w, h int) bool {
	for _, s := range screenSizes {
		if (w == s[0] && h == s[1]) || (w == s[1] && h == s[0]) {
			return true
		}
	}
	return false
}
