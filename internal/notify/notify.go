// Package notify writes persistent, per-person notifications about outcomes:
// a file is ready, a render finished or failed, a QA report is done, the
// dashboard assistant proposed changes, a duplicate was skipped, heavy jobs
// paused for battery, an upload was interrupted, speaker detection finished. The recipient is the person who
// initiated the work, so the notification shows on every machine they use.
//
// Every notification has a group key; writing the same outcome twice is a no-op
// (or, for outcomes that can change, an in-place update), which keeps the
// daemon, headless workers and inline CLI workers from double-notifying.
package notify

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Mode says what happens when a notification with the same group key exists.
type Mode int

const (
	// Once keeps the first notification for a group key.
	Once Mode = iota
	// Refresh updates it in place (and marks it unread) when its content changed.
	Refresh
)

// Upsert writes n for its (user, group key). It reports whether anything was
// written. Notifications without a recipient are dropped.
func Upsert(ctx context.Context, a *app.App, n models.Notification, mode Mode) (bool, error) {
	if n.UserID == "" || n.GroupKey == "" {
		return false, nil
	}
	now := time.Now().UTC()
	n.WorkspaceID = a.WorkspaceID()
	if n.Machine == "" {
		n.Machine = a.Cfg.Installation.Name
	}
	if n.Tone == "" {
		n.Tone = "info"
	}
	coll := a.DB.C(store.CNotifications)
	key := bson.M{"workspaceId": n.WorkspaceID, "userId": n.UserID, "groupKey": n.GroupKey}
	var existing models.Notification
	err := coll.FindOne(ctx, key).Decode(&existing)
	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		n.ID, n.CreatedAt, n.UpdatedAt, n.Read, n.ReadAt = ids.New(models.NotificationPrefix), now, now, false, nil
		if err := a.DB.Insert(ctx, store.CNotifications, n); err != nil {
			if store.IsDuplicateKey(err) { // another process wrote it first
				return false, nil
			}
			return false, err
		}
		return true, nil
	case err != nil:
		return false, err
	}
	if mode == Once || (existing.Title == n.Title && existing.Body == n.Body && existing.Tone == n.Tone) {
		return false, nil
	}
	set := bson.M{"title": n.Title, "body": n.Body, "tone": n.Tone, "link": n.Link, "details": n.Details, "machine": n.Machine,
		"read": false, "updatedAt": now}
	_, err = coll.UpdateOne(ctx, bson.M{"_id": existing.ID, "workspaceId": n.WorkspaceID}, bson.M{"$set": set, "$unset": bson.M{"readAt": ""}})
	return err == nil, err
}

func warn(_ *app.App, what string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: notification (%s) not written: %v\n", what, err)
	}
}

// AssetLink is the dashboard deep link for an asset.
func AssetLink(as *models.Asset) string {
	if len(as.ProjectIDs) > 0 {
		return links.Asset(as.ProjectIDs[0], as.ID)
	}
	return links.Asset("", as.ID)
}

// DerivativeTypes are the jobs ingest schedules for a new asset ("file ready"
// waits for all of them).
var DerivativeTypes = []string{"thumbnail", "proxy", "contact_sheet", "waveform", "mezzanine", "evidence", "transcribe"}

var stepLabel = map[string]string{"thumbnail": "thumbnail", "proxy": "preview copy", "contact_sheet": "frame grid", "waveform": "waveform",
	"mezzanine": "steady copy", "evidence": "scene analysis", "transcribe": "transcript"}

// JobAdvice says what to do about a failed job in plain words; the ops package
// sets it, so a notification says what job details say.
var JobAdvice func(j *models.Job) string

