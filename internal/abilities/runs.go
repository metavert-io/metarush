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
)

// AbilityRunRow is one run as lists show it (its log and every request are
// on `ability runs --id`).
type AbilityRunRow struct {
	ID            string                   `json:"id"`
	Name          string                   `json:"name"`
	Title         string                   `json:"title"`
	Number        int                      `json:"number"`
	Version       string                   `json:"version"`
	VersionNumber int                      `json:"versionNumber"`
	State         string                   `json:"state" jsonschema:"enum=queued,enum=running,enum=done,enum=failed,enum=canceled"`
	StateLabel    string                   `json:"stateLabel"`
	Inputs        []models.AbilityRunInput `json:"inputs"`
	Outputs       []models.AbilityOutput   `json:"outputs"`
	Requests      int                      `json:"requests" jsonschema:"description=How many requests it made through MetaRush."`
	Refused       int                      `json:"refused" jsonschema:"description=How many of them MetaRush refused."`
	Hosts         []string                 `json:"hosts" jsonschema:"description=The places it reached."`
	EstimateUSD   float64                  `json:"estimateUsd"`
	Machine       string                   `json:"machine"`
	By            models.Actor             `json:"by"`
	ProjectID     string                   `json:"projectId,omitempty"`
	JobID         string                   `json:"jobId,omitempty"`
	Summary       string                   `json:"summary,omitempty"`
	Error         string                   `json:"error,omitempty"`
	Seconds       float64                  `json:"seconds,omitempty"`
	CreatedAt     time.Time                `json:"createdAt"`
	FinishedAt    *time.Time               `json:"finishedAt,omitempty"`
	Link          string                   `json:"link"`
}

// runStateWords says a run's state.
func runStateWords(state string) string {
	switch state {
	case models.AbilityRunQueued:
		return "waiting to start"
	case models.AbilityRunRunning:
		return "running"
	case models.AbilityRunDone:
		return "done"
	case models.AbilityRunFailed:
		return "didn't finish"
	case models.AbilityRunCanceled:
		return "stopped"
	}
	return state
}

func runRow(r *models.AbilityRun) AbilityRunRow {
	row := AbilityRunRow{ID: r.ID, Name: r.Name, Title: r.Title, Number: r.Number, Version: r.Hash, VersionNumber: r.VersionNumber, State: r.State, StateLabel: runStateWords(r.State),
		Inputs: r.Inputs, Outputs: r.Outputs, Requests: len(r.Requests) + r.RequestsLeftOut, Hosts: []string{}, EstimateUSD: r.EstimateUSD, Machine: r.Machine, By: r.By,
		ProjectID: r.ProjectID, JobID: r.JobID, Summary: r.Summary, Error: r.Error, Seconds: r.Seconds, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
		Link: links.AbilityRun(r.Name, r.ID)}
	seen := map[string]bool{}
	for _, q := range r.Requests {
		if q.Refused != "" {
			row.Refused++
		}
		if !seen[q.Host] && q.Refused == "" {
			seen[q.Host] = true
			row.Hosts = append(row.Hosts, q.Host)
		}
	}
	if row.Inputs == nil {
		row.Inputs = []models.AbilityRunInput{}
	}
	if row.Outputs == nil {
		row.Outputs = []models.AbilityOutput{}
	}
	return row
}

// runsOf lists an ability's runs ("" = every ability's), newest first.
func runsOf(ctx context.Context, a *app.App, name string, limit int64) ([]models.AbilityRun, error) {
	filter := bson.M{}
	if name != "" {
		filter["name"] = name
	}
	var out []models.AbilityRun
	err := a.DB.Find(ctx, store.CAbilityRuns, a.WorkspaceID(), filter, &out, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(limit).
		SetProjection(bson.M{"log": 0}))
	return out, err
}

// getRun loads one run (its log included).
func getRun(ctx context.Context, a *app.App, id string) (*models.AbilityRun, error) {
	var r models.AbilityRun
	if err := a.DB.GetByID(ctx, store.CAbilityRuns, a.WorkspaceID(), id, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func setRun(ctx context.Context, a *app.App, id string, set bson.M) error {
	set["updatedAt"] = now()
	_, err := a.DB.C(store.CAbilityRuns).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}, bson.M{"$set": set})
	return err
}

func optionsUpsert() options.Lister[options.ReplaceOptions] { return options.Replace().SetUpsert(true) }

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
