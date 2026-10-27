package content

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Part is one kind of thing Content Settings hold. Each is registered once;
// following, "Also used by", copies and the project's Settings tab work for
// every registered part the same way.
type Part struct {
	// Name is the part's stable name (brand, spelling, voice, rules).
	Name string
	// Label is how people see it ("Narration voice").
	Label string
	// Empty reports settings whose part holds nothing (a new project's own
	// settings start with every part empty).
	Empty func(ctx context.Context, a *app.App, rec *models.ContentSettings) bool
	// Summary says in a few plain words what the part holds.
	Summary func(ctx context.Context, a *app.App, rec *models.ContentSettings) string
	// Copy copies the part from one project's settings into another's
	// ("its own, starting as a copy").
	Copy func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error
	// Check says what is wrong with the part ("" nothing).
	Check func(rec *models.ContentSettings) string
}

var parts []Part

// Register adds a part (at init). Names are unique.
func Register(p Part) {
	for _, x := range parts {
		if x.Name == p.Name {
			panic("content part registered twice: " + p.Name)
		}
	}
	parts = append(parts, p)
}

// Parts lists the registered parts in order.
func Parts() []Part { return append([]Part(nil), parts...) }

// PartNamed finds a part by name.
func PartNamed(name string) (Part, bool) {
	for _, p := range parts {
		if p.Name == name {
			return p, true
		}
	}
	return Part{}, false
}

// RecordEmpty reports settings whose every part is empty.
func RecordEmpty(ctx context.Context, a *app.App, rec *models.ContentSettings) bool {
	if rec == nil {
		return true
	}
	for _, p := range parts {
		if p.Empty != nil && !p.Empty(ctx, a, rec) {
			return false
		}
	}
	return true
}

// Check lists what is wrong with a record, part by part.
func Check(rec *models.ContentSettings) []string {
	var out []string
	for _, p := range parts {
		if p.Check != nil {
			if why := p.Check(rec); why != "" {
				out = append(out, p.Label+": "+why)
			}
		}
	}
	return out
}

// The parts of D193, then the house style (D199) and house sign-offs (D200).
const (
	PartBrand    = "brand"
	PartSpelling = "spelling"
	PartVoice    = "voice"
	PartRules    = "rules"
	PartStyle    = "style"
	PartSignoffs = "signoffs"
)