// AssetSettled writes one "file ready" notification once every derivative job
// of the asset is terminal, listing what is ready and what failed. Called after
// any job with an asset finishes; it does nothing while jobs are still open.
func AssetSettled(ctx context.Context, a *app.App, assetID string) {
	if assetID == "" {
		return
	}
	var js []models.Job
	if err := a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"assetId": assetID, "type": bson.M{"$in": DerivativeTypes}}, &js); err != nil || len(js) == 0 {
		return
	}
	// Latest job per type (a re-run supersedes an earlier attempt).
	latest := map[string]models.Job{}
	for _, j := range js {
		if cur, ok := latest[j.Type]; !ok || j.CreatedAt.After(cur.CreatedAt) {
			latest[j.Type] = j
		}
	}
	var ready, failed, canceled []string
	users := map[string]bool{}
	var failMsg string
	for _, typ := range DerivativeTypes {
		j, ok := latest[typ]
		if !ok {
			continue
		}
		switch j.State {
		case models.JobQueued, models.JobRunning:
			return // not settled yet
		case models.JobSucceeded:
			ready = append(ready, stepLabel[typ])
		case models.JobFailed:
			failed = append(failed, stepLabel[typ])
			if failMsg == "" {
				failMsg = firstLine(j.Error)
			}
		default:
			canceled = append(canceled, stepLabel[typ])
		}
		if j.CreatedBy.UserID != "" {
			users[j.CreatedBy.UserID] = true
		}
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return
	}
	if as.MadeNarration() || as.HasTag(models.TagEnhanced) {
		return // its narration or sound job tells the person (NarrationOutcome)
	}
	if as.Source.IsGenerated() {
		return // its generation tells the person ("A 6-second shot is ready")
	}
	if len(users) == 0 && as.CreatedBy.UserID != "" {
		users[as.CreatedBy.UserID] = true
	}
	title := as.DisplayTitle()
	n := models.Notification{Kind: models.NotifyFileReady, GroupKey: "file_ready:" + as.ID, AssetID: as.ID, Link: AssetLink(&as),
		Details: map[string]any{"ready": ready, "failed": failed, "canceled": canceled, "name": as.Name}}
	if len(as.ProjectIDs) > 0 {
		n.ProjectID = as.ProjectIDs[0]
	}
	switch {
	case len(failed) == 0:
		n.Tone, n.Title = "success", title+" is ready"
		n.Body = readySentenceOf(ready, as.Kind == "audio")
	default:
		n.Tone, n.Title = "error", fmt.Sprintf("%s is ready, but the %s failed", title, list(failed))
		n.Body = strings.TrimSpace(readySentenceOf(ready, as.Kind == "audio") + " You can retry the " + list(failed) + " from the activity bar.")
		if failMsg != "" {
			n.Details["error"] = failMsg // technical detail stays out of the sentence
		}
	}
	if len(canceled) > 0 {
		n.Body = strings.TrimSpace(n.Body + " The " + list(canceled) + " was stopped.")
	}
	for u := range users {
		n.UserID = u
		_, err := Upsert(ctx, a, n, Refresh)
		warn(a, "file ready", err)
	}
}

// NarrationOutcome notifies the person who asked for narration work (scratch
// voice, AI voice, cleanup, placing a recording, a voice copy) that it is ready
// to review, or could not be done. Canceled work is not announced.
func NarrationOutcome(ctx context.Context, a *app.App, j *models.Job) {
	if j == nil || j.Type != "voiceover" {
		return
	}
	action, _ := j.Payload["action"].(string)
	revID, _ := j.Payload["revisionId"].(string)
	lines := intOf(j.Result["lines"])
	n := models.Notification{UserID: j.CreatedBy.UserID, GroupKey: "narration:" + j.ID, JobID: j.ID, ProjectID: j.ProjectID, Link: links.Job(j.ID),
		Details: map[string]any{"action": action}}
	switch {
	case revID != "":
		n.Link = links.Revision(j.ProjectID, revID)
		n.Details["revisionId"] = revID
	case j.ProjectID != "":
		n.Link = links.Project(j.ProjectID)
	}
	switch j.State {
	case models.JobSucceeded:
		n.Kind, n.Tone = models.NotifyNarrationReady, "success"
		switch action {
		case "scratch":
			n.Title = "Scratch narration ready for " + lineCount(lines)
		case "tts":
			n.Title = "AI voice ready for " + lineCount(lines)
		case "cleanup":
			n.Title = "Narration cleaned up: " + lineCount(lines)
		case "align":
			n.Title = "Your recording is placed: " + lineCount(lines)
		case "place":
			n.Title = "Your take is placed: " + lineCount(lines)
		case "clone":
			n.Title = "Your voice copy is ready"
		case "enhance":
			n.Title = "Clearer voices ready"
		case "sound-setup":
			n.Title = "Clearer voices are set up on this computer"
		default:
			n.Title = "Narration ready"
		}
		n.Body = "Listen in the preview, then apply the change to add it to the cut."
		if rid, _ := j.Result["appliedRevisionId"].(string); rid != "" {
			// Put in the cut already (apply): say where, and link to that version.
			n.Body = "It is in the cut now."
			if num := intOf(j.Result["appliedNumber"]); num > 0 {
				n.Body = fmt.Sprintf("It is in the cut now as version %d.", num)
			}
			n.Link = links.Revision(j.ProjectID, rid)
			n.Details["revisionId"] = rid
		}
		if action == "clone" {
			n.Body = "Choose it as the narration voice in Settings, Integrations."
		}
	case models.JobFailed:
		n.Kind, n.Tone = models.NotifyNarrationFailed, "error"
		switch action {
		case "scratch":
			n.Title = "Could not make the scratch narration"
		case "tts":
			n.Title = "Could not make the AI voice"
		case "cleanup":
			n.Title = "Could not clean up the narration"
		case "align":
			n.Title = "Could not place your recording"
		case "place":
			n.Title = "Could not place your take"
		case "clone":
			n.Title = "Could not make your voice copy"
		case "enhance":
			n.Title = "Could not make the voices clearer"
		case "sound-setup":
			n.Title = "Could not set up clearer voices"
		default:
			n.Title = "Narration could not be made"
		}
		// What to do next, in plain words; the error itself stays in Details.
		n.Body = "Open it to see what went wrong, then try again."
		if JobAdvice != nil {
			if adv := JobAdvice(j); adv != "" {
				n.Body = adv
			}
		}
		n.Details["error"] = firstLine(j.Error)
		if code, _ := j.Payload["errorCode"].(string); code != "" {
			n.Details["code"] = code // technical detail stays out of the sentence
		}
	default:
		return
	}
	_, err := Upsert(ctx, a, n, Refresh)
	warn(a, "narration", err)
}

