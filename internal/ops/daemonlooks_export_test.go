package ops

import (
	"context"

	"github.com/metavert-io/metarush/internal/app"
)

// FollowLook is the review-copy daemon's look, by the database server's clock.
func FollowLook(ctx context.Context, a *app.App) (*FollowResult, error) { return followLook(ctx, a) }

// RecoverPublicationsNow is the posting daemon's look, by the database server's clock.
func RecoverPublicationsNow(ctx context.Context, a *app.App) []string {
	return recoverPublicationsNow(ctx, a)
}
