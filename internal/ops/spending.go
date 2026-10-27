package ops

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Spending limits a person set (D241). Auto runs a change that costs money by
// itself only where, for every outside service the change spends with, a
// person set the project's budget and the team's daily limit: a default
// doesn't count, nor does "no limit". Each service says what its limits are
// and who set them by registering here (generated video, the AI voice and
// cloud clean-up today; new kinds of AI work register theirs), keyed by the
// service's name as estimates say it (costs.Part.Service).

// SpendingLimit is one limit on what a service may spend.
type SpendingLimit struct {
	// Exists: the service has such a limit at all (ElevenLabs has no daily limit).
	Exists bool `json:"exists"`
	// Set: it was set — not a default, not "no limit".
	Set bool `json:"set"`
	// Person: who set it is kept, through a people-only step (limits set
	// before MetaRush kept who don't count for Auto).
	Person bool `json:"person"`
	// Words says it: "$20 for this project", "$5 a day for the team", "100,000 credits".
	Words string `json:"words,omitempty"`
	// Default: the service's own default applies (it doesn't count).
	Default bool `json:"default,omitempty"`
	// By and At: who set it and when (empty when it was set before MetaRush kept who).
	By string     `json:"by,omitempty"`
	At *time.Time `json:"at,omitempty"`
}

// SpendingLimits are a service's limits for one project.
type SpendingLimits struct {
	Service string        `json:"service"`
	Project SpendingLimit `json:"project" jsonschema:"description=The project's budget with the service."`
	Daily   SpendingLimit `json:"daily" jsonschema:"description=The most the whole team may spend with it in a day."`
	// Where says where a person sets them (Settings → Integrations).
	Where string `json:"where,omitempty"`
}

// AutoMay reports whether Auto may spend with the service in the project, and
// why not in plain words.
func (l SpendingLimits) AutoMay() (bool, string) {
	switch {
	case !l.Project.Set:
		return false, "nobody set this project's budget for " + l.Service
	case !l.Project.Person:
		return false, "MetaRush doesn't know who set this project's budget for " + l.Service + " (set it again)"
	case !l.Daily.Exists:
		return false, l.Service + " has no daily limit for the team"
	case !l.Daily.Set:
		return false, "nobody set the team's daily limit for " + l.Service
	case !l.Daily.Person:
		return false, "MetaRush doesn't know who set the team's daily limit for " + l.Service + " (set it again)"
	}
	return true, ""
}

// SetByOf describes who set a limit (nil: not recorded): their name, when,
// and whether a person is recorded at all.
func SetByOf(s *models.SetBy) (string, *time.Time, bool) {
	if s == nil {
		return "", nil, false
	}
	at := s.At
	return s.UserName, &at, s.UserID != ""
}

var (
	limitsMu sync.Mutex
	limits   = map[string]func(ctx context.Context, a *app.App, projectID string) SpendingLimits{}
)

// RegisterSpendingLimits tells Auto a service's limits (by its name as
// estimates say it).
func RegisterSpendingLimits(service string, f func(ctx context.Context, a *app.App, projectID string) SpendingLimits) {
	limitsMu.Lock()
	defer limitsMu.Unlock()
	limits[strings.ToLower(service)] = f
}

// SpendingLimitsFor are a service's limits for a project; ok is false when
// the service didn't say (Auto then asks).
func SpendingLimitsFor(ctx context.Context, a *app.App, service, projectID string) (SpendingLimits, bool) {
	limitsMu.Lock()
	f := limits[strings.ToLower(service)]
	limitsMu.Unlock()
	if f == nil {
		return SpendingLimits{Service: service}, false
	}
	l := f(ctx, a, projectID)
	if l.Service == "" {
		l.Service = service
	}
	return l, true
}

// SpendingServices lists the services that registered their limits.
func SpendingServices(ctx context.Context, a *app.App, projectID string) []SpendingLimits {
	limitsMu.Lock()
	fs := make([]func(ctx context.Context, a *app.App, projectID string) SpendingLimits, 0, len(limits))
	for _, f := range limits {
		fs = append(fs, f)
	}
	limitsMu.Unlock()
	out := make([]SpendingLimits, 0, len(fs))
	for _, f := range fs {
		out = append(out, f(ctx, a, projectID))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// AutoSpent says what a change Auto ran by itself actually cost (D241): for
// generated video, what its generations settled at (a refused or failed one
// costs nothing); for other work it started, its estimate once the work
// succeeded (nothing when it failed); for work done at once, its estimate.
// While some of it is still being made, pending is true and upTo is the most
// it can come to.
func AutoSpent(ctx context.Context, a *app.App, r models.AutoSpend) (spent, upTo float64, pending bool) {
	if len(r.JobIDs) == 0 {
		return r.USD, r.USD, false
	}
	var gens []struct {
		Settled *struct {
			USD float64 `bson:"usd"`
		} `bson:"settled"`
		Outcome *struct {
			State  string `bson:"state"`
			Actual *struct {
				USD float64 `bson:"usd"`
			} `bson:"actual"`
		} `bson:"outcome"`
		Estimate struct {
			USD float64 `bson:"usd"`
		} `bson:"estimate"`
	}
	_ = a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"jobId": bson.M{"$in": r.JobIDs}}, &gens,
		options.Find().SetProjection(bson.M{"settled": 1, "outcome": 1, "estimate": 1}))
	if len(gens) > 0 {
		for _, g := range gens {
			switch {
			case g.Settled != nil:
				spent += g.Settled.USD
				upTo += g.Settled.USD
			case g.Outcome != nil && g.Outcome.Actual != nil:
				spent += g.Outcome.Actual.USD
				upTo += g.Outcome.Actual.USD
			case g.Outcome != nil:
				// ended without a charge
			default:
				pending = true
				upTo += g.Estimate.USD
			}
		}
		return costs.Round(spent), costs.Round(upTo), pending
	}
	var jobs []models.Job
	_ = a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": r.JobIDs}}, &jobs, options.Find().SetProjection(bson.M{"state": 1}))
	ok := false
	for _, j := range jobs {
		switch j.State {
		case models.JobSucceeded:
			ok = true
		case models.JobQueued, models.JobRunning:
			return 0, r.USD, true
		}
	}
	if ok {
		return r.USD, r.USD, false
	}
	return 0, 0, false
}