// ImportFailed tells the person who pasted a link that the import could not be
// done, with the reason and the next step in plain words (the advice the
// import recorded); the technical error stays in Details. A finished import
// needs no notification of its own: the clip's "is ready" covers it.
func ImportFailed(ctx context.Context, a *app.App, j *models.Job) {
	if j == nil || j.Type != "import" || j.State != models.JobFailed {
		return
	}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	platform, title := str(j.Payload, "platform"), str(j.Payload, "title")
	site := map[string]string{"youtube": "YouTube", "tiktok": "TikTok", "x": "X"}[platform]
	if site == "" {
		site = "the web"
	}
	what := "the video from " + site
	if title != "" {
		what = "“" + title + "” from " + site
	}
	n := models.Notification{Kind: models.NotifyImportFailed, UserID: j.CreatedBy.UserID, GroupKey: "import:" + j.ID, JobID: j.ID, ProjectID: j.ProjectID, Tone: "error",
		Title: "Could not import " + what, Link: "/?activity=open", Details: map[string]any{"url": str(j.Payload, "url"), "error": firstLine(j.Error)}}
	if j.ProjectID != "" {
		n.Link = "/p/" + j.ProjectID + "?tab=sources&activity=open"
	}
	// Without the import's advice, say what to do; the error stays in Details.
	body := "Something went wrong while importing it. Choose Retry in the activity bar to try again."
	var p struct {
		Advice *struct {
			Code    string `bson:"code"`
			Message string `bson:"message"`
			Next    string `bson:"next"`
			SignIn  bool   `bson:"signIn"`
		} `bson:"advice"`
	}
	if b, err := bson.Marshal(j.Payload); err == nil && bson.Unmarshal(b, &p) == nil && p.Advice != nil {
		body = strings.TrimSpace(p.Advice.Message + " " + p.Advice.Next)
		n.Details["code"] = p.Advice.Code
		if p.Advice.SignIn {
			n.Details["signIn"] = true
		}
	}
	n.Body = body
	_, err := Upsert(ctx, a, n, Refresh)
	warn(a, "import", err)
}

func lineCount(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

func intOf(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// RenderOutcome notifies the person who queued a render that it finished or failed.
func RenderOutcome(ctx context.Context, a *app.App, renderID string) {
	var rd models.Render
	if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), renderID, &rd); err != nil {
		return
	}
	var p models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), rd.ProjectID, &p)
	label := models.RenderLabel(rd.Quality, rd.RevisionNumber)
	n := models.Notification{UserID: rd.CreatedBy.UserID, GroupKey: "render:" + rd.ID, RenderID: rd.ID, ProjectID: rd.ProjectID, Link: links.Render(rd.ProjectID, rd.ID),
		Details: map[string]any{"preset": rd.Preset, "quality": rd.Quality, "revisionNumber": rd.RevisionNumber, "project": p.Name}}
	switch rd.State {
	case models.RenderAvailable:
		n.Kind, n.Tone, n.Title = models.NotifyRenderFinished, "success", label+" finished"
		n.Body = strings.TrimSpace(p.Name + ", " + editspec.PresetLabel(rd.Preset))
		if rd.RenderedByName != "" {
			n.Machine = rd.RenderedByName // shown with the time; not repeated in the sentence
		}
	case models.RenderFailed:
		n.Kind, n.Tone, n.Title = models.NotifyRenderFailed, "error", label+" failed"
		n.Body = strings.TrimSpace(p.Name + ". Open it to see what went wrong and try again.")
		n.Details["error"] = firstLine(rd.Error) // technical detail stays out of the sentence
	default:
		return
	}
	_, err := Upsert(ctx, a, n, Refresh)
	warn(a, "render", err)
}

