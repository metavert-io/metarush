package content

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// The layout pack (D263) is a Content Settings part: the graphics (title
// card, lower third, end screen, quote card, chapter card) and call layouts
// every project using the settings draws with, restyled by the house style.
// Settings without a pack use MetaRush's own starter pack in their house
// style. A proposed pack (PackDraft, D266) waits for a person to accept it.

// PartPack is the layout pack part.
const PartPack = "pack"

func init() {
	Register(Part{Name: PartPack, Label: "Layout pack",
		Empty: func(_ context.Context, _ *app.App, r *models.ContentSettings) bool {
			return r.Pack == nil && r.PackDraft == nil
		},
		Summary: func(_ context.Context, _ *app.App, r *models.ContentSettings) string {
			return PackSummary(r)
		},
		Copy: func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error {
			if from.Pack == nil {
				return nil
			}
			pk := *from.Pack
			to.Pack = &pk
			return Save(ctx, a, to.ProjectID, bson.M{"pack": pk}, nil)
		},
		Check: func(r *models.ContentSettings) string {
			return r.Pack.Problem(nil, false)
		},
	})
}

// PackSummary says a settings record's pack in a few words.
func PackSummary(r *models.ContentSettings) string {
	s := "MetaRush's own, in the house style"
	if p := r.Pack; p != nil {
		name := p.Name
		if name == "" {
			name = "Its own"
		} else {
			name = quote(name)
		}
		n := len(p.Pieces)
		s = fmt.Sprintf("%s: %d %s", name, n, map[bool]string{true: "graphic", false: "graphics"}[n == 1])
		if !p.Layouts.Empty() {
			s += " and its call layouts"
		}
	}
	if r.PackDraft != nil {
		s += "; a proposed pack waits for a person"
	}
	return s
}

// PackFor is the layout pack a project's cuts follow (D263): its Content
// Settings' pack over MetaRush's starter pack in their house style ("" : the
// default's). Own is nil when the settings have none.
func PackFor(ctx context.Context, a *app.App, projectID string) editspec.Packs {
	style, owner, ownerName := StyleFor(ctx, a, projectID)
	ps := editspec.Packs{Starter: editspec.StarterPack(style)}
	if owner == "" {
		return ps
	}
	rec, err := Record(ctx, a, owner)
	if err != nil || rec == nil || rec.Pack == nil {
		return ps
	}
	pk := *rec.Pack
	ps.Own, ps.Of = &pk, ownerName
	return ps
}
