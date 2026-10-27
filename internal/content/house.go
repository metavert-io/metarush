package content

import (
	"context"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// StyleFor is the house style (D199) of the Content Settings a project uses
// ("" : the default's) and whose settings hold it: nil when nothing is set
// (MetaRush's own choices), owner "" when no settings are set up.
func StyleFor(ctx context.Context, a *app.App, projectID string) (style *editspec.HouseStyle, owner, ownerName string) {
	g, r, err := Resolve(ctx, a, projectID)
	if err != nil || r.Owner == "" {
		return nil, "", ""
	}
	rec, err := Record(ctx, a, r.Owner)
	if err != nil || rec == nil || rec.Style.Empty() {
		return nil, r.Owner, g.Name(r.Owner)
	}
	st := *rec.Style
	return &st, r.Owner, g.Name(r.Owner)
}

// HouseSignoffs are the house sign-offs (D200) of the Content Settings a
// project uses, marked House with whose settings hold them; whether each was
// given is the project's (ops.SignoffsFor reads it).
func HouseSignoffs(ctx context.Context, a *app.App, projectID string) []models.Signoff {
	if projectID == "" {
		return nil
	}
	g, r, err := Resolve(ctx, a, projectID)
	if err != nil || r.Owner == "" {
		return nil
	}
	rec, err := Record(ctx, a, r.Owner)
	if err != nil || rec == nil {
		return nil
	}
	out := make([]models.Signoff, 0, len(rec.Signoffs))
	for _, s := range rec.Signoffs {
		s.House, s.HouseOf = true, g.Name(r.Owner)
		s.Done, s.DoneBy, s.DoneAt, s.Note, s.Version, s.Branch = false, nil, nil, "", 0, ""
		out = append(out, s)
	}
	return out
}