// QADone notifies the person who asked for QA that its report is ready.
func QADone(ctx context.Context, a *app.App, j *models.Job, reportID string) {
	var rep models.QAReport
	if err := a.DB.GetByID(ctx, store.CQAReports, a.WorkspaceID(), reportID, &rep); err != nil {
		return
	}
	n := models.Notification{Kind: models.NotifyQADone, UserID: j.CreatedBy.UserID, GroupKey: "qa:" + rep.ID, RenderID: rep.RenderID, ProjectID: rep.ProjectID,
		JobID: j.ID, Link: links.QA(rep.ProjectID, rep.RenderID, rep.ID),
		Details: map[string]any{"reportId": rep.ID, "verdict": rep.Verdict, "errors": rep.Counts.Errors, "warnings": rep.Counts.Warnings, "info": rep.Counts.Info}}
	who := "Quality check of the " + strings.ToLower(models.RenderLabel(rep.Quality, rep.RevisionNumber))
	switch {
	case rep.Counts.Errors > 0:
		n.Tone, n.Title = "error", fmt.Sprintf("%s: %s", who, counts(rep.Counts.Errors, rep.Counts.Warnings))
	case rep.Counts.Warnings > 0:
		n.Tone, n.Title = "warning", fmt.Sprintf("%s: %s", who, counts(0, rep.Counts.Warnings))
	default:
		n.Tone, n.Title = "success", who+" passed"
	}
	n.Body = "It lists what to look at, with the moment in the video for each."
	// The speech check speaks for itself: "Around 0:27 a word repeats in the rendered video."
	for _, sev := range []string{models.QAError, models.QAWarning} {
		found := false
		for _, f := range rep.Findings {
			if f.Severity == sev && strings.HasPrefix(f.Kind, "speech_") {
				n.Body = strings.TrimSuffix(f.Message, ".") + ". " + n.Body
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if rep.Counts.Errors+rep.Counts.Warnings == 0 {
		n.Body = "Nothing looks wrong in the finished video."
		if m := rep.Measurements; m.SpeechWordsExpected > 0 && m.SpeechWordsMatched >= m.SpeechWordsExpected {
			n.Body = "Nothing looks wrong in the finished video, and every expected word was heard."
		}
	}
	_, err := Upsert(ctx, a, n, Once)
	warn(a, "qa", err)
}

// QAProblems is QADone for a review copy MetaRush made by itself to follow the
// latest version (D187): the bell rings only when the check found something
// to look at; a clean check shows on the copy itself.
func QAProblems(ctx context.Context, a *app.App, j *models.Job, reportID string) {
	var rep models.QAReport
	if err := a.DB.GetByID(ctx, store.CQAReports, a.WorkspaceID(), reportID, &rep); err != nil {
		return
	}
	if rep.Counts.Errors+rep.Counts.Warnings == 0 {
		return
	}
	QADone(ctx, a, j, reportID)
}

// AssistantTurn notifies the person who wrote a message in a dashboard Claude
// Code conversation that its reply left something for them: plans to review
// (proposals) or changes Claude asked them to make (action cards). One
// notification per message; the link opens that conversation.
func AssistantTurn(ctx context.Context, a *app.App, conversationID string, turn int, projectID, projectName string, proposals, asks int, userID string) {
	if proposals <= 0 && asks <= 0 {
		return
	}
	title := fmt.Sprintf("Claude Code proposed %s", plural(proposals, "change"))
	if asks > 0 {
		title = "Claude Code needs your OK"
	}
	n := models.Notification{Kind: models.NotifyAssistantProposal, UserID: userID, GroupKey: fmt.Sprintf("assistant:%s:t%d", conversationID, turn),
		ProjectID: projectID, Tone: "info", Title: title, Link: "/?ask=" + conversationID,
		Details: map[string]any{"conversationId": conversationID, "turn": turn, "proposals": proposals, "asks": asks}}
	if projectID != "" {
		n.Link = "/p/" + projectID + "?ask=" + conversationID
		n.Body = "For " + projectName + ". Review them in the Claude Code panel."
	} else {
		n.Body = "Review them in the Claude Code panel."
	}
	_, err := Upsert(ctx, a, n, Once)
	warn(a, "assistant", err)
}

// DuplicateSkipped notifies that name was identical to an existing asset, so
// nothing was stored twice.
func DuplicateSkipped(ctx context.Context, a *app.App, as *models.Asset, name, via string) {
	where := "the inbox"
	if len(as.ProjectIDs) > 0 {
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), as.ProjectIDs[0], &p) == nil {
			where = p.Name
		}
	}
	n := models.Notification{Kind: models.NotifyDuplicateSkipped, UserID: a.Actor.UserID, GroupKey: "duplicate:" + as.ID + ":" + name, AssetID: as.ID, Tone: "info",
		Title: name + " was already in MetaRush", Body: fmt.Sprintf("It is identical to “%s” (in %s), so nothing was stored twice.", as.DisplayTitle(), where),
		Link: AssetLink(as), Details: map[string]any{"name": name, "original": as.Name, "title": as.DisplayTitle(), "via": via}}
	_, err := Upsert(ctx, a, n, Once)
	warn(a, "duplicate", err)
}

// BatteryPaused notifies the people whose heavy jobs a worker started deferring
// for battery: once per deferral episode.
func BatteryPaused(ctx context.Context, a *app.App, machine string, percent, threshold int, since time.Time, waiting []models.Job) {
	byUser := map[string][]models.Job{}
	for _, j := range waiting {
		if j.CreatedBy.UserID != "" {
			byUser[j.CreatedBy.UserID] = append(byUser[j.CreatedBy.UserID], j)
		}
	}
	for u, js := range byUser {
		kinds := map[string]int{}
		var jobIDs []string
		for _, j := range js {
			kinds[j.Type]++
			jobIDs = append(jobIDs, j.ID)
		}
		n := models.Notification{Kind: models.NotifyBatteryPaused, UserID: u, Tone: "warning", Machine: machine,
			GroupKey: fmt.Sprintf("battery:%s:%d", a.Cfg.Installation.ID, since.Unix()), Link: "/?activity=open",
			Title:   fmt.Sprintf("%s paused on %s", capFirst(jobKinds(kinds)), machine),
			Body:    fmt.Sprintf("Battery at %d%% (below %d%%). They resume when you plug in, or choose Process now in the activity bar.", percent, threshold),
			Details: map[string]any{"jobIds": jobIDs, "batteryPercent": percent, "threshold": threshold}}
		_, err := Upsert(ctx, a, n, Once)
		warn(a, "battery", err)
	}
}

// SpeakersDone notifies the person who asked MetaRush to listen for the voices
// again (speaker detection) how it went, with a link to the clip's Speakers tab.
func SpeakersDone(ctx context.Context, a *app.App, j *models.Job, state string) {
	if j.AssetID == "" || (state != models.JobSucceeded && state != models.JobFailed) {
		return
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), j.AssetID, &as); err != nil {
		return
	}
	projectID := ""
	if len(as.ProjectIDs) > 0 {
		projectID = as.ProjectIDs[0]
	}
	title := as.DisplayTitle()
	n := models.Notification{Kind: models.NotifySpeakers, UserID: j.CreatedBy.UserID, GroupKey: "speakers:" + j.ID, AssetID: as.ID, ProjectID: projectID, JobID: j.ID,
		Link: links.AssetSpeakers(projectID, as.ID), Details: map[string]any{"name": as.Name}}
	if state == models.JobFailed {
		n.Tone, n.Title = "error", fmt.Sprintf("Could not tell the voices apart in “%s”", title)
		n.Body = "Open the clip to try again."
		n.Details["error"] = firstLine(j.Error) // technical detail stays out of the sentence
	} else {
		count, named := 0, 0
		if as.Speakers != nil {
			count, named = as.Speakers.Count, as.Speakers.Named
		}
		n.Tone = "success"
		switch count {
		case 0:
			n.Title = fmt.Sprintf("No voices found in “%s”", title)
		case 1:
			n.Title = fmt.Sprintf("Found 1 voice in “%s”", title)
		default:
			n.Title = fmt.Sprintf("Found %d voices in “%s”", count, title)
		}
		switch {
		case count == 0:
			n.Body = "Nobody seems to speak in it."
		case named >= count:
			n.Body = "Every voice has a name."
		case named == 0:
			n.Body = "Listen to each voice and give it a name."
		case count-named == 1:
			n.Body = "One voice has no name yet. Listen and name it."
		default:
			n.Body = fmt.Sprintf("%d voices have no name yet. Listen and name them.", count-named)
		}
		n.Details["voices"], n.Details["named"] = count, named
	}
	_, err := Upsert(ctx, a, n, Once)
	warn(a, "speakers", err)
}

