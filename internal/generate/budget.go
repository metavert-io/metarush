package generate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Money for generative video is reserved before a request is sent and settled
// with what it cost (D124). Each project and each UTC day has a ledger
// document (generation_spend) with what was spent and what is held for
// requests still running; every hold names its generation, so settling twice
// or after a restart never counts twice. A hold is taken only while the new
// total stays within the limit — one conditional update, so two machines
// asking at once can't both go over.

func micros(usd float64) int64 { return int64(math.Round(usd * 1e6)) }

func dollars(m int64) float64 { return math.Round(float64(m)/1e3) / 1e3 }

func dayOf(t time.Time) string { return t.UTC().Format("2006-01-02") }

func ledgerID(ws, scope, key string) string { return ws + ":" + scope + ":" + key }

// hold is one generation's money set aside in a ledger document.
type hold struct {
	ID     string    `bson:"id"`
	Micros int64     `bson:"micros"`
	At     time.Time `bson:"at"`
}

type ledger struct {
	models.GenSpend `bson:",inline"`
	Holds           []hold `bson:"holds,omitempty"`
}

func ensureLedger(ctx context.Context, a *app.App, scope, key string) (string, error) {
	id := ledgerID(a.WorkspaceID(), scope, key)
	_, err := a.DB.C(store.CGenSpend).UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$setOnInsert": bson.M{"workspaceId": a.WorkspaceID(), "scope": scope, "key": key, "spentMicros": int64(0), "heldMicros": int64(0), "count": 0, "updatedAt": time.Now().UTC()}},
		options.UpdateOne().SetUpsert(true))
	if err != nil && !store.IsDuplicateKey(err) {
		return "", err
	}
	return id, nil
}

func loadLedger(ctx context.Context, a *app.App, scope, key string) ledger {
	var l ledger
	_ = a.DB.C(store.CGenSpend).FindOne(ctx, bson.M{"_id": ledgerID(a.WorkspaceID(), scope, key), "workspaceId": a.WorkspaceID()}).Decode(&l)
	return l
}

// take holds amount in one ledger when the total stays within limit (limit < 0: no limit).
func take(ctx context.Context, a *app.App, id, genID string, amount, limit int64) (bool, error) {
	filter := bson.M{"_id": id, "holds.id": bson.M{"$ne": genID}}
	if limit >= 0 {
		filter["$expr"] = bson.M{"$lte": bson.A{bson.M{"$add": bson.A{"$spentMicros", "$heldMicros", amount}}, limit}}
	}
	res, err := a.DB.C(store.CGenSpend).UpdateOne(ctx, filter, bson.M{"$inc": bson.M{"heldMicros": amount},
		"$push": bson.M{"holds": hold{ID: genID, Micros: amount, At: time.Now().UTC()}}, "$set": bson.M{"updatedAt": time.Now().UTC()}})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// release lets a hold go, counting spent micro-dollars instead (0: nothing was spent).
func release(ctx context.Context, a *app.App, id, genID string, spent int64) error {
	var l ledger
	if err := a.DB.C(store.CGenSpend).FindOne(ctx, bson.M{"_id": id}).Decode(&l); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil
		}
		return err
	}
	var held int64 = -1
	for _, h := range l.Holds {
		if h.ID == genID {
			held = h.Micros
		}
	}
	if held < 0 {
		return nil // settled already
	}
	inc := bson.M{"heldMicros": -held, "spentMicros": spent}
	if spent > 0 {
		inc["count"] = 1
	}
	_, err := a.DB.C(store.CGenSpend).UpdateOne(ctx, bson.M{"_id": id, "holds.id": genID},
		bson.M{"$inc": inc, "$pull": bson.M{"holds": bson.M{"id": genID}}, "$set": bson.M{"updatedAt": time.Now().UTC()}})
	return err
}

// limits are what a request is checked against.
type limits struct {
	daily   int64 // micro-dollars
	project int64 // micro-dollars; < 0: no limit
}

