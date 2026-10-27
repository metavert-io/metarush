package assistant

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/store"
)

// What Auto spends (D241). A change that costs money and ran by itself under
// Auto is recorded (auto_spending: for whom, the project, the card, its
// estimate), so the receipt lists it and the bell says once a day — updated
// as the day goes on — what Auto spent by itself.

// recordAutoSpend keeps a change that cost money and ran by itself, and
// brings the person's note of the day up to date.
func (m *Manager) recordAutoSpend(ctx context.Context, c *conv, act AssistantAction, who models.Actor) {
	if !act.AutoPriced || act.Estimate == nil || act.State == ActionFailed {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	var services []string
	for _, p := range act.Estimate.Parts {
		if !p.Free && p.USD > 0 && !slices.Contains(services, p.Service) {
			services = append(services, p.Service)
		}
	}
	rec := models.AutoSpend{ID: "aus_" + c.id + "_" + act.ID, WorkspaceID: m.App.WorkspaceID(), ProjectID: firstNonEmpty(act.SpendsIn, c.projectID()), ForID: who.UserID, ForName: who.UserName,
		ConversationID: c.id, ActionID: act.ID, Op: act.Op, What: act.Title, USD: act.Estimate.USD, Services: services, Day: localDay(now), At: now}
	if act.Work != nil {
		rec.JobIDs = append([]string(nil), act.Work.JobIDs...) // what it actually costs is what they settle at
	}
	if err := m.App.DB.Insert(ctx, store.CAutoSpending, rec); err != nil && !store.IsDuplicateKey(err) {
		return
	}
	m.autoSpentNote(ctx, who.UserID, rec.Day)
}

// autoSpentNote is the person's note of the day on the bell: what Auto spent
// by itself, on what.
func (m *Manager) autoSpentNote(ctx context.Context, uid, day string) {
	if uid == "" {
		return
	}
	var recs []models.AutoSpend
	if err := m.App.DB.Find(ctx, store.CAutoSpending, m.App.WorkspaceID(), bson.M{"forId": uid, "day": day}, &recs, options.Find().SetSort(bson.D{{Key: "at", Value: 1}})); err != nil || len(recs) == 0 {
		return
	}
	total, upTo, pending := 0.0, 0.0, false
	var what []string
	for i, r := range recs {
		spent, most, waiting := ops.AutoSpent(ctx, m.App, r)
		total, upTo, pending = total+spent, upTo+most, pending || waiting
		if i < 4 {
			what = append(what, theirs(clip(r.What, 80)))
		}
	}
	if len(recs) > 4 {
		what = append(what, fmt.Sprintf("%d more", len(recs)-4))
	}
	last := recs[len(recs)-1]
	n := models.Notification{Kind: models.NotifyAutoSpent, UserID: uid, GroupKey: "auto_spent:" + day, ProjectID: last.ProjectID, Tone: "info",
		Title: autoSpentTitle(total, upTo, pending),
		Body: fmt.Sprintf("%s that cost money ran by themselves (Auto is on and your estimates are off): %s. The project's receipt lists them.",
			strings.ToUpper(pluralize(len(recs), "change")[:1])+pluralize(len(recs), "change")[1:], strings.Join(what, ", ")),
		Details: map[string]any{"day": day, "usd": costs.Round(total), "upToUsd": costs.Round(upTo), "pending": pending, "changes": len(recs)}}
	// What Auto spent today across projects, with each project's receipt.
	n.Link = links.AutoSpending
	_, _ = notify.Upsert(ctx, m.App, n, notify.Refresh)
}

// moneyText says dollars as the bell does: $1.24, less than a cent.
func moneyText(usd float64) string {
	switch {
	case usd <= 0:
		return "nothing"
	case usd < 0.01:
		return "less than a cent"
	}
	return fmt.Sprintf("$%.2f", usd)
}

// AssistantSpendingIn asks what Auto may spend in a project, and what it spent today.
type AssistantSpendingIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=The project whose budgets to look at (ID or name)."`
}

// AutoService is one outside service and whether Auto may spend with it.
type AutoService struct {
	ops.SpendingLimits
	AutoMay bool   `json:"autoMay"`
	Why     string `json:"why,omitempty" jsonschema:"description=Why Auto asks first\\, in plain words."`
}

// AssistantSpendingOut says when Auto spends money by itself (D241).
type AssistantSpendingOut struct {
	Mode         string             `json:"mode" jsonschema:"enum=ask,enum=auto"`
	EstimatesOff bool               `json:"estimatesOff"`
	ProjectID    string             `json:"projectId,omitempty"`
	Services     []AutoService      `json:"services"`
	Today        []models.AutoSpend `json:"today" jsonschema:"description=What Auto spent by itself for you today (this computer's day)."`
	// Projects names the projects of today's spending.
	Projects map[string]string `json:"projects,omitempty"`
	TodayUSD float64           `json:"todayUsd" jsonschema:"description=What it actually cost (charged)."`
	// TodayPending: some of it is still being made; TodayUpToUSD is the most it can come to.
	TodayPending bool    `json:"todayPending,omitempty"`
	TodayUpToUSD float64 `json:"todayUpToUsd,omitempty"`
	Summary      string  `json:"summary"`
}