// ClipLooked tells the person who asked how looking at a clip went: what's on
// screen (the on-screen index) or written scene descriptions. Automatic runs
// (new clips, catching up) do not notify: the activity bar shows them.
func ClipLooked(ctx context.Context, a *app.App, j *models.Job, state string) {
	if j.AssetID == "" || (state != models.JobSucceeded && state != models.JobFailed) {
		return
	}
	if requested, _ := j.Payload["requested"].(bool); !requested || j.CreatedBy.UserID == "" {
		return
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), j.AssetID, &as); err != nil {
		return
	}
	projectID := ""
	if len(as.ProjectIDs) > 0 {
		projectID = as.ProjectIDs[0]
	}
	title := as.DisplayTitle()
	kind, what := models.NotifyOnScreen, "what's on screen"
	if j.Type == "describe" {
		kind, what = models.NotifyDescribed, "the scenes"
	}
	n := models.Notification{Kind: kind, UserID: j.CreatedBy.UserID, GroupKey: kind + ":" + j.ID, AssetID: as.ID, ProjectID: projectID, JobID: j.ID,
		Link: links.AssetOnScreen(projectID, as.ID), Details: map[string]any{"name": as.Name}}
	if state == models.JobFailed {
		n.Tone, n.Title = "error", fmt.Sprintf("Could not look at %s in “%s”", what, title)
		n.Body = "Open the clip's details to see why, then try again."
		n.Details["error"] = firstLine(j.Error)
	} else {
		n.Tone = "success"
		if kind == models.NotifyDescribed {
			n.Title = fmt.Sprintf("Described the scenes in “%s”", title)
			n.Body = "Claude Code wrote a sentence or two per scene; they are searchable now."
			if c := intOf(j.Result["scenes"]); c > 0 {
				n.Body = fmt.Sprintf("Claude Code described %s; they are searchable now.", plural(c, "scene"))
			}
		} else {
			n.Title = fmt.Sprintf("Looked at what's on screen in “%s”", title)
			if s, _ := j.Result["summary"].(string); s != "" {
				n.Body = capFirst(s) + "."
			}
		}
	}
	_, err := Upsert(ctx, a, n, Once)
	warn(a, kind, err)
}