// Reserve sets money aside for a generation before it is sent: against the
// day's cap and the project's budget. It refuses, in plain words, when either
// would be exceeded.
func Reserve(ctx context.Context, a *app.App, projectID, genID string, usd float64, now time.Time) (*models.GenBudget, error) {
	st, err := loadSettings(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	return reserveWith(ctx, a, st, projectID, genID, usd, now)
}

func reserveWith(ctx context.Context, a *app.App, st *settings, projectID, genID string, usd float64, now time.Time) (*models.GenBudget, error) {
	return reserveFor(ctx, a, st, projectID, genID, usd, now, "")
}

// ReserveWork sets money aside for AI work of another kind than video
// (D226): against the same day's limit and project budget as generated
// video. what names the kind of work for the refusal ("AI music").
func ReserveWork(ctx context.Context, a *app.App, projectID, genID string, usd float64, now time.Time, what string) (*models.GenBudget, error) {
	st, err := loadSettings(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	return reserveFor(ctx, a, st, projectID, genID, usd, now, what)
}

func reserveFor(ctx context.Context, a *app.App, st *settings, projectID, genID string, usd float64, now time.Time, what string) (*models.GenBudget, error) {
	scope, hint := "generated video", "Try a draft or a shorter shot, or raise "
	if what != "" {
		scope, hint = "AI work", "Try something smaller, or raise "
	}
	amount := micros(usd)
	day := dayOf(now)
	dayID, err := ensureLedger(ctx, a, "day", day)
	if err != nil {
		return nil, err
	}
	ok, err := take(ctx, a, dayID, genID, amount, micros(st.dailyCap))
	if err != nil {
		return nil, err
	}
	if !ok {
		l := loadLedger(ctx, a, "day", day)
		left := math.Max(0, st.dailyCap-dollars(l.SpentMicros+l.HeldMicros))
		return nil, apperr.New(apperr.Budget, "This would go over today's limit for %s: it costs about %s and %s is left of %s.", scope, money(usd), money(left), money(st.dailyCap)).
			WithHint(hint + "the daily limit in Settings, Integrations.").
			WithDetails(map[string]any{"scope": "day", "day": day, "limitUsd": st.dailyCap, "leftUsd": left, "costUsd": usd}).WithCommand("metarush generate configure --daily-cap <dollars>")
	}
	if projectID != "" {
		prjID, err := ensureLedger(ctx, a, "project", projectID)
		if err == nil {
			limit := int64(-1)
			if st.projectBudget != nil {
				limit = micros(*st.projectBudget)
			}
			ok, err = take(ctx, a, prjID, genID, amount, limit)
		}
		if err != nil || !ok {
			_ = release(context.WithoutCancel(ctx), a, dayID, genID, 0)
			if err != nil {
				return nil, err
			}
			l := loadLedger(ctx, a, "project", projectID)
			left := math.Max(0, *st.projectBudget-dollars(l.SpentMicros+l.HeldMicros))
			return nil, apperr.New(apperr.Budget, "This would go over the project's budget for %s: it costs about %s and %s is left of %s.", scope, money(usd), money(left), money(*st.projectBudget)).
				WithHint(hint + "the project's budget in Settings, Integrations.").
				WithDetails(map[string]any{"scope": "project", "projectId": projectID, "limitUsd": *st.projectBudget, "leftUsd": left, "costUsd": usd}).
				WithCommand("metarush generate configure --project " + projectID + " --budget <dollars>")
		}
	}
	return &models.GenBudget{ReservedUSD: usd, Day: day}, nil
}

// Settle counts what a generation cost against its reservation (once: a
// second call, or one after a restart, changes nothing) and records it on the
// generation.
func Settle(ctx context.Context, a *app.App, g *models.Generation, usd float64, note string) error {
	if g.Budget == nil {
		return nil
	}
	spent := micros(usd)
	if err := release(ctx, a, ledgerID(a.WorkspaceID(), "day", g.Budget.Day), g.ID, spent); err != nil {
		return err
	}
	if g.ProjectID != "" {
		if err := release(ctx, a, ledgerID(a.WorkspaceID(), "project", g.ProjectID), g.ID, spent); err != nil {
			return err
		}
	}
	s := models.GenSettlement{USD: usd, At: time.Now().UTC(), Note: note}
	if err := setOnce(ctx, a, g.ID, "settled", s); err != nil {
		return err
	}
	g.Settled = &s
	return nil
}

// BudgetView is a project's budget and today's limit with what counts
// against them (generated video and every other kind of AI work, D226).
func BudgetView(ctx context.Context, a *app.App, projectID string, now time.Time) (GenBudgetView, error) {
	st, err := loadSettings(ctx, a, projectID)
	if err != nil {
		return GenBudgetView{}, err
	}
	return budgetView(ctx, a, st, projectID, now), nil
}

// budgetView is a project's budget and today's cap with what counts against them.
func budgetView(ctx context.Context, a *app.App, st *settings, projectID string, now time.Time) GenBudgetView {
	day := dayOf(now)
	l := loadLedger(ctx, a, "day", day)
	cap := st.dailyCap
	left := math.Max(0, cap-dollars(l.SpentMicros+l.HeldMicros))
	v := GenBudgetView{Today: GenLimit{LimitUSD: &cap, SpentUSD: dollars(l.SpentMicros), HeldUSD: dollars(l.HeldMicros), LeftUSD: &left, Default: st.dailyCapDefault, Day: day}}
	if projectID != "" {
		pl := loadLedger(ctx, a, "project", projectID)
		p := &GenLimit{SpentUSD: dollars(pl.SpentMicros), HeldUSD: dollars(pl.HeldMicros), Default: st.projectBudgetDefault}
		if st.projectBudget != nil {
			lim := *st.projectBudget
			pleft := math.Max(0, lim-dollars(pl.SpentMicros+pl.HeldMicros))
			p.LimitUSD, p.LeftUSD = &lim, &pleft
		}
		v.Project = p
	}
	return v
}

// ReleaseOrphans lets go of holds older than an hour whose generation was
// never saved (the process stopped between the reservation and the record),
// so a crash never keeps money set aside for ever. Holds of saved
// generations are settled by their jobs.
func ReleaseOrphans(ctx context.Context, a *app.App) (int, error) {
	var ls []ledger
	if err := a.DB.Find(ctx, store.CGenSpend, a.WorkspaceID(), bson.M{"holds.at": bson.M{"$lt": time.Now().Add(-time.Hour)}}, &ls); err != nil {
		return 0, err
	}
	n := 0
	for _, l := range ls {
		for _, h := range l.Holds {
			if time.Since(h.At) < time.Hour {
				continue
			}
			var g models.Generation
			err := a.DB.GetByID(ctx, store.CGenerations, a.WorkspaceID(), h.ID, &g)
			if err == nil {
				continue
			}
			if err := release(ctx, a, l.ID, h.ID, 0); err == nil {
				n++
			}
		}
	}
	return n, nil
}

// money says an amount of US dollars the way people read prices: $0.61, $12.
func money(usd float64) string {
	switch {
	case usd == 0:
		return "$0"
	case usd < 0.01:
		return "less than a cent"
	case usd >= 100 && usd == math.Trunc(usd):
		return fmt.Sprintf("$%.0f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}