func init() {
	Register(Part{Name: PartBrand, Label: "Brand",
		Empty: func(_ context.Context, _ *app.App, r *models.ContentSettings) bool {
			return models.BrandKitEmpty(r.BrandKit)
		},
		Summary: func(ctx context.Context, a *app.App, r *models.ContentSettings) string {
			k := r.BrandKit
			if models.BrandKitEmpty(k) {
				return "No brand yet"
			}
			var bits []string
			switch n := LogoCount(k); n {
			case 0:
			case 1:
				bits = append(bits, "a logo")
			default:
				bits = append(bits, fmt.Sprintf("%d logos", n))
			}
			if k.Primary != "" || k.Accent != "" || k.Text != "" || k.Background != "" {
				bits = append(bits, "its colours")
			}
			switch {
			case k.FontResource != "":
				var res models.Resource
				if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), k.FontResource, &res) == nil && res.Title != "" {
					bits = append(bits, "the font “"+res.Title+"”")
				} else {
					bits = append(bits, "a font file")
				}
			case k.Font != "":
				bits = append(bits, k.Font)
			}
			return capFirst(joinAnd(bits))
		},
		Copy: func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error {
			if models.BrandKitEmpty(from.BrandKit) {
				return nil
			}
			k := *from.BrandKit
			k.UpdatedAt, k.UpdatedBy = time.Now().UTC(), a.Actor
			to.BrandKit = &k
			return Save(ctx, a, to.ProjectID, bson.M{"brandKit": k}, nil)
		},
		Check: func(r *models.ContentSettings) string {
			if r.BrandKit == nil {
				return ""
			}
			for _, c := range []string{r.BrandKit.Primary, r.BrandKit.Accent, r.BrandKit.Text, r.BrandKit.Background} {
				if c != "" && !hexColor(c) {
					return fmt.Sprintf("%q isn't a colour (#RRGGBB).", c)
				}
			}
			return ""
		},
	})
	Register(Part{Name: PartSpelling, Label: "Spelling",
		Empty: func(ctx context.Context, a *app.App, r *models.ContentSettings) bool {
			return spellingCount(ctx, a, r.ProjectID) == 0
		},
		Summary: func(ctx context.Context, a *app.App, r *models.ContentSettings) string {
			switch n := spellingCount(ctx, a, r.ProjectID); n {
			case 0:
				return "No words yet"
			case 1:
				return "1 word"
			default:
				return fmt.Sprintf("%d words", n)
			}
		},
		Copy: func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error {
			var list []models.SpellingEntry
			if err := a.DB.Find(ctx, store.CSpelling, a.WorkspaceID(), bson.M{"projectId": from.ProjectID}, &list); err != nil {
				return err
			}
			for _, e := range list {
				e.ID, e.ProjectID = ids.New(ids.Spelling), to.ProjectID
				if err := a.DB.Insert(ctx, store.CSpelling, e); err != nil && !store.IsDuplicateKey(err) {
					return err
				}
			}
			return nil
		},
		Check: func(*models.ContentSettings) string { return "" },
	})
	Register(Part{Name: PartVoice, Label: "Narration voice",
		Empty: func(_ context.Context, _ *app.App, r *models.ContentSettings) bool {
			for _, v := range r.Voice {
				if !v.Empty() {
					return false
				}
			}
			return true
		},
		Summary: func(_ context.Context, _ *app.App, r *models.ContentSettings) string {
			for _, name := range []string{"elevenlabs"} {
				v, ok := r.Voice[name]
				if !ok || v.Empty() {
					continue
				}
				s := "No voice chosen"
				if v.VoiceID != "" {
					s = firstNonEmpty(v.VoiceName, "A chosen voice")
				}
				if v.ModelName != "" || v.ModelID != "" {
					s += " (" + firstNonEmpty(v.ModelName, v.ModelID) + ")"
				}
				return s
			}
			return "No voice chosen yet"
		},
		Copy: func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error {
			if len(from.Voice) == 0 {
				return nil
			}
			to.Voice = from.Voice
			return Save(ctx, a, to.ProjectID, bson.M{"voice": from.Voice}, nil)
		},
		Check: func(r *models.ContentSettings) string {
			for _, v := range r.Voice {
				if s := v.Voice; s != nil {
					for _, x := range []*float64{s.Stability, s.SimilarityBoost, s.Style} {
						if x != nil && (*x < 0 || *x > 1) {
							return "a voice setting is outside 0–1."
						}
					}
					if s.Speed != nil && (*s.Speed < 0.7 || *s.Speed > 1.2) {
						return "the speed is outside 0.7–1.2."
					}
				}
			}
			return ""
		},
	})
	Register(Part{Name: PartRules, Label: "House rules",
		Empty: func(_ context.Context, _ *app.App, r *models.ContentSettings) bool { return len(r.HouseRules) == 0 },
		Summary: func(_ context.Context, _ *app.App, r *models.ContentSettings) string {
			switch n := len(r.HouseRules); n {
			case 0:
				return "No house rules yet"
			case 1:
				return "1 house rule"
			default:
				return fmt.Sprintf("%d house rules", n)
			}
		},
		Copy: func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error {
			if len(from.HouseRules) == 0 {
				return nil
			}
			rules := make([]models.ProjectRule, 0, len(from.HouseRules))
			for _, r := range from.HouseRules {
				r.ID = ids.New(ids.Rule)
				rules = append(rules, r)
			}
			to.HouseRules = rules
			return Save(ctx, a, to.ProjectID, bson.M{"houseRules": rules}, nil)
		},
		Check: func(r *models.ContentSettings) string {
			if len(r.HouseRules) > 100 {
				return "at most 100 house rules."
			}
			for _, x := range r.HouseRules {
				if strings.TrimSpace(x.Text) == "" {
					return "a house rule has no words."
				}
			}
			return ""
		},
	})
	Register(Part{Name: PartStyle, Label: "House style",
		Empty: func(_ context.Context, _ *app.App, r *models.ContentSettings) bool { return r.Style.Empty() },
		Summary: func(_ context.Context, _ *app.App, r *models.ContentSettings) string {
			return StyleSummary(r.Style)
		},
		Copy: func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error {
			if from.Style.Empty() {
				return nil
			}
			st := *from.Style
			to.Style = &st
			return Save(ctx, a, to.ProjectID, bson.M{"style": st}, nil)
		},
		Check: func(r *models.ContentSettings) string {
			if p := r.Style.Problem(); p != "" {
				return p
			}
			return ""
		},
	})
	Register(Part{Name: PartSignoffs, Label: "House sign-offs",
		Empty: func(_ context.Context, _ *app.App, r *models.ContentSettings) bool { return len(r.Signoffs) == 0 },
		Summary: func(_ context.Context, _ *app.App, r *models.ContentSettings) string {
			switch n := len(r.Signoffs); n {
			case 0:
				return "No house sign-offs yet"
			case 1:
				return "1 house sign-off"
			default:
				return fmt.Sprintf("%d house sign-offs", n)
			}
		},
		Copy: func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error {
			if len(from.Signoffs) == 0 {
				return nil
			}
			list := make([]models.Signoff, 0, len(from.Signoffs))
			for _, s := range from.Signoffs {
				s.ID = ids.New(ids.Signoff)
				list = append(list, s)
			}
			to.Signoffs = list
			return Save(ctx, a, to.ProjectID, bson.M{"signoffs": list}, nil)
		},
		Check: func(r *models.ContentSettings) string {
			if len(r.Signoffs) > 50 {
				return "at most 50 house sign-offs."
			}
			for _, s := range r.Signoffs {
				if strings.TrimSpace(s.Text) == "" {
					return "a house sign-off has no words."
				}
			}
			return ""
		},
	})
}

