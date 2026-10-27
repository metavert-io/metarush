// Package receipt is a project's receipt (D240).
package receipt

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// The receipt (D240): for a project, every request MetaRush sent to an outside
// AI service — which service and model, what kind of work, what was sent, what
// it cost, whether MetaRush deleted it there afterwards and why it ran — the
// Claude Code conversations about it (whose account answered, under which
// terms), the generated media it has, what Auto spent by itself, and the cost
// of the video at cost: what the services charge, with nothing added (D240's
// part two, the cost of each version and of the project).
//
// Generation records are read generically: every document in `generations`
// for the project is a request, whatever its kind of work (`kind`, video when
// it has none) and with whichever of the usual fields it has, so new kinds of
// AI work recorded there appear without changes here. The AI voice
// (`voice_takes`) and cloud clean-up (`sound_cache`) keep their own records.
// The activity log's record of every request to an outside service is listed
// beside them, so a service whose records live elsewhere still shows.

// Deletion at the service (ReceiptRequest.Deleted).
const (
	DeletedThere = "deleted" // MetaRush deleted what was sent and made there, after the download
	KeptThere    = "kept"    // MetaRush doesn't delete it there
	DeleteFailed = "failed"  // MetaRush tried and it failed
	DeleteNA     = "unknown" // the record doesn't say
)

// ProjectReceiptIn names the project (and a version whose cost to show).
type ProjectReceiptIn struct {
	ProjectID string `json:"projectId"`
	Revision  string `json:"revision,omitempty" jsonschema:"description=The version whose cost to show (rN or its ID); default the latest."`
	Branch    string `json:"branch,omitempty"`
}

// ReceiptRequest is one request to an outside AI service.
type ReceiptRequest struct {
	Kind    string    `json:"kind" jsonschema:"description=The kind of AI work: video\\, voice\\, clean-up\\, or what its record says (picture\\, music…)."`
	What    string    `json:"what" jsonschema:"description=What it made\\, in plain words."`
	Service string    `json:"service"`
	Model   string    `json:"model,omitempty"`
	At      time.Time `json:"at"`
	By      string    `json:"by,omitempty" jsonschema:"description=Who asked."`
	Why     string    `json:"why,omitempty" jsonschema:"description=Why it ran: who asked and from where."`
	// Sent says what left this computer, in plain words; Prompt is the words sent.
	Sent   []string `json:"sent"`
	Prompt string   `json:"prompt,omitempty"`
	// Kinds are what it sent (footage, voices, faces, words, pictures) and
	// Left whether it reached the service (D291).
	Kinds []string `json:"kinds"`
	Left  bool     `json:"left"`
	USD   float64  `json:"usd"`
	// Basis: charged (what the service reported, or what was settled), estimate
	// (MetaRush's estimate at its dated prices) or none (nothing charged).
	Basis   string `json:"basis" jsonschema:"enum=charged,enum=estimate,enum=none"`
	Outcome string `json:"outcome" jsonschema:"enum=done,enum=failed,enum=refused,enum=canceled,enum=running"`
	// Deleted says whether MetaRush deleted what was sent and made at the
	// service afterwards; DeletedSays says it in words.
	Deleted     string   `json:"deleted" jsonschema:"enum=deleted,enum=kept,enum=failed,enum=unknown"`
	DeletedSays string   `json:"deletedSays"`
	Outputs     []string `json:"outputs,omitempty" jsonschema:"description=The clips it made (IDs)."`
	// Record is where MetaRush keeps it: <collection>/<ID>.
	Record string `json:"record"`
}

// ReceiptClaude is one Claude Code conversation about the project.
type ReceiptClaude struct {
	ConversationID string `json:"conversationId"`
	Title          string `json:"title"`
	StartedBy      string `json:"startedBy,omitempty"`
	Messages       int    `json:"messages"`
	Tokens         int64  `json:"tokens" jsonschema:"description=Tokens it used (counted only by newer versions)."`
	Model          string `json:"model,omitempty"`
	Machine        string `json:"machine" jsonschema:"description=The computer whose Claude Code answered."`
	Who            string `json:"who,omitempty" jsonschema:"description=Whose Claude account answered."`
	// Plan is the account's plan in words (Claude Max, an Anthropic API key):
	// what delivered files say instead of whose account it is.
	Plan   string    `json:"plan,omitempty"`
	Terms  string    `json:"terms,omitempty" jsonschema:"description=Its terms\\, in words."`
	USD    float64   `json:"usd" jsonschema:"description=What it cost: with an API key what Claude Code reported; on a Claude plan 0 (it counts toward the plan's limits)."`
	Billed bool      `json:"billed" jsonschema:"description=Billed per token (an API key or a cloud provider)."`
	At     time.Time `json:"at"`
	Last   time.Time `json:"last"`
}