// Recognized tells a person that the voice of someone they named was
// recognized automatically in other videos, so they can check it: one
// notification per person ("Recognized Maya's voice in 2 more videos") with
// a link to check them — the People page, or the clip's Speakers tab when it is
// one clip. It covers the videos where the voice is still labeled
// automatically (not yet confirmed or rejected): a new video marks it unread
// again; checking them only updates it quietly.
func Recognized(ctx context.Context, a *app.App, userID, personID string) {
	if userID == "" || personID == "" {
		return
	}
	var p models.Person
	if err := a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), personID, &p); err != nil || p.MergedInto != "" {
		return
	}
	var vs []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"personId": personID, "source": models.SpeakerByAuto, "mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &vs); err != nil {
		return
	}
	seen := map[string]bool{}
	var assetIDs []string
	scores := map[string]float64{}
	for _, v := range vs {
		if !seen[v.AssetID] {
			seen[v.AssetID] = true
			assetIDs = append(assetIDs, v.AssetID)
		}
		scores[v.AssetID] = math.Max(scores[v.AssetID], v.Score)
	}
	sort.Strings(assetIDs)
	coll := a.DB.C(store.CNotifications)
	key := bson.M{"workspaceId": a.WorkspaceID(), "userId": userID, "groupKey": "recognized:" + personID}
	var existing models.Notification
	found := coll.FindOne(ctx, key).Decode(&existing) == nil
	if len(assetIDs) == 0 {
		if found && !existing.Read {
			_, _ = coll.UpdateOne(ctx, bson.M{"_id": existing.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"read": true, "readAt": time.Now().UTC()}})
		}
		return
	}
	var assets []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": assetIDs}}, &assets)
	byID := map[string]models.Asset{}
	for _, as := range assets {
		byID[as.ID] = as
	}
	var titles []string
	var pct []int
	for _, id := range assetIDs {
		as := byID[id]
		titles = append(titles, as.DisplayTitle())
		pct = append(pct, int(scores[id]*100+0.5))
	}
	n := models.Notification{Kind: models.NotifyRecognized, UserID: userID, GroupKey: "recognized:" + personID, Tone: "info", Link: links.Person(personID),
		Details: map[string]any{"personId": personID, "name": p.Name, "assets": assetIDs, "titles": titles, "scores": pct}}
	if len(assetIDs) == 1 {
		as := byID[assetIDs[0]]
		projectID := ""
		if len(as.ProjectIDs) > 0 {
			projectID = as.ProjectIDs[0]
		}
		n.AssetID, n.ProjectID, n.Link = as.ID, projectID, links.AssetSpeakers(projectID, as.ID)
		n.Title = fmt.Sprintf("Recognized %s's voice in “%s”", p.Name, as.DisplayTitle())
		n.Body = fmt.Sprintf("It's probably %s (%d%% voice match). Check that it really is them, or choose “Not %s?”.", p.Name, pct[0], p.Name)
	} else {
		n.Title = fmt.Sprintf("Recognized %s's voice in %d more videos", p.Name, len(assetIDs))
		n.Body = fmt.Sprintf("Check that it really is %s in each of them, or choose “Not %s?” where it isn't.", p.Name, p.Name)
	}
	if !found {
		_, err := Upsert(ctx, a, n, Once)
		warn(a, "recognized", err)
		return
	}
	known := map[string]bool{}
	if prev, ok := existing.Details["assets"].(bson.A); ok {
		for _, x := range prev {
			if s, ok := x.(string); ok {
				known[s] = true
			}
		}
	}
	fresh := false
	for _, id := range assetIDs {
		if !known[id] {
			fresh = true
		}
	}
	set := bson.M{"title": n.Title, "body": n.Body, "link": n.Link, "details": n.Details, "assetId": n.AssetID, "projectId": n.ProjectID}
	upd := bson.M{"$set": set}
	if fresh {
		set["read"], set["updatedAt"], set["machine"] = false, time.Now().UTC(), a.Cfg.Installation.Name
		upd["$unset"] = bson.M{"readAt": ""}
	}
	_, err := coll.UpdateOne(ctx, bson.M{"_id": existing.ID, "workspaceId": a.WorkspaceID()}, upd)
	warn(a, "recognized", err)
}

