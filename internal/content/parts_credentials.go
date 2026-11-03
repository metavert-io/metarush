package content

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
)

// Content Credentials (D418) is a Content Settings part: whether the
// delivered videos of every project using the settings carry them — always,
// always and held back until they can be signed, never — or as the
// workspace says. A client who wants signed videos (or none) says so once.
// Owners change it (content.credentials.set).

// PartCredentials is Content Credentials' part.
const PartCredentials = "credentials"

func init() {
	Register(Part{Name: PartCredentials, Label: "Content Credentials",
		Empty: func(_ context.Context, _ *app.App, r *models.ContentSettings) bool { return r.Credentials == nil },
		Summary: func(_ context.Context, _ *app.App, r *models.ContentSettings) string {
			if r.Credentials == nil {
				return "As the workspace says"
			}
			switch r.Credentials.Mode {
			case models.CredentialsRequired:
				return "Every video is signed; none goes out without them"
			case models.CredentialsOn:
				return "Every video is signed"
			}
			return "Videos go out without Content Credentials"
		},
		Copy: func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error {
			if from.Credentials == nil {
				return nil
			}
			c := *from.Credentials
			to.Credentials = &c
			return Save(ctx, a, to.ProjectID, bson.M{"credentials": c}, nil)
		},
		Check: func(r *models.ContentSettings) string {
			if c := r.Credentials; c != nil && c.Mode != models.CredentialsOn && c.Mode != models.CredentialsOff && c.Mode != models.CredentialsRequired {
				return "it says neither on, off nor required."
			}
			return ""
		},
	})
}
