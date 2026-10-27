package transcripts

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/store"
)

// NotifyMissed tells the people whose fixes didn't carry over to a clip's new
// transcript where to look (one notification per person and transcript).
func NotifyMissed(ctx context.Context, a *app.App, as *models.Asset, c *models.CarryReport) {
	if c == nil || len(c.Missed) == 0 {
		return
	}
	idsOf := make([]string, 0, len(c.Missed))
	for _, m := range c.Missed {
		idsOf = append(idsOf, m.FixID)
	}
	var fixes []models.TranscriptFix
	if err := a.DB.Find(ctx, store.CTranscriptFixes, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idsOf}}, &fixes); err != nil {
		return
	}
	per := map[string]int{}
	for _, f := range fixes {
		if f.By.UserID != "" {
			per[f.By.UserID]++
		}
	}
	projectID := ""
	if len(as.ProjectIDs) > 0 {
		projectID = as.ProjectIDs[0]
	}
	for user, n := range per {
		title := fmt.Sprintf("1 of your transcript fixes didn't carry over to “%s”", as.DisplayTitle())
		if n > 1 {
			title = fmt.Sprintf("%d of your transcript fixes didn't carry over to “%s”", n, as.DisplayTitle())
		}
		_, _ = notify.Upsert(ctx, a, models.Notification{Kind: models.NotifyFixesMissed, UserID: user, Tone: "warning",
			GroupKey: fmt.Sprintf("fixes-missed:%s:%d", as.ID, c.At.Unix()), Title: title,
			Body:    "It was transcribed again and now says something else there. Open its transcript to check those places.",
			AssetID: as.ID, ProjectID: projectID, Link: links.Asset(projectID, as.ID), Details: map[string]any{"missed": n}}, notify.Once)
	}
}