// RecognizedFaces tells a person that the face of someone they named was
// recognized automatically in other clips (D114), so they can check it: one
// notification per person ("Recognized Maya's face in 2 more clips") with a
// link to the person's page, where each face has "Not Maya?". It covers the
// clips where the face is still named by looks only: a new clip marks it unread
// again; checking them only updates it quietly.
func RecognizedFaces(ctx context.Context, a *app.App, userID, personID string) {
	if userID == "" || personID == "" {
		return
	}
	var p models.Person
	if err := a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), personID, &p); err != nil || p.MergedInto != "" {
		return
	}
	var fs []models.FaceTrack
	if err := a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"recognized.personId": personID, "personId": bson.M{"$in": bson.A{nil, ""}}}, &fs,
		options.Find().SetProjection(bson.M{"assetId": 1, "recognized": 1})); err != nil {
		return
	}
	seen := map[string]bool{}
	var assetIDs []string
	scores := map[string]float64{}
	for _, f := range fs {
		if !seen[f.AssetID] {
			seen[f.AssetID] = true
			assetIDs = append(assetIDs, f.AssetID)
		}
		if f.Recognized != nil {
			scores[f.AssetID] = math.Max(scores[f.AssetID], f.Recognized.Score)
		}
	}
	sort.Strings(assetIDs)
	coll := a.DB.C(store.CNotifications)
	key := bson.M{"workspaceId": a.WorkspaceID(), "userId": userID, "groupKey": "recognized-face:" + personID}
	var existing models.Notification
	found := coll.FindOne(ctx, key).Decode(&existing) == nil
	if len(assetIDs) == 0 {
		if found && !existing.Read {
			_, _ = coll.UpdateOne(ctx, bson.M{"_id": existing.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"read": true, "readAt": time.Now().UTC()}})
		}
		return
	}
	var assets []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": assetIDs}}, &assets)
	byID := map[string]models.Asset{}
	for _, as := range assets {
		byID[as.ID] = as
	}
	var titles []string
	var pct []int
	for _, id := range assetIDs {
		as := byID[id]
		titles = append(titles, as.DisplayTitle())
		pct = append(pct, int(scores[id]*100+0.5))
	}
	n := models.Notification{Kind: models.NotifyRecognized, UserID: userID, GroupKey: "recognized-face:" + personID, Tone: "info", Link: links.Person(personID),
		Details: map[string]any{"personId": personID, "name": p.Name, "assets": assetIDs, "titles": titles, "scores": pct, "by": "looks"}}
	if len(assetIDs) == 1 {
		as := byID[assetIDs[0]]
		projectID := ""
		if len(as.ProjectIDs) > 0 {
			projectID = as.ProjectIDs[0]
		}
		n.AssetID, n.ProjectID = as.ID, projectID
		n.Title = fmt.Sprintf("Recognized %s's face in “%s”", p.Name, as.DisplayTitle())
		n.Body = fmt.Sprintf("It looks like %s (%d%% alike). Check that it really is them on %s's page, or choose “Not %s?”.", p.Name, pct[0], p.Name, p.Name)
	} else {
		n.Title = fmt.Sprintf("Recognized %s's face in %d more clips", p.Name, len(assetIDs))
		n.Body = fmt.Sprintf("Check that it really is %s in each of them on their page, or choose “Not %s?” where it isn't.", p.Name, p.Name)
	}
	if !found {
		_, err := Upsert(ctx, a, n, Once)
		warn(a, "recognized-face", err)
		return
	}
	known := map[string]bool{}
	if prev, ok := existing.Details["assets"].(bson.A); ok {
		for _, x := range prev {
			if s, ok := x.(string); ok {
				known[s] = true
			}
		}
	}
	fresh := false
	for _, id := range assetIDs {
		if !known[id] {
			fresh = true
		}
	}
	set := bson.M{"title": n.Title, "body": n.Body, "link": n.Link, "details": n.Details, "assetId": n.AssetID, "projectId": n.ProjectID}
	upd := bson.M{"$set": set}
	if fresh {
		set["read"], set["updatedAt"], set["machine"] = false, time.Now().UTC(), a.Cfg.Installation.Name
		upd["$unset"] = bson.M{"readAt": ""}
	}
	_, err := coll.UpdateOne(ctx, bson.M{"_id": existing.ID, "workspaceId": a.WorkspaceID()}, upd)
	warn(a, "recognized-face", err)
}