// ReceiptLogged counts the requests to an outside service the activity log
// recorded for the project.
type ReceiptLogged struct {
	Service string    `json:"service"`
	What    string    `json:"what"`
	Count   int       `json:"count"`
	Failed  int       `json:"failed"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
}

// ReceiptGenerated is generated media the project has.
type ReceiptGenerated struct {
	AssetID string `json:"assetId,omitempty"`
	// ResourceID is set for a picture an AI service made (D226).
	ResourceID string `json:"resourceId,omitempty"`
	Title      string `json:"title"`
	Kind       string `json:"kind" jsonschema:"enum=shot,enum=voice,enum=picture,enum=sound,enum=presenter,description=shot: a generated clip; voice: AI narration; picture: an AI picture; sound: AI speech\\, sound effects\\, music\\, a voice change or a dub; presenter: a presenter reading lines on camera."`
	// What says it in plain words: a generated shot, a piece of music, a dubbed copy.
	What    string  `json:"what,omitempty"`
	Service string  `json:"service,omitempty"`
	Model   string  `json:"model,omitempty"`
	Seconds float64 `json:"seconds"`
	// InVersion: the version the receipt prices uses it.
	InVersion bool `json:"inVersion"`
}

// ReceiptAuto is a change that cost money and that Auto ran by itself (D241).
type ReceiptAuto struct {
	At   time.Time `json:"at"`
	For  string    `json:"for" jsonschema:"description=The person whose Auto ran it."`
	What string    `json:"what"`
	// USD is what it actually cost (what it made settled at); while it is
	// still being made, Pending is true and UpToUSD the most it can come to.
	USD            float64 `json:"usd"`
	Pending        bool    `json:"pending,omitempty"`
	UpToUSD        float64 `json:"upToUsd,omitempty"`
	ConversationID string  `json:"conversationId,omitempty"`
}

// ReceiptSample is a sample a run of Recipes made first (D294): its cost is
// among the requests above (the sample is made in the run's project); this
// says which of it was the sample, and who approved it or skipped it.
type ReceiptSample struct {
	Run   int       `json:"run" jsonschema:"description=The run's number."`
	Title string    `json:"title" jsonschema:"description=What the run made\\, by its title."`
	State string    `json:"state" jsonschema:"enum=making,enum=waiting,enum=approved,enum=not-quite,enum=skipped,enum=stopped"`
	What  []string  `json:"what" jsonschema:"description=What the sample was\\, kind by kind."`
	USD   float64   `json:"usd" jsonschema:"description=What the sample spent in US dollars."`
	By    string    `json:"by,omitempty" jsonschema:"description=Who approved it\\, turned it down or skipped it."`
	Note  string    `json:"note,omitempty" jsonschema:"description=What they said: why it wasn't right\\, or why it was skipped."`
	At    time.Time `json:"at"`
}

// ReceiptCost is the cost of the video at cost (D240): what the services
// charged, nothing added.
type ReceiptCost struct {
	ProjectUSD float64              `json:"projectUsd" jsonschema:"description=Everything spent on outside AI services for the project."`
	Project    string               `json:"project" jsonschema:"description=It in words."`
	ByService  []ReceiptServiceCost `json:"byService"`
	// Version is the cost of what one version uses (its generated clips, AI
	// voice lines and cloud clean-up), part by part.
	RevisionID string          `json:"revisionId,omitempty"`
	Number     int             `json:"number,omitempty"`
	Version    *costs.Estimate `json:"version,omitempty"`
	Claude     string          `json:"claude" jsonschema:"description=How Claude Code counts\\, in words."`
	Note       string          `json:"note"`
}

// ReceiptServiceCost is what one service cost the project.
type ReceiptServiceCost struct {
	Service  string  `json:"service"`
	USD      float64 `json:"usd"`
	Requests int     `json:"requests"`
}

// ReceiptPosted is one time a person posted a version to a video platform.
type ReceiptPosted struct {
	RevisionID string             `json:"revisionId"`
	Number     int                `json:"number"`
	Post       models.Publication `json:"post"`
}

// ReceiptSentAnyway is a version a person sent out although the quality
// check or a sign-off held it (D299): who, when, their note and what held it.
type ReceiptSentAnyway struct {
	RevisionID string    `json:"revisionId"`
	Number     int       `json:"number"`
	By         string    `json:"by"`
	At         time.Time `json:"at"`
	Note       string    `json:"note"`
	Held       []string  `json:"held" jsonschema:"description=What held it\\, in plain words."`
	// WentOut: it was delivered (or its timeline exported to a client) past
	// those holds with this note; until then it was only allowed to go out.
	WentOut bool `json:"wentOut"`
}

// ProjectReceiptOut is a project's receipt.
type ProjectReceiptOut struct {
	Format      string             `json:"format" jsonschema:"description=metarush.receipt/1"`
	ProjectID   string             `json:"projectId"`
	ProjectName string             `json:"projectName"`
	At          time.Time          `json:"at"`
	Requests    []ReceiptRequest   `json:"requests"`
	Claude      []ReceiptClaude    `json:"claude"`
	Logged      []ReceiptLogged    `json:"logged" jsonschema:"description=Every request to an outside service the activity log recorded for the project\\, by service and what it did."`
	Generated   []ReceiptGenerated `json:"generated"`
	Auto        []ReceiptAuto      `json:"auto"`
	// Posted are the versions people posted to video platforms (D305).
	Posted []ReceiptPosted `json:"posted"`
	// Samples are the samples runs made first in it (D294).
	Samples []ReceiptSample `json:"samples"`
	// SentAnyway are the versions a person sent out past the ship gate (D299).
	SentAnyway []ReceiptSentAnyway `json:"sentAnyway"`
	Cost       ReceiptCost         `json:"cost"`
	// Left is what of the project left this computer, and where (D291).
	Left *ReceiptLeft `json:"left"`
	// Lines say it all in a few plain sentences.
	Lines   []string `json:"lines"`
	Summary string   `json:"summary"`
}

func init() {
	ops.Def("project.receipt", "A project's receipt: every request to an outside AI service (service, model, kind, what was sent, cost, deleted there or not, why), Claude Code's conversations and whose account answered, generated media, Auto's spending, and the cost of the video at cost, per version and project", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ProjectReceiptIn) (*ProjectReceiptOut, error) {
			p, err := ops.ResolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			r, err := ops.ResolveRevision(ctx, a, p, in.Revision, in.Branch)
			if err != nil {
				return nil, err
			}
			return Receipt(ctx, a, p, r)
		})
	// A delivered video's record carries the receipt's part for its version.
	ops.DeliveryReceiptFor = deliveryReceipt
}

// Receipt makes a project's receipt, with the cost of version r (nil: none).
func Receipt(ctx context.Context, a *app.App, p *models.Project, r *models.Revision) (*ProjectReceiptOut, error) {
	// The activity log writes what this process did within the last second
	// or so in the background: written now, the receipt misses none of it
	// (what Claude was shown a moment ago, say).
	a.Log.Flush(ctx)
	out := &ProjectReceiptOut{Format: "metarush.receipt/1", ProjectID: p.ID, ProjectName: p.Name, At: time.Now().UTC(), Requests: []ReceiptRequest{},
		Claude: []ReceiptClaude{}, Logged: []ReceiptLogged{}, Generated: []ReceiptGenerated{}, Auto: []ReceiptAuto{}, Samples: []ReceiptSample{}, SentAnyway: []ReceiptSentAnyway{}, Posted: []ReceiptPosted{}}
	pc := &priced{gens: map[string]costs.Part{}, takes: map[string]costs.Part{}, pinned: map[string][]costs.Part{}}
	if err := receiptGenerations(ctx, a, p.ID, out, pc); err != nil {
		return nil, err
	}
	if err := receiptVoices(ctx, a, p.ID, out, pc); err != nil {
		return nil, err
	}
	if err := receiptCloud(ctx, a, p.ID, out, pc); err != nil {
		return nil, err
	}
	sort.SliceStable(out.Requests, func(i, j int) bool { return out.Requests[i].At.Before(out.Requests[j].At) })
	if err := receiptClaude(ctx, a, p.ID, out); err != nil {
		return nil, err
	}
	out.Logged = receiptLogged(ctx, a, p.ID)
	out.Auto = autoSpentIn(ctx, a, p.ID)
	out.Posted = receiptPosted(ctx, a, p.ID)
	out.Samples = samplesIn(ctx, a, p.ID)
	out.SentAnyway = sentAnywayIn(ctx, a, p.ID)
	receiptGenerated(ctx, a, p.ID, r, out, pc)
	if r != nil {
		pinnedCloud(ctx, a, r, pc)
	}
	out.Cost = receiptCost(out, r, pc)
	out.Left = receiptLeft(ctx, a, p, out)
	out.Lines = append(receiptLines(out, true), out.Left.Summary)
	out.Summary = out.Lines[0]
	return out, nil
}

// priced keeps what each record cost, for the versions that use it: a
// generated clip by its asset, an AI voice take by its ID, cloud clean-up by
// the recording it cleaned.
type priced struct {
	gens  map[string]costs.Part
	takes map[string]costs.Part
	// pinned: what each clearer copy a version plays used in the cloud, by
	// the copy (spec.mix.enhanced[].variant).
	pinned map[string][]costs.Part
}

// path reads a value at a dotted path of a document (nested documents may
// decode as maps or as ordered documents).
func path(d bson.M, p string) any {
	var cur any = d
	for _, k := range strings.Split(p, ".") {
		switch m := cur.(type) {
		case bson.M:
			cur = m[k]
		case map[string]any:
			cur = m[k]
		case bson.D:
			cur = nil
			for _, e := range m {
				if e.Key == k {
					cur = e.Value
					break
				}
			}
		default:
			return nil
		}
	}
	return cur
}

// asM turns a nested document into a map.
func asM(v any) (bson.M, bool) {
	switch m := v.(type) {
	case bson.M:
		return m, true
	case map[string]any:
		return bson.M(m), true
	case bson.D:
		out := bson.M{}
		for _, e := range m {
			out[e.Key] = e.Value
		}
		return out, true
	}
	return nil, false
}

// ownPresenter: a clip read by a person's own presenter (not a stock one).
func ownPresenter(d bson.M) bool {
	stock, _ := path(d, "options.stock").(bool)
	return pstr(d, "options.presenter") != "" && !stock
}

func pstr(d bson.M, paths ...string) string {
	for _, p := range paths {
		if s, ok := path(d, p).(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func pnum(d bson.M, paths ...string) (float64, bool) {
	for _, p := range paths {
		switch v := path(d, p).(type) {
		case float64:
			return v, true
		case int32:
			return float64(v), true
		case int64:
			return float64(v), true
		case int:
			return float64(v), true
		}
	}
	return 0, false
}

func ptime(d bson.M, paths ...string) time.Time {
	for _, p := range paths {
		switch v := path(d, p).(type) {
		case bson.DateTime:
			return v.Time().UTC()
		case time.Time:
			return v.UTC()
		}
	}
	return time.Time{}
}

func parr(d bson.M, p string) []bson.M {
	var out []bson.M
	switch v := path(d, p).(type) {
	case bson.A:
		for _, e := range v {
			if m, ok := asM(e); ok {
				out = append(out, m)
			}
		}
	case []any:
		for _, e := range v {
			if m, ok := asM(e); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// kindWords says what a kind of AI work makes, for a receipt line.
var kindWords = map[string]string{"video": "a generated shot", "picture": "a picture", "voice": "AI speech", "voice-copy": "a voice copy", "voice-change": "a recording in another voice",
	"sound-effect": "a sound effect", "music": "music", "dubbing": "a dubbed copy", "transcription": "a transcript in the cloud", "clean-up": "a clearer voice",
	"presenter": "a presenter clip", "presenter-make": "a person's presenter"}

// whyWords says why a request ran from the channel it came through.
func whyWords(by string, via string) string {
	who := firstNonEmpty(by, "someone")
	switch via {
	case models.ViaAssistant, "assistant-card":
		return who + " kept Claude's card"
	case models.ViaAssistantAuto:
		return "Auto ran Claude's card for " + who
	case "dashboard":
		return who + " asked in the dashboard"
	case "cli":
		return who + " asked from a terminal"
	case "mcp":
		return "asked through MCP for " + who
	case models.ViaRecipe:
		return "a run of Recipes, for " + who
	case "worker":
		return "MetaRush, for " + who
	}
	return "asked by " + who
}

// receiptGenerations reads every generation record of the project, whatever
// its kind of work.
func receiptGenerations(ctx context.Context, a *app.App, projectID string, out *ProjectReceiptOut, pc *priced) error {
	var docs []bson.M
	if err := a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"projectId": projectID}, &docs, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(5000)); err != nil {
		return err
	}
	for _, d := range docs {
		kind := firstNonEmpty(pstr(d, "kind"), "video")
		rq := ReceiptRequest{Kind: kind, What: firstNonEmpty(kindWords[kind], kind), Service: firstNonEmpty(pstr(d, "providerTitle", "provider.title"), providerTitle(pstr(d, "provider", "provider.name"))),
			Model: firstNonEmpty(pstr(d, "modelTitle", "model.title"), pstr(d, "model", "model.id")), At: ptime(d, "createdAt", "at"), By: pstr(d, "createdBy.userName", "by.userName"),
			Prompt: pstr(d, "sentPrompt", "prompt", "request.prompt"), Record: store.CGenerations + "/" + pstr(d, "_id"), Sent: []string{}}
		rq.Why = whyWords(rq.By, firstNonEmpty(pstr(d, "createdBy.via"), pstr(d, "channel")))
		if strings.HasPrefix(pstr(d, "origin"), "say:") { // new words in a speaker's own voice copy (D311)
			rq.What = "new words in a speaker's own voice copy"
		}
		if rq.Prompt != "" {
			rq.Sent = append(rq.Sent, fmt.Sprintf("the words of the request (%d characters)", len([]rune(rq.Prompt))))
		}
		var files [][]string
		for _, ref := range append(parr(d, "references"), parr(d, "inputs")...) {
			what := firstNonEmpty(pstr(ref, "role"), "a file")
			rq.Sent = append(rq.Sent, strings.ReplaceAll(what, "-", " ")+" ("+firstNonEmpty(pstr(ref, "ref", "name"), shortSHA(pstr(ref, "sha256")))+")")
			files = append(files, leave.FileKinds(pstr(ref, "role"), pstr(ref, "mime"), pstr(ref, "assetId") != ""))
		}
		files = append(files, leave.PresenterKinds(pstr(d, "kind"), ownPresenter(d)))
		rq.Kinds = leave.Kinds(rq.Prompt != "", files...)
		rq.Left = (path(d, "ticket") != nil || len(parr(d, "sends")) > 0 || path(d, "outcome.outputs") != nil) && !ranHere(pstr(d, "provider", "provider.name"))
		// What it cost: settled, else what the service reported, else the estimate.
		switch {
		case path(d, "settled") != nil:
			rq.USD, _ = pnum(d, "settled.usd")
			rq.Basis = "charged"
		case pstr(d, "outcome.actual.basis") == "usage":
			rq.USD, _ = pnum(d, "outcome.actual.usd")
			rq.Basis = "charged"
		default:
			if v, ok := pnum(d, "estimate.usd", "cost.usd"); ok {
				rq.USD, rq.Basis = v, "estimate"
			}
		}
		state := pstr(d, "outcome.state")
		switch state {
		case "":
			rq.Outcome = "running"
		case models.GenDone, models.GenFailed, models.GenRefused, models.GenCanceled:
			rq.Outcome = state
		default:
			rq.Outcome = state
		}
		if (state == models.GenFailed || state == models.GenRefused || state == models.GenCanceled) && rq.Basis == "estimate" {
			rq.USD, rq.Basis = 0, "none" // nothing settled for work that never came back
		}
		if rq.USD == 0 && rq.Basis == "" {
			rq.Basis = "none"
		}
		there := firstNonEmpty(rq.Service, "the service")
		switch {
		case kind == "voice-copy" && path(d, "removed") != nil:
			rq.Deleted, rq.DeletedSays = DeletedThere, "The voice copy was withdrawn and deleted at "+there
		case kind == "voice-copy" && path(d, "withdrawn") != nil:
			rq.Deleted, rq.DeletedSays = KeptThere, "The voice copy was withdrawn; MetaRush is deleting it at "+there
		case kind == "voice-copy" && state == models.GenDone:
			rq.Deleted, rq.DeletedSays = KeptThere, "The voice copy stays at "+there+" while it is in use (speech uses it by its ID); withdrawing it deletes it there"
		case kind == "presenter-make" && path(d, "removed") != nil:
			rq.Deleted, rq.DeletedSays = DeletedThere, "The presenter was retired and deleted at "+there
		case kind == "presenter-make" && path(d, "withdrawn") != nil:
			rq.Deleted, rq.DeletedSays = KeptThere, "The presenter was retired; MetaRush is deleting it at "+there
		case kind == "presenter-make" && state == models.GenDone:
			rq.Deleted, rq.DeletedSays = KeptThere, "The presenter stays at "+there+" while it is in use (clips use it by its ID); retiring it deletes it there"
		case path(d, "forgotten.kept") == true:
			rq.Deleted, rq.DeletedSays = KeptThere, there+"'s API offers no way to delete it, so it stays there: "+firstNonEmpty(pstr(d, "forgotten.detail"), "the service's own terms say for how long")
		case path(d, "forgotten") != nil && path(d, "forgotten.ok") == true:
			rq.Deleted, rq.DeletedSays = DeletedThere, "MetaRush deleted the request and its files at "+there+" after the download"
		case path(d, "forgotten") != nil:
			rq.Deleted, rq.DeletedSays = DeleteFailed, "MetaRush tried to delete it at "+firstNonEmpty(rq.Service, "the service")+", and that didn't work: "+firstNonEmpty(pstr(d, "forgotten.detail"), "no reason given")
		case rq.Outcome == "running":
			rq.Deleted, rq.DeletedSays = DeleteNA, "Still running: it is deleted there after the download"
		default:
			rq.Deleted, rq.DeletedSays = DeleteNA, "The record doesn't say whether it was deleted there"
		}
		for _, o := range parr(d, "outcome.outputs") {
			// A clip (a shot, sound, a dub, the clip a cloud transcript wrote
			// down) or a picture: what a version that uses it costs (D226).
			for _, id := range []string{pstr(o, "assetId"), pstr(o, "resourceId")} {
				if id == "" {
					continue
				}
				rq.Outputs = append(rq.Outputs, id)
				p := costs.Part{Service: rq.Service, What: rq.What, Model: rq.Model, USD: rq.USD, Amount: 1, Unit: "requests", Units: "1 request"}
				if had, ok := pc.gens[id]; ok { // several requests for one thing: added up
					p.USD, p.Amount = round6(had.USD+p.USD), had.Amount+1
					p.Units = fmt.Sprintf("%d requests", int(p.Amount))
					if had.What != p.What {
						p.What = had.What + " and " + p.What
					}
				}
				pc.gens[id] = p
			}
		}
		out.Requests = append(out.Requests, rq)
	}
	return nil
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return firstNonEmpty(s, "unnamed")
}

// receiptVoices reads the project's AI voice takes.
func receiptVoices(ctx context.Context, a *app.App, projectID string, out *ProjectReceiptOut, pc *priced) error {
	var takes []models.VoiceTake
	if err := a.DB.Find(ctx, store.CVoiceTakes, a.WorkspaceID(), bson.M{"projectId": projectID, "source": "tts"}, &takes, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(5000)); err != nil {
		return err
	}
	for _, t := range takes {
		service := firstNonEmpty(providerTitle(t.Provider), "an outside service")
		rq := ReceiptRequest{Kind: "voice", What: kindWords["voice"], Service: service, Model: t.ModelID, At: t.CreatedAt, By: t.CreatedBy.UserName,
			Prompt: t.Text, Outcome: "done", Record: store.CVoiceTakes + "/" + t.ID, Outputs: []string{t.AssetID}}
		rq.Why = whyWords(rq.By, t.CreatedBy.Via)
		rq.Sent = []string{fmt.Sprintf("a narration line's words (%d characters) and the voice's settings", firstPositive(t.Characters, len([]rune(t.Text))))}
		rq.Kinds, rq.Left = []string{models.LeaveWords}, true
		if t.Provider == providers.ElevenLabsName || strings.EqualFold(service, providers.ElevenLabsTitle) {
			rq.USD, rq.Basis = round6(providers.SpeechUSD(t.Credits)), "estimate"
		} else {
			rq.Basis = "none"
		}
		if rq.USD == 0 {
			rq.Basis = "none"
		}
		rq.Deleted, rq.DeletedSays = KeptThere, "MetaRush doesn't delete it at "+service
		pc.takes[t.ID] = costs.Part{Service: service, What: "the AI voice", Model: t.ModelID, Source: theirsLine(t.Text), USD: rq.USD, Unit: "credits", Amount: t.Credits,
			Units: fmt.Sprintf("%.0f credits", t.Credits)}
		out.Requests = append(out.Requests, rq)
	}
	return nil
}

func firstPositive(xs ...int) int {
	for _, x := range xs {
		if x > 0 {
			return x
		}
	}
	return 0
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func theirsLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 60 {
		s = string([]rune(s)[:60]) + "…"
	}
	return "“" + s + "”"
}

func providerTitle(name string) string {
	switch strings.ToLower(name) {
	case "":
		return ""
	case providers.ElevenLabsName:
		return providers.ElevenLabsTitle
	case "gemini", "google":
		return "Google Gemini"
	}
	if p, err := providers.Get(strings.ToLower(name)); err == nil {
		return p.Title() // Vertex AI, sync. (D282)
	}
	return name
}

// ranHere reports AI work done on this computer: a model here (D278), or an
// ability (D281), whose run's requests — the hosts it reached — are listed
// with the abilities'. Neither went to an AI service.
func ranHere(provider string) bool {
	p := strings.ToLower(provider)
	return p == "local" || strings.HasPrefix(p, "ability-")
}

// receiptCloud reads the cloud clean-up of the project's footage (cached by
// the recording, so footage several projects share counts in each).
func receiptCloud(ctx context.Context, a *app.App, projectID string, out *ProjectReceiptOut, pc *priced) error {
	ids := ops.ProjectAssetIDs(ctx, a, projectID)
	if len(ids) == 0 {
		return nil
	}
	var recs []struct {
		ID            string       `bson:"_id"`
		Mode          string       `bson:"mode"`
		SourceAssetID string       `bson:"sourceAssetId"`
		From          float64      `bson:"from"`
		To            float64      `bson:"to"`
		Credits       float64      `bson:"credits"`
		CreatedAt     time.Time    `bson:"createdAt"`
		CreatedBy     models.Actor `bson:"createdBy"`
	}
	if err := a.DB.Find(ctx, "sound_cache", a.WorkspaceID(), bson.M{"kind": "wet", "mode": "cloud", "sourceAssetId": bson.M{"$in": ids}}, &recs, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}})); err != nil {
		return err
	}
	for _, w := range recs {
		rq := ReceiptRequest{Kind: "clean-up", What: kindWords["clean-up"], Service: providers.ElevenLabsTitle, Model: providers.IsolationModel, At: w.CreatedAt, By: w.CreatedBy.UserName,
			Outcome: "done", Record: "sound_cache/" + w.ID, Sent: []string{fmt.Sprintf("%s of a recording's sound", secondsWords(w.To-w.From))},
			Kinds: []string{models.LeaveVoices}, Left: true}
		rq.Why = whyWords(rq.By, w.CreatedBy.Via)
		rq.USD, rq.Basis = round6(providers.IsolationUSD(w.Credits)), "estimate"
		if rq.USD == 0 {
			rq.Basis = "none"
		}
		rq.Deleted, rq.DeletedSays = KeptThere, "MetaRush doesn't delete it at ElevenLabs"
		out.Requests = append(out.Requests, rq)
	}
	return nil
}

func secondsWords(s float64) string {
	if s < 90 {
		return fmt.Sprintf("%.0f seconds", math.Max(1, math.Round(s)))
	}
	return fmt.Sprintf("%.1f minutes", s/60)
}

// receiptClaude reads the project's Claude Code conversations.
func receiptClaude(ctx context.Context, a *app.App, projectID string, out *ProjectReceiptOut) error {
	var cs []struct {
		ID               string    `bson:"_id"`
		Title            string    `bson:"title"`
		UserName         string    `bson:"userName"`
		InstallationName string    `bson:"installationName"`
		TurnCount        int       `bson:"turnCount"`
		CostUSD          float64   `bson:"costUsd"`
		ModelUsed        string    `bson:"modelUsed"`
		CreatedAt        time.Time `bson:"createdAt"`
		UpdatedAt        time.Time `bson:"updatedAt"`
		Tokens           struct {
			Total int64 `bson:"total"`
		} `bson:"tokens"`
		Account *struct {
			Who       string `bson:"who"`
			PlanName  string `bson:"planName"`
			Plan      string `bson:"plan"`
			TermsName string `bson:"termsName"`
		} `bson:"account"`
	}
	proj := bson.M{"title": 1, "userName": 1, "installationName": 1, "turnCount": 1, "costUsd": 1, "modelUsed": 1, "createdAt": 1, "updatedAt": 1, "tokens.total": 1, "account": 1}
	if err := a.DB.Find(ctx, store.CAssistantConversations, a.WorkspaceID(), bson.M{"projectId": projectID}, &cs,
		options.Find().SetProjection(proj).SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(2000)); err != nil {
		return err
	}
	for _, c := range cs {
		rc := ReceiptClaude{ConversationID: c.ID, Title: c.Title, StartedBy: c.UserName, Messages: c.TurnCount, Tokens: c.Tokens.Total, Model: c.ModelUsed,
			Machine: c.InstallationName, At: c.CreatedAt, Last: c.UpdatedAt}
		if acc := c.Account; acc != nil {
			rc.Who, rc.Terms, rc.Plan = acc.Who, acc.TermsName, acc.PlanName
			switch acc.Plan {
			case "api", "bedrock", "vertex", "foundry":
				rc.Billed, rc.USD = true, round6(c.CostUSD)
			}
		}
		out.Claude = append(out.Claude, rc)
	}
	return nil
}

// receiptLogged counts the requests to outside services the activity log
// recorded for the project, by service and what they did.
func receiptLogged(ctx context.Context, a *app.App, projectID string) []ReceiptLogged {
	pipe := bson.A{
		bson.M{"$match": bson.M{"workspaceId": a.WorkspaceID(), "kind": logbook.KindProvider, "refs.projectId": projectID}},
		bson.M{"$group": bson.M{"_id": bson.M{"s": "$provider.title", "w": "$provider.what"}, "n": bson.M{"$sum": 1},
			"failed": bson.M{"$sum": bson.M{"$cond": bson.A{"$provider.ok", 0, 1}}}, "first": bson.M{"$min": "$at"}, "last": bson.M{"$max": "$at"}}},
		bson.M{"$sort": bson.M{"n": -1}},
		bson.M{"$limit": 100},
	}
	cur, err := a.DB.C(store.CLogEntries).Aggregate(ctx, pipe)
	out := []ReceiptLogged{}
	if err != nil {
		return out
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var row struct {
			ID struct {
				S string `bson:"s"`
				W string `bson:"w"`
			} `bson:"_id"`
			N      int       `bson:"n"`
			Failed int       `bson:"failed"`
			First  time.Time `bson:"first"`
			Last   time.Time `bson:"last"`
		}
		if cur.Decode(&row) == nil {
			out = append(out, ReceiptLogged{Service: firstNonEmpty(row.ID.S, "an outside service"), What: row.ID.W, Count: row.N, Failed: row.Failed, First: row.First.UTC(), Last: row.Last.UTC()})
		}
	}
	return out
}

// autoSpentIn lists what Auto spent in a project by itself (D241): the
// records the panel keeps when Auto runs a change that costs money.
func autoSpentIn(ctx context.Context, a *app.App, projectID string) []ReceiptAuto {
	var recs []models.AutoSpend
	out := []ReceiptAuto{}
	if err := a.DB.Find(ctx, store.CAutoSpending, a.WorkspaceID(), bson.M{"projectId": projectID}, &recs, options.Find().SetSort(bson.D{{Key: "at", Value: 1}}).SetLimit(2000)); err != nil {
		return out
	}
	for _, x := range recs {
		spent, upTo, pending := ops.AutoSpent(ctx, a, x)
		ra := ReceiptAuto{At: x.At, For: x.ForName, What: x.What, USD: spent, Pending: pending, ConversationID: x.ConversationID}
		if pending {
			ra.UpToUSD = upTo
		}
		out = append(out, ra)
	}
	return out
}

// samplesIn are the samples runs of Recipes made first in a project (D294).
func samplesIn(ctx context.Context, a *app.App, projectID string) []ReceiptSample {
	out := []ReceiptSample{}
	var runs []models.RecipeRun
	if err := a.DB.Find(ctx, store.CRecipeRuns, a.WorkspaceID(), bson.M{"projectId": projectID, "sample": bson.M{"$exists": true}}, &runs,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(200)); err != nil {
		return out
	}
	for _, r := range runs {
		sm := r.Sample
		if sm == nil {
			continue
		}
		x := ReceiptSample{Run: r.Number, Title: r.RecipeTitle /* a run of Recipes */, State: sm.State, What: []string{}, USD: costs.Round(sm.Spent.USD), Note: sm.Note, At: r.CreatedAt}
		if sm.State == models.SampleSkipped {
			x.Note = sm.Skipped
		}
		for _, k := range sm.Kinds {
			x.What = append(x.What, k.What)
		}
		if sm.By != nil {
			x.By = sm.By.UserName
		}
		if sm.At != nil {
			x.At = *sm.At
		}
		out = append(out, x)
	}
	return out
}

// sampleLine says what a sample was, for the receipt's lines.
func sampleLine(x ReceiptSample) string {
	who := x.By
	if who == "" {
		who = "someone"
	}
	run := fmt.Sprintf("run %d of %s", x.Run, q(x.Title))
	if x.State == models.SampleSkipped {
		return fmt.Sprintf("%s went without its sample; %s's reason: %s.", strings.ToUpper(run[:1])+run[1:], who, q(x.Note))
	}
	l := fmt.Sprintf("A sample came first in %s: %s, %s", run, andList(x.What), moneyWords(x.USD))
	switch x.State {
	case models.SampleApproved:
		l += ", approved by " + who
	case models.SampleNotQuite:
		l += "; " + who + " said it wasn't right: " + q(x.Note)
	case models.SampleWaiting:
		l += ", waiting for a person's approval"
	case models.SampleStopped:
		l += "; the run stopped before anyone decided"
	default:
		l += ", still being made"
	}
	return l + "."
}

// receiptGenerated lists the project's generated clips and AI narration.
func receiptGenerated(ctx context.Context, a *app.App, projectID string, r *models.Revision, out *ProjectReceiptOut, pc *priced) {
	used := map[string]bool{}
	usedTakes := map[string]bool{}
	if r != nil {
		for _, id := range editspec.AssetIDs(&r.Spec) {
			used[id] = true
		}
		if v := r.Spec.Voiceover; v != nil {
			for _, b := range v.Beats {
				if b.Take != nil && b.Take.TakeID != "" {
					usedTakes[b.Take.TakeID] = true
				}
			}
		}
	}
	var gen []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"projectIds": projectID, "source.platform": "generated"}, &gen, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(2000))
	for _, as := range gen {
		g := ReceiptGenerated{AssetID: as.ID, Title: firstNonEmpty(as.DisplayTitle(), as.Name), Kind: "shot", What: "a generated shot", Seconds: math.Round(as.Media.Duration*10) / 10, InVersion: used[as.ID]}
		if src := as.Source; src != nil && src.Generated != nil && src.Generated.Kind != "" && src.Generated.Kind != models.GenKindVideo {
			// Other AI work that lands as a clip (D226): speech, sound effects, music, a voice change, a dub.
			g.Kind, g.What = "sound", firstNonEmpty(kindWords[src.Generated.Kind], src.Generated.Kind)
			if src.Generated.Kind == "presenter" {
				g.Kind = "presenter" // a presenter reading lines on camera (D277)
			}
			if sd := src.Generated.Said; sd != nil {
				g.What = fmt.Sprintf("“%s” in place of “%s”, in %s's own voice copy", sd.Words, sd.Before, firstWho(sd.Who))
			}
		}
		if p, ok := pc.gens[as.ID]; ok {
			g.Service, g.Model = p.Service, p.Model
		}
		out.Generated = append(out.Generated, g)
	}
	// Words changed in a speaker's voice copy that the version plays from a
	// cut it nests (D311): made, and paid for, in that cut's project.
	if r != nil {
		heard, _ := ops.SaidHeardInVersion(ctx, a, r)
		for _, h := range heard {
			c := h.Change
			if h.Nested == "" || c == nil || c.Sound == "" || used[c.Sound] {
				continue
			}
			used[c.Sound] = true
			g := ReceiptGenerated{AssetID: c.Sound, Title: "new words", Kind: "sound", InVersion: true, Service: c.ProviderTitle, Model: c.Model,
				What: fmt.Sprintf("“%s” in place of “%s”, in %s's own voice copy (in a cut played in this one; on its project's receipt)", c.Words, c.Before, firstWho(c.Speaker.Name))}
			if c.Fit != nil {
				g.Seconds = math.Round((c.Fit.To-c.Fit.From)*10) / 10
			}
			out.Generated = append(out.Generated, g)
		}
	}
	// Pictures an AI service made for the project (resources made with kind ai).
	usedPics := map[string]bool{}
	if r != nil {
		for _, id := range editspec.ResourceIDs(&r.Spec) {
			usedPics[id] = true
		}
	}
	var pics []models.Resource
	_ = a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"projectId": projectID, "versions.source.madeWith.kind": resources.MakeAI}, &pics,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(2000))
	for _, res := range pics {
		g := ReceiptGenerated{ResourceID: res.ID, Title: firstNonEmpty(res.Title, res.Name), Kind: "picture", What: kindWords["picture"], InVersion: usedPics[res.ID]}
		if p, ok := pc.gens[res.ID]; ok {
			g.Service, g.Model = p.Service, p.Model
		}
		out.Generated = append(out.Generated, g)
	}
	var takes []models.VoiceTake
	_ = a.DB.Find(ctx, store.CVoiceTakes, a.WorkspaceID(), bson.M{"projectId": projectID, "aiVoice": true}, &takes, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(5000))
	for _, t := range takes {
		out.Generated = append(out.Generated, ReceiptGenerated{AssetID: t.AssetID, Title: theirsLine(t.Text), Kind: "voice", Service: firstNonEmpty(providerTitle(t.Provider), "an outside service"), Model: t.ModelID,
			Seconds: math.Round(t.Duration*10) / 10, InVersion: usedTakes[t.ID]})
	}
}

// VersionCost is what one version uses of the outside AI work, at cost: its
// generated clips (shots, AI sound and dubs, clips written down in the cloud),
// AI pictures, AI voice lines and the cloud clean-up of its recordings.
func versionCost(r *models.Revision, pc *priced) *costs.Estimate {
	var parts []costs.Part
	seen := map[string]bool{}
	for _, c := range r.Spec.Clips {
		if p, ok := pc.gens[c.AssetID]; ok && !seen[c.AssetID] {
			seen[c.AssetID] = true
			p.Source = "a clip of the cut"
			parts = append(parts, p)
		}
	}
	for _, t := range r.Spec.Audio { // AI music and sound effects play as tracks (D226)
		if p, ok := pc.gens[t.AssetID]; ok && !seen[t.AssetID] {
			seen[t.AssetID] = true
			p.Source = "a sound track of the cut"
			parts = append(parts, p)
		}
	}
	for _, id := range editspec.ResourceIDs(&r.Spec) {
		if p, ok := pc.gens[id]; ok && !seen[id] {
			seen[id] = true
			p.Source = "a picture of the cut"
			parts = append(parts, p)
		}
	}
	if v := r.Spec.Voiceover; v != nil {
		for i, b := range v.Beats {
			if b.Take == nil || b.Take.TakeID == "" || seen[b.Take.TakeID] {
				continue
			}
			if p, ok := pc.takes[b.Take.TakeID]; ok {
				seen[b.Take.TakeID] = true
				p.Source = fmt.Sprintf("Line %d: %s", i+1, p.Source)
				parts = append(parts, p)
			}
		}
	}
	if r.Spec.Mix != nil {
		for _, c := range r.Spec.Mix.Said { // words changed in a speaker's own voice copy (D311)
			if p, ok := pc.gens[c.Sound]; ok && !seen[c.Sound] {
				seen[c.Sound] = true
				p.Source = fmt.Sprintf("“%s” in place of “%s”, in %s's own voice copy", c.Words, c.Before, firstWho(c.Who))
				parts = append(parts, p)
			}
		}
		for _, e := range r.Spec.Mix.Enhanced {
			if seen["copy:"+e.Variant] {
				continue
			}
			seen["copy:"+e.Variant] = true
			for _, p := range pc.pinned[e.Variant] {
				p.Source = "a recording of the cut"
				parts = append(parts, p)
			}
		}
	}
	e := costs.Of(parts...)
	return &e
}

// receiptCost adds the project's cost up by service, and prices the version.
func receiptCost(out *ProjectReceiptOut, r *models.Revision, pc *priced) ReceiptCost {
	c := ReceiptCost{ByService: []ReceiptServiceCost{}, Note: "At cost: what each service charges at its own prices, with nothing added by MetaRush. Estimates are at the dated prices MetaRush keeps; what a plan's credits cost may be less."}
	by := map[string]*ReceiptServiceCost{}
	var order []string
	add := func(service string, usd float64) {
		if by[service] == nil {
			by[service] = &ReceiptServiceCost{Service: service}
			order = append(order, service)
		}
		by[service].USD = round6(by[service].USD + usd)
		by[service].Requests++
	}
	for _, rq := range out.Requests {
		add(firstNonEmpty(rq.Service, "an outside service"), rq.USD)
	}
	billed := 0.0
	billedAny := false
	for _, cl := range out.Claude {
		if cl.Billed {
			billedAny = true
			billed += cl.USD
		}
	}
	if billedAny {
		add("Claude (API)", billed)
	}
	for _, s := range order {
		c.ByService = append(c.ByService, *by[s])
		c.ProjectUSD += by[s].USD
	}
	c.ProjectUSD = costs.Round(c.ProjectUSD)
	c.Project = moneyWords(c.ProjectUSD)
	switch {
	case len(out.Claude) == 0:
		c.Claude = "No Claude Code conversations about this project in the panel."
	case billedAny:
		c.Claude = "Claude Code's conversations with an API key are billed per token, as Claude Code reported them."
	default:
		c.Claude = "Claude Code's conversations count toward the Claude plan's limits: no extra charge."
	}
	if r != nil {
		c.RevisionID, c.Number = r.ID, r.Number
		c.Version = versionCost(r, pc)
	}
	return c
}

// moneyWords says an amount the way the receipt does: $1.24, less than a cent, nothing.
func moneyWords(usd float64) string {
	switch {
	case usd <= 0:
		return "nothing"
	case usd < 0.01:
		return "less than a cent"
	}
	return fmt.Sprintf("$%.2f", usd)
}

// receiptLines says the receipt in a few plain sentences.
func receiptLines(out *ProjectReceiptOut, whose bool) []string {
	var lines []string
	n := len(out.Requests)
	switch n {
	case 0:
		lines = append(lines, fmt.Sprintf("%s sent nothing to outside AI services: its cost at cost is nothing.", q(out.ProjectName)))
	default:
		kinds := map[string]int{}
		var order []string
		for _, r := range out.Requests {
			if kinds[r.What] == 0 {
				order = append(order, r.What)
			}
			kinds[r.What]++
		}
		var parts []string
		for _, k := range order {
			parts = append(parts, fmt.Sprintf("%s × %d", k, kinds[k]))
		}
		var services []string
		for _, s := range out.Cost.ByService {
			services = append(services, s.Service)
		}
		lines = append(lines, fmt.Sprintf("%s made %s to outside AI services (%s: %s); its cost at cost is %s.", q(out.ProjectName), count(n, "request", "requests"),
			andList(services), strings.Join(parts, ", "), out.Cost.Project))
	}
	if v := out.Cost.Version; v != nil {
		if v.USD > 0 {
			lines = append(lines, fmt.Sprintf("Version %d uses %s of it.", out.Cost.Number, moneyWords(v.USD)))
		} else {
			lines = append(lines, fmt.Sprintf("Version %d costs nothing at cost.", out.Cost.Number))
		}
	}
	deleted, kept := 0, 0
	for _, r := range out.Requests {
		switch r.Deleted {
		case DeletedThere:
			deleted++
		case KeptThere:
			kept++
		}
	}
	if deleted > 0 {
		lines = append(lines, fmt.Sprintf("%s deleted at the service after the download.", count1(deleted, "request was", "requests were")))
	}
	if kept > 0 {
		lines = append(lines, fmt.Sprintf("%s stay at the service: MetaRush doesn't delete them there.", count1(kept, "request's results", "requests' results")))
	}
	if len(out.Claude) > 0 {
		who := claudeAnswers(out.Claude, whose)
		l := fmt.Sprintf("Claude: %s in the panel", count(len(out.Claude), "conversation", "conversations"))
		if len(who) > 0 {
			if whose {
				l += ", answered by " + strings.Join(who, " and ")
			} else {
				l += ", answered with " + strings.Join(who, " and ")
			}
		}
		lines = append(lines, l+". "+out.Cost.Claude)
	}
	byKind := map[string]int{}
	for _, g := range out.Generated {
		byKind[g.Kind]++
	}
	if len(out.Generated) > 0 {
		var g []string
		for _, k := range []struct{ kind, one, many string }{{"shot", "generated shot", "generated shots"}, {"picture", "AI picture", "AI pictures"},
			{"sound", "AI sound clip", "AI sound clips"}, {"presenter", "AI presenter clip", "AI presenter clips"}, {"voice", "line of AI narration", "lines of AI narration"}} {
			if n := byKind[k.kind]; n > 0 {
				g = append(g, count(n, k.one, k.many))
			}
		}
		lines = append(lines, "Generated media in it: "+andList(g)+".")
	}
	for _, x := range out.Samples {
		lines = append(lines, sampleLine(x))
	}
	for _, x := range out.SentAnyway {
		if x.WentOut {
			lines = append(lines, fmt.Sprintf("Version %d went out although %s held it: %s sent it out anyway on %s (%s).", x.Number,
				count(len(x.Held), "thing", "things"), x.By, x.At.UTC().Format("2 Jan 2006"), q(x.Note)))
			continue
		}
		lines = append(lines, fmt.Sprintf("Version %d was allowed to go out although %s held it: %s let it go anyway on %s (%s); it hasn't gone out yet.", x.Number,
			count(len(x.Held), "thing", "things"), x.By, x.At.UTC().Format("2 Jan 2006"), q(x.Note)))
	}
	if len(out.Auto) > 0 {
		total, upTo, pending := 0.0, 0.0, false
		for _, x := range out.Auto {
			total += x.USD
			if x.Pending {
				pending, upTo = true, upTo+x.UpToUSD
			} else {
				upTo += x.USD
			}
		}
		if pending {
			lines = append(lines, fmt.Sprintf("Auto spent up to %s by itself, on %s (some of it is still being made).", moneyWords(costs.Round(upTo)), count(len(out.Auto), "change", "changes")))
		} else {
			lines = append(lines, fmt.Sprintf("Auto spent %s by itself, on %s.", moneyWords(costs.Round(total)), count(len(out.Auto), "change", "changes")))
		}
	}
	for _, x := range out.Posted {
		if x.Post.State != models.PublicationPosted {
			continue
		}
		who := ""
		if whose && x.Post.By.UserName != "" {
			who = " by " + x.Post.By.UserName
		}
		lines = append(lines, fmt.Sprintf("Version %d was posted to %s as “%s” (%s)%s on %s.", x.Number, platformWords(x.Post.Platform), x.Post.Channel, x.Post.Privacy, who, x.Post.At.Format("2 Jan 2006")))
	}
	return lines
}

func platformWords(p string) string {
	if p == "youtube" {
		return "YouTube"
	}
	return p
}

// receiptPosted lists every post of the project's versions to a platform.
func receiptPosted(ctx context.Context, a *app.App, projectID string) []ReceiptPosted {
	var revs []models.Revision
	_ = a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"projectId": projectID, "publications.0": bson.M{"$exists": true}}, &revs,
		options.Find().SetProjection(bson.M{"number": 1, "publications": 1}))
	out := []ReceiptPosted{}
	for _, r := range revs {
		for _, p := range r.Publications {
			out = append(out, ReceiptPosted{RevisionID: r.ID, Number: r.Number, Post: p})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Post.At.Before(out[j].Post.At) })
	return out
}

// sentAnywayIn are the project's versions a person sent out although
// something held them (D299), oldest first.
func sentAnywayIn(ctx context.Context, a *app.App, projectID string) []ReceiptSentAnyway {
	out := []ReceiptSentAnyway{}
	var revs []models.Revision
	if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"projectId": projectID, "shipOverrides.0": bson.M{"$exists": true}}, &revs,
		options.Find().SetProjection(bson.M{"number": 1, "shipOverrides": 1, "sentOut": 1}).SetSort(bson.D{{Key: "number", Value: 1}})); err != nil {
		return out
	}
	for _, r := range revs {
		went := map[string]bool{}
		for _, s := range r.SentOut {
			for _, id := range s.Overrides {
				went[id] = true
			}
		}
		for _, o := range r.ShipOverrides {
			x := ReceiptSentAnyway{RevisionID: r.ID, Number: r.Number, By: firstNonEmpty(o.By.UserName, "a person"), At: o.At, Note: o.Note, Held: []string{}, WentOut: went[o.ID]}
			for _, h := range o.Holds {
				x.Held = append(x.Held, h.Says)
			}
			out = append(out, x)
		}
	}
	return out
}

// deliveryReceipt is a version's receipt for its delivery record.
func deliveryReceipt(ctx context.Context, a *app.App, p *models.Project, r *models.Revision) *ops.DeliveryReceipt {
	rc, err := Receipt(ctx, a, p, r)
	if err != nil {
		return nil
	}
	// A delivered file goes to other people: it says the plan and the terms
	// Claude answered under, never whose account it is (D239).
	d := &ops.DeliveryReceipt{Format: rc.Format, ProjectUSD: rc.Cost.ProjectUSD, Requests: len(rc.Requests), Services: []string{}, Claude: len(rc.Claude), Generated: len(rc.Generated),
		Lines: receiptLines(rc, false), Command: "metarush project receipt " + p.ID + " --json"}
	if v := rc.Cost.Version; v != nil {
		d.CostUSD, d.Cost = v.USD, v
	}
	for _, s := range rc.Cost.ByService {
		d.Services = append(d.Services, s.Service)
	}
	for _, rq := range rc.Requests {
		if rq.Deleted == DeletedThere {
			d.Deleted++
		}
	}
	d.ClaudeAnswers = claudeAnswers(rc.Claude, false)
	for _, x := range rc.Auto {
		d.AutoUSD += x.USD
		d.AutoPending = d.AutoPending || x.Pending
	}
	d.AutoUSD = costs.Round(d.AutoUSD)
	return d
}

// Words for sentences.

func q(s string) string { return plainwords.TheirsOpen + s + plainwords.TheirsClose }

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func count1(n int, one, many string) string {
	s := count(n, one, many)
	return strings.ToUpper(s[:1]) + s[1:]
}

func andList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// pinnedCloud prices what a version's clearer copies used in the cloud: each
// copy it plays (spec.mix.enhanced[].variant) is followed to its record in
// sound_cache and the processed voices it is made of; only cloud ones cost
// money (a copy made on this computer is free, whatever else was made of the
// same recording). Copies made before their records listed their voices
// (no wets) are priced only when the copy says it used the cloud, from the
// cloud voices of their recording that overlap the copy's stretch and were
// made before it, one per stretch: tries of the same stretch aren't added up
// (legacyPieces).
func pinnedCloud(ctx context.Context, a *app.App, r *models.Revision, pc *priced) {
	if r.Spec.Mix == nil {
		return
	}
	ws := a.WorkspaceID()
	for _, e := range r.Spec.Mix.Enhanced {
		if _, done := pc.pinned[e.Variant]; done || e.Variant == "" {
			continue
		}
		pc.pinned[e.Variant] = nil
		var cp struct {
			Wets          []string  `bson:"wets"`
			SourceAssetID string    `bson:"sourceAssetId"`
			From          float64   `bson:"from"`
			To            float64   `bson:"to"`
			CreatedAt     time.Time `bson:"createdAt"`
			Scores        []struct {
				Mode string `bson:"mode"`
			} `bson:"scores"`
		}
		if a.DB.C("sound_cache").FindOne(ctx, bson.M{"workspaceId": ws, "kind": "copy", "assetId": e.Variant}).Decode(&cp) != nil {
			continue // not a copy MetaRush knows: nothing to price
		}
		var filter bson.M
		if len(cp.Wets) > 0 {
			keys := make([]string, 0, len(cp.Wets))
			for _, k := range cp.Wets {
				keys = append(keys, ws+"/wet:"+k)
			}
			filter = bson.M{"workspaceId": ws, "_id": bson.M{"$in": keys}, "mode": "cloud"}
		} else {
			cloud := false
			for _, s := range cp.Scores {
				cloud = cloud || s.Mode == "cloud"
			}
			if !cloud {
				continue
			}
			filter = bson.M{"workspaceId": ws, "kind": "wet", "mode": "cloud", "sourceAssetId": cp.SourceAssetID, "from": bson.M{"$lt": cp.To}, "to": bson.M{"$gt": cp.From}}
			if !cp.CreatedAt.IsZero() {
				filter["createdAt"] = bson.M{"$lte": cp.CreatedAt}
			}
		}
		var wets []wetPiece
		if a.DB.Find(ctx, "sound_cache", ws, filter, &wets) != nil {
			continue
		}
		if len(cp.Wets) == 0 {
			wets = legacyPieces(wets)
		}
		for _, w := range wets {
			pc.pinned[e.Variant] = append(pc.pinned[e.Variant], costs.Part{Service: providers.ElevenLabsTitle, What: "a clearer voice", Model: providers.IsolationModel,
				USD: round6(providers.IsolationUSD(w.Credits)), Unit: "credits", Amount: w.Credits, Units: fmt.Sprintf("%.0f credits (%s of sound)", w.Credits, secondsWords(w.To-w.From))})
		}
	}
}

// wetPiece is a processed voice: a stretch of a recording and what it cost.
type wetPiece struct {
	From      float64   `bson:"from"`
	To        float64   `bson:"to"`
	Credits   float64   `bson:"credits"`
	CreatedAt time.Time `bson:"createdAt"`
}

// legacyPieces picks, from the cloud voices a copy without a list of its
// own may be made of, one per stretch: the stretches that don't overlap,
// earliest ending first (the most of them that fit side by side, as a copy's
// pieces do), the newest try of the same stretch first. An estimate: the
// copy never said which tries it used.
func legacyPieces(wets []wetPiece) []wetPiece {
	sort.SliceStable(wets, func(i, j int) bool {
		if wets[i].To != wets[j].To {
			return wets[i].To < wets[j].To
		}
		if wets[i].From != wets[j].From {
			return wets[i].From > wets[j].From
		}
		return wets[i].CreatedAt.After(wets[j].CreatedAt)
	})
	var out []wetPiece
	end := math.Inf(-1)
	for _, w := range wets {
		if w.From >= end {
			out = append(out, w)
			end = w.To
		}
	}
	return out
}

// claudeAnswers says whose Claude answered the conversations, once each: the
// account (whose, for people who can see the receipt) or only its plan and
// terms (for delivered files).
func claudeAnswers(cs []ReceiptClaude, whose bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range cs {
		s := ""
		switch {
		case whose && c.Who != "":
			s = c.Who + ", under " + c.Terms
		case !whose && c.Terms != "":
			s = aPlan(c.Plan) + ", under " + c.Terms
		}
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// aPlan says a plan with its article: "a Claude Max plan", "an Anthropic API key".
func aPlan(plan string) string {
	switch {
	case plan == "":
		return "a Claude sign-in"
	case strings.HasPrefix(plan, "a ") || strings.HasPrefix(plan, "an "):
		return plan
	case strings.HasPrefix(plan, "Claude "):
		return "a " + plan + " plan"
	}
	return plan
}

func firstWho(who string) string {
	if strings.TrimSpace(who) == "" {
		return "the speaker"
	}
	return strings.TrimSpace(who)
}
