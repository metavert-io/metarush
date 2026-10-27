package receipt

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/store"
)

// What left this computer (D291): for a project, everything of it that went
// somewhere — AI services (each request), Claude and AI apps (what MetaRush
// showed them, from the activity log, and the conversations themselves),
// abilities (each host a run reached), delivery (sync folders, Recipes'
// files) and cloud storage (where the workspace keeps its media and words
// now) — with the refusals of its What may leave, and "nothing left" when
// that is so. The receipt carries it; `project.leaves` is it alone.

// ReceiptLeft is what of a project left this computer, and where.
type ReceiptLeft struct {
	// Nothing: nothing of the project left this computer, and none of it is
	// kept in cloud storage.
	Nothing bool               `json:"nothing"`
	Places  []ReceiptLeftPlace `json:"places"`
	// Stored says where the workspace keeps the project's things, when that
	// is cloud storage.
	Stored  []string         `json:"stored"`
	Refused []ReceiptRefusal `json:"refused"`
	// Rules are the project's What may leave, in words.
	Rules   string   `json:"rules"`
	Lines   []string `json:"lines"`
	Summary string   `json:"summary"`
}

// ReceiptLeftPlace is one receiver things of the project went to.
type ReceiptLeftPlace struct {
	Place string `json:"place" jsonschema:"enum=ai,enum=abilities,enum=claude,enum=storage,enum=delivery,enum=reviewers"`
	// To is who received them, in words.
	To string `json:"to"`
	// Kinds are what went (footage, voices, faces, words, pictures).
	Kinds []string  `json:"kinds"`
	Count int       `json:"count"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
	// What lists a few of them, in words.
	What []string `json:"what"`
}

// ReceiptRefusal is something What may leave kept on this computer.
type ReceiptRefusal struct {
	At      time.Time `json:"at"`
	Place   string    `json:"place"`
	Kind    string    `json:"kind,omitempty"`
	To      string    `json:"to,omitempty"`
	What    string    `json:"what,omitempty"`
	By      string    `json:"by,omitempty" jsonschema:"description=Who asked for it."`
	Message string    `json:"message"`
}

// ProjectLeavesIn names the project.
type ProjectLeavesIn struct {
	ProjectID string `json:"projectId"`
}

// ProjectLeavesOut is what of a project left this computer.
type ProjectLeavesOut struct {
	ProjectID   string      `json:"projectId"`
	ProjectName string      `json:"projectName"`
	At          time.Time   `json:"at"`
	Left        ReceiptLeft `json:"left"`
}

func init() {
	ops.Def("project.leaves", "What of a project left this computer, and where: every request to an AI service, what Claude and AI apps were shown, the places abilities' runs reached, deliveries to folders a sync service uploads and Recipes' files, and cloud storage holding its media — with what its What may leave rules kept here; says so when nothing left", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ProjectLeavesIn) (*ProjectLeavesOut, error) {
			p, err := ops.ResolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			r, err := Receipt(ctx, a, p, nil)
			if err != nil {
				return nil, err
			}
			return &ProjectLeavesOut{ProjectID: p.ID, ProjectName: p.Name, At: r.At, Left: *r.Left}, nil
		})
}

// placeOrder is the order places are listed in.
var placeOrder = map[string]int{models.PlaceAI: 0, models.PlaceClaude: 1, models.PlaceAbilities: 2, models.PlaceDelivery: 3, models.PlaceReviewers: 4, models.PlaceStorage: 5}

// receiptLeft gathers what left, from the receipt's requests and Claude's
// conversations and from the records of runs, deliveries and refusals.
func receiptLeft(ctx context.Context, a *app.App, p *models.Project, out *ProjectReceiptOut) *ReceiptLeft {
	l := &ReceiptLeft{Places: []ReceiptLeftPlace{}, Stored: []string{}, Refused: []ReceiptRefusal{}}
	byKey := map[string]*ReceiptLeftPlace{}
	add := func(place, to string, kinds []string, at time.Time, what string) {
		k := place + "\x00" + to
		x := byKey[k]
		if x == nil {
			x = &ReceiptLeftPlace{Place: place, To: to, Kinds: []string{}, What: []string{}, First: at, Last: at}
			byKey[k] = x
		}
		x.Count++
		for _, kind := range kinds {
			if !slices.Contains(x.Kinds, kind) {
				x.Kinds = append(x.Kinds, kind)
			}
		}
		if !at.IsZero() && (x.First.IsZero() || at.Before(x.First)) {
			x.First = at
		}
		if at.After(x.Last) {
			x.Last = at
		}
		if what != "" && len(x.What) < 5 && !slices.Contains(x.What, what) {
			x.What = append(x.What, what)
		}
	}
	// The project's things: its clips (those in the Trash too), pictures and
	// versions, wherever work on them was started from.
	th := projectThings(ctx, a, p.ID)
	// AI services: every request that reached one — the project's, other
	// projects' (or none's) that sent its clips or pictures — and voice copies.
	for _, r := range out.Requests {
		if r.Left {
			add(models.PlaceAI, r.Service, r.Kinds, r.At, r.What)
		}
	}
	otherAIWork(ctx, a, p.ID, th, add)
	voiceCopiesLeft(ctx, a, p.ID, th, add)
	// Claude and AI apps: what MetaRush showed them, and the conversations.
	claudeLeft(ctx, a, p.ID, th, out, add)
	// Abilities: the hosts their runs reached.
	abilitiesLeft(ctx, a, p.ID, th, add)
	// Delivery: sync folders, Recipes' files; Remotion pushes and refreshes.
	deliveriesLeft(ctx, a, p.ID, add)
	remotionLeft(ctx, a, p.ID, add)
	// People outside the workspace: review links (D314, D317).
	reviewLinksLeft(ctx, a, p.ID, add)
	for _, x := range byKey {
		x.Kinds = ordered(x.Kinds)
		l.Places = append(l.Places, *x)
	}
	sort.SliceStable(l.Places, func(i, j int) bool {
		if placeOrder[l.Places[i].Place] != placeOrder[l.Places[j].Place] {
			return placeOrder[l.Places[i].Place] < placeOrder[l.Places[j].Place]
		}
		return l.Places[i].First.Before(l.Places[j].First)
	})
	// Cloud storage: where its things are kept now.
	if len(ops.ProjectAssetIDs(ctx, a, p.ID)) > 0 {
		if k := leave.MediaKept(a.Cfg.Media); k.Cloud {
			l.Stored = append(l.Stored, "Its footage, sound and pictures are kept in "+k.Words+".")
		}
	}
	if k := leave.DatabaseKept(a.Cfg.Database); k.Cloud {
		l.Stored = append(l.Stored, "Its transcripts, names and notes are kept in "+k.Words+".")
	}
	l.Refused = refusals(ctx, a, p.ID)
	rules, _, _ := leave.For(ctx, a, p.ID)
	l.Rules = leave.Summary(rules) + "."
	l.Nothing = len(l.Places) == 0 && len(l.Stored) == 0
	l.Lines = leftLines(p.Name, l)
	l.Summary = l.Lines[0]
	return l
}

func ordered(kinds []string) []string {
	out := []string{}
	for _, k := range models.LeaveKinds {
		if slices.Contains(kinds, k) {
			out = append(out, k)
		}
	}
	return out
}

// claudeLeft lists what Claude and AI apps were shown of the project (the
// activity log's record of each operation that shows words or pictures, on
// their channels) and the panel's conversations about it.
func claudeLeft(ctx context.Context, a *app.App, projectID string, th things, out *ProjectReceiptOut, add func(place, to string, kinds []string, at time.Time, what string)) {
	convs := map[string]ReceiptClaude{}
	convIDs := []string{} // never nil: $in takes only an array
	for _, c := range out.Claude {
		convs[c.ConversationID] = c
		convIDs = append(convIDs, c.ConversationID)
	}
	shown := map[string]bool{}
	names := leave.ShowingReads()
	filter := bson.M{"workspaceId": a.WorkspaceID(), "kind": logbook.KindOp, "op.ok": true, "op.name": bson.M{"$in": names},
		"channel": bson.M{"$in": bson.A{logbook.ChannelAssistant, logbook.ChannelClaudeTerminal, logbook.ChannelMCP}},
		"$or": bson.A{bson.M{"refs.projectId": projectID}, bson.M{"refs.conversationId": bson.M{"$in": convIDs}}, bson.M{"refs.assetId": bson.M{"$in": th.assets}},
			bson.M{"refs.revisionId": bson.M{"$in": th.revisions}}}}
	var rows []struct {
		At      time.Time `bson:"at"`
		Channel string    `bson:"channel"`
		Agent   string    `bson:"agent"`
		Op      struct {
			Name string `bson:"name"`
		} `bson:"op"`
		Refs struct {
			ConversationID string `bson:"conversationId"`
		} `bson:"refs"`
	}
	_ = a.DB.Find(ctx, store.CLogEntries, a.WorkspaceID(), filter, &rows, options.Find().SetSort(bson.D{{Key: "at", Value: 1}}).SetLimit(5000).
		SetProjection(bson.M{"at": 1, "channel": 1, "agent": 1, "op.name": 1, "refs.conversationId": 1}))
	for _, r := range rows {
		to := claudeWho(r.Channel, r.Agent, convs[r.Refs.ConversationID])
		kinds, _ := leave.ReadKinds(r.Op.Name)
		add(models.PlaceClaude, to, kinds, r.At, "what "+logbook.OpWords(r.Op.Name)+" shows")
		if r.Refs.ConversationID != "" {
			shown[r.Refs.ConversationID] = true
		}
	}
	// The conversations themselves went to Claude: its messages, the
	// project's names and notes.
	for _, c := range out.Claude {
		if shown[c.ConversationID] {
			continue
		}
		add(models.PlaceClaude, claudeWho(logbook.ChannelAssistant, "", c), nil, c.At, "the conversation “"+c.Title+"”")
	}
}

func claudeWho(channel, agent string, c ReceiptClaude) string {
	switch channel {
	case logbook.ChannelMCP:
		if agent != "" {
			return "the AI app " + agent
		}
		return "an AI app"
	case logbook.ChannelClaudeTerminal:
		return "Claude Code in a terminal"
	}
	if c.Who != "" {
		return "Claude in the panel (" + c.Who + ")"
	}
	return "Claude in the panel"
}

// abilitiesLeft lists the hosts the project's ability runs reached, with
// what each run sent (its inputs its manifest says it sends).
func abilitiesLeft(ctx context.Context, a *app.App, projectID string, th things, add func(place, to string, kinds []string, at time.Time, what string)) {
	var runs []models.AbilityRun
	refs := append(append([]string{}, th.assets...), th.resources...)
	if err := a.DB.Find(ctx, store.CAbilityRuns, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"projectId": projectID}, bson.M{"inputs.ref": bson.M{"$in": refs}}}}, &runs,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(2000)); err != nil {
		return
	}
	sends := map[string][]string{}
	for _, r := range runs {
		if _, ok := sends[r.Hash]; !ok {
			var v struct {
				Facts struct {
					Sends []string `bson:"sends"`
				} `bson:"facts"`
			}
			_ = a.DB.C(store.CAbilities).FindOne(ctx, bson.M{"_id": a.WorkspaceID() + "/" + r.Hash}).Decode(&v)
			sends[r.Hash] = v.Facts.Sends
		}
		kinds := runKinds(r.Inputs, sends[r.Hash])
		hosts := map[string]time.Time{}
		for _, q := range r.Requests {
			// The ability's one service on this computer (D284): nothing
			// sent there left it.
			if q.Refused != "" || sandbox.ThisComputer(q.Host) {
				continue
			}
			if t, ok := hosts[q.Host]; !ok || q.At.Before(t) {
				hosts[q.Host] = q.At
			}
		}
		for h, at := range hosts {
			add(models.PlaceAbilities, h, kinds, at, fmt.Sprintf("run %d of “%s”", r.Number, r.Title))
		}
	}
}

// runKinds are what a run sent of its inputs (what its manifest sends).
func runKinds(inputs []models.AbilityRunInput, sends []string) []string {
	var out []string
	for _, in := range inputs {
		switch {
		case in.Ref != "" && in.Kind == "footage" && slices.Contains(sends, "footage"):
			out = append(out, leave.FootageKinds...)
		case in.Ref != "" && in.Kind == "sound" && slices.Contains(sends, "sound"):
			out = append(out, models.LeaveVoices)
		case in.Ref != "" && in.Kind == "picture" && slices.Contains(sends, "pictures"):
			out = append(out, models.LeavePictures)
		case in.Ref == "" && in.Kind == "words" && slices.Contains(sends, "words"):
			out = append(out, models.LeaveWords)
		}
	}
	return ordered(out)
}

// deliveriesLeft lists deliveries and timeline exports into folders a sync
// service uploads, and Recipes' files (made to leave for another workspace)
// holding the project's footage or pictures.
func deliveriesLeft(ctx context.Context, a *app.App, projectID string, add func(place, to string, kinds []string, at time.Time, what string)) {
	var evs []models.AuditEvent
	if err := a.DB.Find(ctx, store.CAudit, a.WorkspaceID(), bson.M{"projectId": projectID, "action": bson.M{"$in": bson.A{"deliver.export", "export.timeline", "recipe.export"}}}, &evs,
		options.Find().SetSort(bson.D{{Key: "at", Value: 1}}).SetLimit(2000)); err != nil {
		return
	}
	for _, e := range evs {
		d := e.Details
		switch e.Action {
		case "recipe.export":
			n, m := number(d["footage"]), number(d["pictures"])
			var kinds []string
			if n > 0 {
				kinds = append(kinds, leave.FootageKinds...)
			}
			if m > 0 {
				kinds = append(kinds, models.LeavePictures)
			}
			add(models.PlaceDelivery, "a file Recipes made for another workspace", append(kinds, models.LeaveWords), e.At, "a file from Recipes")
		default:
			dir, _ := d["dir"].(string)
			if dir == "" {
				if f, _ := d["file"].(string); f != "" {
					dir = f
				}
			}
			if dir == "" || secretpath.Inspect(dir).Kind != secretpath.KindSync {
				continue
			}
			what := "a delivered video"
			if e.Action == "export.timeline" {
				what = "a timeline export"
			}
			add(models.PlaceDelivery, "a folder a sync service uploads to the cloud", []string{models.LeaveFootage, models.LeaveVoices, models.LeaveFaces, models.LeaveWords}, e.At, what)
		}
	}
}

// refusals are what What may leave kept on this computer for the project.
func refusals(ctx context.Context, a *app.App, projectID string) []ReceiptRefusal {
	var evs []models.AuditEvent
	out := []ReceiptRefusal{}
	if err := a.DB.Find(ctx, store.CAudit, a.WorkspaceID(), bson.M{"projectId": projectID, "action": leave.AuditRefused}, &evs,
		options.Find().SetSort(bson.D{{Key: "at", Value: 1}}).SetLimit(500)); err != nil {
		return out
	}
	for _, e := range evs {
		str := func(k string) string { s, _ := e.Details[k].(string); return s }
		out = append(out, ReceiptRefusal{At: e.At, Place: str("place"), Kind: str("kind"), To: str("to"), What: str("what"), By: e.Actor.UserName, Message: str("message")})
	}
	return out
}

// leftLines say it in plain words.
func leftLines(name string, l *ReceiptLeft) []string {
	var lines []string
	if l.Nothing {
		lines = append(lines, fmt.Sprintf("Nothing of %s left this computer.", q(name)))
	} else {
		var to []string
		for _, x := range l.Places {
			to = append(to, x.To)
		}
		if len(l.Places) == 0 {
			lines = append(lines, fmt.Sprintf("Nothing of %s was sent anywhere, but some of it is kept in cloud storage.", q(name)))
		} else {
			lines = append(lines, fmt.Sprintf("Things of %s left this computer for %s.", q(name), andList(dedupe(to))))
		}
		for _, x := range l.Places {
			kinds := "its conversations"
			if len(x.Kinds) > 0 {
				var ks []string
				for _, k := range x.Kinds {
					ks = append(ks, leave.KindWords(k))
				}
				kinds = andList(ks)
			}
			lines = append(lines, fmt.Sprintf("%s: %s, %s (%s).", upper(x.To), kinds, count(x.Count, "time", "times"), dayRange(x.First, x.Last)))
		}
		lines = append(lines, l.Stored...)
	}
	if n := len(l.Refused); n > 0 {
		lines = append(lines, fmt.Sprintf("What may leave kept it here %s.", count(n, "time", "times")))
	}
	lines = append(lines, "Its rules: "+strings.TrimSuffix(lowerAll(l.Rules), ".")+".")
	return lines
}

func dedupe(xs []string) []string {
	var out []string
	for _, x := range xs {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func dayRange(a, b time.Time) string {
	if a.IsZero() {
		return "date unknown"
	}
	da, db := a.Local().Format("2 Jan 2006"), b.Local().Format("2 Jan 2006")
	if da == db || b.IsZero() {
		return da
	}
	return da + " to " + db
}

func upper(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func lowerAll(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if len(r) > 1 && strings.ToUpper(string(r[1])) == string(r[1]) && strings.ToLower(string(r[1])) != string(r[1]) {
		return s
	}
	return strings.ToLower(string(r[0])) + string(r[1:])
}

// number reads a count kept in a record (any integer or float type).
func number(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	}
	return 0
}

// things are a project's clips (those in the Trash too), pictures and
// versions, by ID.
type things struct {
	assets, resources, revisions []string
}

func projectThings(ctx context.Context, a *app.App, projectID string) things {
	ids := func(coll string, filter bson.M) []string {
		var rows []struct {
			ID string `bson:"_id"`
		}
		_ = a.DB.Find(ctx, coll, a.WorkspaceID(), filter, &rows, options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(20000))
		out := []string{}
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	return things{
		assets:    ids(store.CAssets, bson.M{"$or": bson.A{bson.M{"projectIds": projectID}, bson.M{"trash.projectIds": projectID}}}),
		resources: ids(store.CResources, bson.M{"projectId": projectID}),
		revisions: ids(store.CRevisions, bson.M{"projectId": projectID}),
	}
}

// otherAIWork lists AI requests started from another project, or from none,
// that sent this project's clips or pictures.
func otherAIWork(ctx context.Context, a *app.App, projectID string, th things, add func(place, to string, kinds []string, at time.Time, what string)) {
	if len(th.assets) == 0 && len(th.resources) == 0 {
		return
	}
	var docs []bson.M
	filter := bson.M{"projectId": bson.M{"$ne": projectID}, "$or": bson.A{bson.M{"sourceAssets": bson.M{"$in": th.assets}}, bson.M{"references.assetId": bson.M{"$in": th.assets}},
		bson.M{"references.resourceId": bson.M{"$in": th.resources}}}}
	if err := a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), filter, &docs, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(5000)); err != nil {
		return
	}
	for _, d := range docs {
		if path(d, "ticket") == nil && len(parr(d, "sends")) == 0 && path(d, "outcome.outputs") == nil {
			continue // never reached the service
		}
		if ranHere(pstr(d, "provider", "provider.name")) {
			continue // done on this computer (D278, D281)
		}
		var files [][]string
		for _, ref := range parr(d, "references") {
			files = append(files, leave.FileKinds(pstr(ref, "role"), pstr(ref, "mime"), pstr(ref, "assetId") != ""))
		}
		files = append(files, leave.PresenterKinds(pstr(d, "kind"), ownPresenter(d)))
		kind := firstNonEmpty(pstr(d, "kind"), "video")
		service := firstNonEmpty(pstr(d, "providerTitle", "provider.title"), providerTitle(pstr(d, "provider", "provider.name")))
		add(models.PlaceAI, service, leave.Kinds(pstr(d, "prompt") != "", files...), ptime(d, "createdAt", "at"), firstNonEmpty(kindWords[kind], kind)+" (asked for elsewhere)")
	}
}

// voiceCopiesLeft lists copies of voices made with ElevenLabs from the
// project's clips (voiceover.clone): their samples went there.
func voiceCopiesLeft(ctx context.Context, a *app.App, projectID string, th things, add func(place, to string, kinds []string, at time.Time, what string)) {
	var evs []models.AuditEvent
	filter := bson.M{"action": "voiceover.clone", "$or": bson.A{bson.M{"projectId": projectID}, bson.M{"details.samples": bson.M{"$in": th.assets}},
		bson.M{"details.consentRecording": bson.M{"$in": th.assets}}}}
	if err := a.DB.Find(ctx, store.CAudit, a.WorkspaceID(), filter, &evs, options.Find().SetSort(bson.D{{Key: "at", Value: 1}}).SetLimit(500)); err != nil {
		return
	}
	for _, e := range evs {
		add(models.PlaceAI, providers.ElevenLabsTitle, []string{models.LeaveVoices}, e.At, "voice samples for a copy of a voice")
	}
}

// remotionLeft lists Remotion versions sent back to their repositories and
// refreshes whose steps ran with the network on.
func remotionLeft(ctx context.Context, a *app.App, projectID string, add func(place, to string, kinds []string, at time.Time, what string)) {
	var pushes []models.RemotionPush
	_ = a.DB.Find(ctx, store.CRemotionPushes, a.WorkspaceID(), bson.M{"projectId": projectID, "commit": bson.M{"$exists": true, "$ne": ""}}, &pushes, options.Find().SetLimit(500))
	for _, p := range pushes {
		if p.Target.Kind == "folder" {
			continue
		}
		add(models.PlaceDelivery, p.Target.Where, leave.RemotionKinds, p.CreatedAt, fmt.Sprintf("version %d of a Remotion project sent back", p.VersionNumber))
	}
	var refreshes []models.RemotionRefresh
	_ = a.DB.Find(ctx, store.CRemotionRefreshes, a.WorkspaceID(), bson.M{"projectId": projectID, "ran": true}, &refreshes, options.Find().SetLimit(500))
	for _, r := range refreshes {
		add(models.PlaceAbilities, "the places a Remotion refresh reached", leave.RemotionKinds, r.CreatedAt, fmt.Sprintf("refresh %d", r.Number))
	}
}

// reviewLinksLeft lists the review links that showed the project's things to
// people outside the workspace (D314): what each page showed, and until when
// (its own project's links, and links to versions that play its clips).
func reviewLinksLeft(ctx context.Context, a *app.App, projectID string, add func(place, to string, kinds []string, at time.Time, what string)) {
	var ls []models.ReviewLink
	if err := a.DB.Find(ctx, store.CReviewLinks, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"projectId": projectID}, bson.M{"projects": projectID}}}, &ls,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(500)); err != nil {
		return
	}
	for _, l := range ls {
		to := "people with a review link"
		if l.For != "" {
			to = q(l.For) + " (a review link)"
		}
		until := l.ExpiresAt
		if l.EndedAt != nil && l.EndedAt.Before(until) {
			until = *l.EndedAt
		}
		what := fmt.Sprintf("version %d: %s (until %s)", l.RevisionNumber, andList(l.Exposes), until.Local().Format("2 Jan 2006"))
		add(models.PlaceReviewers, to, l.Kinds, l.CreatedAt, what)
	}
}