func init() {
	ops.Def("assistant.spending", "When Auto spends money by itself: with each outside service, whether a person set the project's budget and the team's daily limit (and who); and what Auto spent for you today", ops.Flags{},
		func(ctx context.Context, a *app.App, in *AssistantSpendingIn) (*AssistantSpendingOut, error) {
			return For(a).Spending(AsPerson(ctx, a), in)
		})
}

// Spending says when Auto spends money by itself for the asking person.
func (m *Manager) Spending(ctx context.Context, in *AssistantSpendingIn) (*AssistantSpendingOut, error) {
	uid, _ := m.person(ctx)
	out := &AssistantSpendingOut{Mode: m.personMode(ctx, uid), EstimatesOff: !prefs.EstimatesFirst(ctx, m.App, uid), Services: []AutoService{}, Today: []models.AutoSpend{}}
	pid := ""
	if strings.TrimSpace(in.ProjectID) != "" {
		p, err := ops.ResolveProject(ctx, m.App, in.ProjectID)
		if err != nil {
			return nil, err
		}
		pid, out.ProjectID = p.ID, p.ID
	}
	for _, l := range ops.SpendingServices(ctx, m.App, pid) {
		s := AutoService{SpendingLimits: l}
		if pid == "" {
			s.Why = "a project's budget is set per project"
		} else {
			s.AutoMay, s.Why = l.AutoMay()
		}
		out.Services = append(out.Services, s)
	}
	day := localDay(time.Now())
	_ = m.App.DB.Find(ctx, store.CAutoSpending, m.App.WorkspaceID(), bson.M{"forId": uid, "day": day}, &out.Today, options.Find().SetSort(bson.D{{Key: "at", Value: 1}}))
	for _, r := range out.Today {
		if r.ProjectID != "" {
			if out.Projects == nil {
				out.Projects = map[string]string{}
			}
			if _, ok := out.Projects[r.ProjectID]; !ok {
				if p, err := ops.ResolveProject(ctx, m.App, r.ProjectID); err == nil {
					out.Projects[r.ProjectID] = p.Name
				}
			}
		}
		spent, most, waiting := ops.AutoSpent(ctx, m.App, r)
		out.TodayUSD += spent
		out.TodayUpToUSD += most
		out.TodayPending = out.TodayPending || waiting
	}
	out.TodayUSD, out.TodayUpToUSD = costs.Round(out.TodayUSD), costs.Round(out.TodayUpToUSD)
	switch {
	case out.Mode != ModeAuto || !out.EstimatesOff:
		out.Summary = "Auto doesn't spend money by itself for you: changes that cost money wait for your OK."
		if out.Mode == ModeAuto {
			out.Summary = "Auto doesn't spend money by itself for you while your estimates are on (Settings → Preferences): changes that cost money wait for your OK."
		}
	default:
		var may, not []string
		for _, s := range out.Services {
			if s.AutoMay {
				may = append(may, s.Service)
			} else {
				not = append(not, s.Service)
			}
		}
		switch {
		case pid == "":
			out.Summary = "Auto spends money by itself only in a project where a person set its budget and the team's daily limit with that service."
		case len(may) == 0:
			out.Summary = "Auto doesn't spend money by itself in this project: nobody set both its budget and the team's daily limit with any service."
		default:
			out.Summary = "Auto spends money by itself in this project with " + strings.Join(may, " and ") + ", within the budgets a person set."
			if len(not) > 0 {
				out.Summary += " With " + strings.Join(not, " and ") + " it asks first."
			}
		}
	}
	if len(out.Today) > 0 {
		if out.TodayPending {
			out.Summary += fmt.Sprintf(" Today it spent up to %s by itself for you (some of it is still being made).", moneyText(out.TodayUpToUSD))
		} else {
			out.Summary += fmt.Sprintf(" Today it spent %s by itself for you.", moneyText(out.TodayUSD))
		}
	}
	return out, nil
}

// autoSpentTitle says what Auto spent: what was charged, or "up to" the most
// it can come to while some of it is still being made.
func autoSpentTitle(spent, upTo float64, pending bool) string {
	if pending {
		return fmt.Sprintf("Auto spent up to %s by itself today", moneyText(costs.Round(upTo)))
	}
	return fmt.Sprintf("Auto spent %s by itself today", moneyText(costs.Round(spent)))
}

// refreshAutoSpend brings the day's note up to date once the work of a
// change Auto paid for has ended (what it cost is known then).
func (m *Manager) refreshAutoSpend(ctx context.Context, c *conv, actionID string) {
	var rec models.AutoSpend
	if m.App.DB.GetByID(ctx, store.CAutoSpending, m.App.WorkspaceID(), "aus_"+c.id+"_"+actionID, &rec) != nil {
		return
	}
	m.autoSpentNote(ctx, rec.ForID, rec.Day)
}

// localDay is the day on this computer's clock (the host's), as people count
// "today" (YYYY-MM-DD).
func localDay(t time.Time) string { return t.Local().Format("2006-01-02") }