// UploadInterrupted notifies the person who started a browser upload (a's
// actor) that it stopped before finishing.
func UploadInterrupted(ctx context.Context, a *app.App, uploadID, name string, pct int, projectID string) {
	user := a.Actor.UserID
	if user == "" {
		user = a.Cfg.User.ID
	}
	n := models.Notification{Kind: models.NotifyUploadInterrupted, UserID: user, GroupKey: "upload_interrupted:" + uploadID, ProjectID: projectID, Tone: "warning",
		Title: fmt.Sprintf("Upload of %s stopped at %d%%", name, pct), Body: "Drop the same file again to resume where it stopped.",
		Link: "/?activity=open", Details: map[string]any{"uploadId": uploadID, "percent": pct, "name": name}}
	if projectID != "" {
		n.Link = "/p/" + projectID + "?tab=sources&activity=open"
	}
	_, err := Upsert(ctx, a, n, Once)
	warn(a, "upload", err)
}

func jobKinds(kinds map[string]int) string {
	var keys []string
	for k := range kinds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		name := stepLabel[k]
		switch k {
		case "transcribe":
			name = "transcription"
		case "render":
			name = "final render"
		case "diarize":
			name = "speaker detection"
		case "":
			name = "job"
		}
		if name == "" {
			name = k
		}
		parts = append(parts, plural(kinds[k], name))
	}
	return list(parts)
}

func counts(errs, warns int) string {
	var parts []string
	if errs > 0 {
		parts = append(parts, plural(errs, "problem"))
	}
	if warns > 0 {
		parts = append(parts, plural(warns, "thing")+" worth checking")
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// readySentence says what someone can now do with a clip, from the steps that
// finished. Plumbing (thumbnail, frame grid, waveform, smooth-playback copy) is
// left out: it matters only through what it enables.
func readySentence(ready []string) string { return readySentenceOf(ready, false) }

// readySentenceOf is readySentence for a sound file when audio is set: it is
// listened to and has no scenes (its analysis is only loudness).
func readySentenceOf(ready []string, audio bool) string {
	can := map[string]string{"preview copy": "watch it", "transcript": "read what was said", "scene analysis": "see its scenes"}
	if audio {
		can["preview copy"], can["scene analysis"] = "listen to it", ""
	}
	var parts []string
	for _, s := range []string{"preview copy", "transcript", "scene analysis"} {
		for _, r := range ready {
			if r == s && can[s] != "" {
				parts = append(parts, can[s])
			}
		}
	}
	if len(parts) == 0 {
		if len(ready) == 0 {
			return ""
		}
		return "It is stored and ready to use."
	}
	return "You can now " + list(parts) + "."
}

func list(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