// LogoCount is how many named logos a brand has (D198).
func LogoCount(k *models.BrandKit) int {
	if k == nil {
		return 0
	}
	n := 0
	for _, id := range []string{k.Logo, k.LogoDark, k.LogoLight, k.LogoIcon, k.LogoWordmark} {
		if id != "" {
			n++
		}
	}
	return n
}

// StyleSummary says a house style in a few words for the Settings tab.
func StyleSummary(h *editspec.HouseStyle) string {
	words := editspec.StyleWords(h)
	switch len(words) {
	case 0:
		return "MetaRush's own style"
	case 1:
		return words[0]
	case 2:
		return words[0] + "; " + strings.ToLower(words[1][:1]) + words[1][1:]
	}
	return fmt.Sprintf("%s; and %d more", words[0], len(words)-1)
}

func spellingCount(ctx context.Context, a *app.App, projectID string) int {
	if projectID == "" {
		return 0
	}
	n, err := a.DB.C(store.CSpelling).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID})
	if err != nil {
		return 0
	}
	return int(n)
}

func hexColor(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// AIAllowed says whether the Content Settings a project uses allow an AI
// service (D225): nil when they do, or a plain refusal. internal/aiwork sets
// it; generated video asks it before anything is priced.
var AIAllowed func(ctx context.Context, a *app.App, projectID, provider string) error

// AIServiceAllowed asks AIAllowed (allowed when nothing set it).
func AIServiceAllowed(ctx context.Context, a *app.App, projectID, provider string) error {
	if AIAllowed == nil {
		return nil
	}
	return AIAllowed(ctx, a, projectID, provider)
}
