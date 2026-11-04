package abilities

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// Versions a catalogue takes back (D488). Every daemon reads the trusted
// catalogues again once their copy is a day old by the database server's
// clock, whether or not anyone browses (a claim keeps it to one read), and
// when the catalogue a version came from takes that version back, the owners
// get a question: take its approval back (or turn it down while it waits),
// or keep it. Its page, every plan of a run and the approval say so too. A
// catalogue taking a version back never changes anything here by itself:
// what may run stays an owner's decision.

// CatalogueRefreshEvery is how often a daemon looks for catalogues due.
var CatalogueRefreshEvery = 15 * time.Minute

// RunCatalogueRefresh reads the trusted catalogues again as they come due,
// and keeps the questions about versions they took back in step.
func RunCatalogueRefresh(ctx context.Context, a *app.App, logf func(string, ...any)) {
	t := time.NewTicker(CatalogueRefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n := RefreshCatalogues(ctx, a); n > 0 && logf != nil {
			logf("abilities: %d version(s) brought in from a catalogue were taken back by it; the owners are asked", n)
		}
	}
}

// RefreshCatalogues is one look of RunCatalogueRefresh: the catalogues due
// are read again, then the questions follow what they take back. It returns
// how many versions here their catalogues took back.
func RefreshCatalogues(ctx context.Context, a *app.App) int {
	cs, err := trustedCatalogues(ctx, a)
	if err != nil || len(cs) == 0 {
		return 0
	}
	refreshCatalogues(ctx, a, cs, false)
	return noteTakenBack(ctx, a)
}

const takenBackPrefix = "ability-taken-back:"

func takenBackKey(hash string) string { return takenBackPrefix + hash }

// noteTakenBack asks the owners about every version here that the catalogue
// it came from takes back (one question per version, never again once
// answered), and closes the questions that no longer apply.
func noteTakenBack(ctx context.Context, a *app.App) int {
	cs, err := trustedCatalogues(ctx, a)
	if err != nil {
		return 0
	}
	keep := []string{}
	for i := range cs {
		c := &cs[i]
		if c.Index == nil || len(c.Index.Revoked) == 0 {
			continue
		}
		why := map[string]string{}
		hashes := bson.A{}
		for _, r := range c.Index.Revoked {
			why[r.Archive] = r.Why
			hashes = append(hashes, r.Archive)
		}
		var vs []models.AbilityVersion
		if err := a.DB.Find(ctx, store.CAbilities, a.WorkspaceID(), bson.M{"hash": bson.M{"$in": hashes}, "imported.catalogue.url": c.URL,
			"state": bson.M{"$in": bson.A{models.AbilityImported, models.AbilityWaiting, models.AbilityApproved}}}, &vs,
			options.Find().SetProjection(bson.M{"name": 1, "hash": 1, "number": 1, "title": 1, "state": 1}).SetLimit(1000)); err != nil {
			return 0 // nothing closed on a guess
		}
		for j := range vs {
			keep = append(keep, takenBackKey(vs[j].Hash))
			askTakenBack(ctx, a, c, &vs[j], why[vs[j].Hash])
		}
	}
	_, _ = todos.CloseStale(ctx, a, takenBackPrefix, keep)
	return len(keep)
}

// askTakenBack opens (or brings up to date) the owners' question about a
// version its catalogue took back.
func askTakenBack(ctx context.Context, a *app.App, c *models.AbilityCatalogue, v *models.AbilityVersion, why string) {
	title := c.Key
	if c.Index != nil {
		title = c.Index.Title
	}
	q := fmt.Sprintf("The catalogue %s took back version %d of the ability %s. Take its approval back?", their(title), v.Number, their(v.Title))
	sugs := []models.TodoSuggestion{{Label: "Take its approval back", Value: "revoke"}, {Label: "Keep it approved", Value: "keep"}}
	if v.State != models.AbilityApproved {
		q = fmt.Sprintf("The catalogue %s took back version %d of the ability %s. Turn it down?", their(title), v.Number, their(v.Title))
		sugs = []models.TodoSuggestion{{Label: "Turn it down", Value: "reject"}, {Label: "Keep it as it is", Value: "keep"}}
	}
	detail := "The catalogue doesn't say why."
	if why != "" {
		detail = "The catalogue says: " + their(why) + "."
	}
	detail += " Nothing changes here by itself: an owner decides whether it may still run. Its page, a plan of a run and the approval say it was taken back."
	_, _ = todos.Upsert(ctx, a, models.Todo{Kind: models.TodoAbilityTakenBack, Scope: "workspace", GroupKey: takenBackKey(v.Hash), HumanOnly: true, Priority: 1,
		Question: q, Detail: detail, Suggestions: sugs,
		Context: models.TodoContext{Link: links.Ability(v.Name, v.Hash), Ability: &models.TodoAbility{Name: v.Name, Title: v.Title, Version: v.Hash, Number: v.Number}}})
}
