package content

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
)

// Before it goes out (D298) is a Content Settings part: whether the videos of
// every project using the settings wait for the quality check and their
// sign-offs before they go out to the client — always, never (reminders
// only), or as the workspace says. Owners change it (content.gate.set).

// PartGate is the ship gate's part.
const PartGate = "gate"

func init() {
	Register(Part{Name: PartGate, Label: "Before it goes out",
		Empty: func(_ context.Context, _ *app.App, r *models.ContentSettings) bool { return r.Gate == nil },
		Summary: func(_ context.Context, _ *app.App, r *models.ContentSettings) string {
			switch {
			case r.Gate == nil:
				return "As the workspace says"
			case r.Gate.Mode == models.GateOn:
				return "Videos wait for the quality check and sign-offs"
			}
			return "The quality check and sign-offs only remind"
		},
		Copy: func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error {
			if from.Gate == nil {
				return nil
			}
			g := *from.Gate
			to.Gate = &g
			return Save(ctx, a, to.ProjectID, bson.M{"gate": g}, nil)
		},
		Check: func(r *models.ContentSettings) string {
			if r.Gate != nil && r.Gate.Mode != models.GateOn && r.Gate.Mode != models.GateOff {
				return "it says neither on nor off."
			}
			return ""
		},
	})
}
