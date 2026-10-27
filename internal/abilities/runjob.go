package abilities

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/store"
)

// Running an ability (D234): the worker of the computer that asked takes the
// job, checks again that the version is approved and set up here, starts
// MetaRush's proxy for the run (the declared hosts, the keys, a password of
// its own), runs the program in the sandbox, records every request, and
// takes in only the files it declared, through the one classifier, with how
// they were made. Never retried: a run cut short says so.

// finalErr is an error retrying wouldn't fix.
type finalErr struct{ error }

func (e finalErr) Unwrap() error { return e.error }

// IsFinal reports an error the job shouldn't be retried for.
func IsFinal(err error) bool {
	var f finalErr
	return errors.As(err, &f)
}

func final(err error) error { return finalErr{err} }

// What a run that says it sends only words (or nothing) may send in all —
// request lines, headers and bodies — and how many requests it may make.
const (
	sendBudget    = 256 << 10
	wordsRequests = 200
)

// keyHeader is the header a key goes in ("" when it goes in the address).
func keyHeader(k models.AbilityKey) string {
	if k.Query != "" {
		return ""
	}
	if k.Header == "" {
		return "Authorization"
	}
	return k.Header
}

// RunJob runs one ability-run job.
func RunJob(ctx context.Context, a *app.App, j *models.Job, report func(pct float64, msg string)) (map[string]any, error) {
	id, _ := j.Payload["run"].(string)
	if id == "" {
		return nil, final(errors.New("this job doesn't say which run it is"))
	}
	r, err := getRun(ctx, a, id)
	if err != nil {
		return nil, final(err)
	}
	if r.State != models.AbilityRunQueued {
		// A run that started before and was cut short is over (never run twice).
		if r.State == models.AbilityRunRunning {
			finish(ctx, a, r, models.AbilityRunFailed, "", "It was cut short (MetaRush stopped while it ran); start it again if you still want it.", nil)
		}
		return nil, final(fmt.Errorf("run %d of “%s” is %s", r.Number, r.Title, runStateWords(r.State)))
	}
	started := now()
	_ = setRun(ctx, a, r.ID, map[string]any{"state": models.AbilityRunRunning, "startedAt": started})
	r.State, r.StartedAt = models.AbilityRunRunning, &started
	fail := func(msg string) (map[string]any, error) {
		finish(ctx, a, r, models.AbilityRunFailed, "", msg, nil)
		return nil, final(errors.New(msg))
	}
	v, err := getVersion(ctx, a, r.Hash)
	if err != nil {
		return fail("Its version isn't there any more.")
	}
	if v.State != models.AbilityApproved {
		return fail(fmt.Sprintf("Version %d of “%s” isn't approved any more (%s).", v.Number, v.Title, StateWords(v.State)))
	}
	f := v.Facts
	if f.Network == NetworkOpen && !v.OpenNetwork {
		return fail("It asks for the whole internet, which no owner agreed to.")
	}
	here, paths := hereFor(ctx, a, f, v.Name, true)
	if !here.Ready {
		return fail(strings.Join(here.Problems, " "))
	}
	report(0.05, "Getting it ready")
	arc, err := loadArchive(ctx, a, v)
	if err != nil {
		return fail(apperr.From(err).Message)
	}
	m, err := arc.Manifest()
	if err != nil {
		return fail("Its manifest can't be read.")
	}
	// Inputs again, as planned (the files by their SHA-256).
	values := map[string]any{}
	files := map[string]string{}
	for _, in := range r.Inputs {
		if in.Ref == "" {
			values[in.Name] = in.Value
			continue
		}
		fi, err := resolveFile(ctx, a, in.Kind, in.Ref, r.ProjectID, nil)
		if err != nil || fi.SHA256 != in.SHA256 {
			return fail(fmt.Sprintf("“%s” changed since the run was planned.", in.Title))
		}
		p, err := a.Cache.Ensure(ctx, a.Media, fi.Key, fi.SHA256, fi.Size, nil)
		if err != nil {
			return fail(fmt.Sprintf("MetaRush couldn't fetch “%s”.", in.Title))
		}
		files[in.Name] = p
	}
	res, msg, canceled := sandboxRun(ctx, a, r, v, arc, m, paths, values, files, report)
	if canceled {
		finish(ctx, a, r, models.AbilityRunCanceled, "", "It was stopped.", nil)
		return nil, final(ctx.Err())
	}
	if res != nil {
		defer res.Close()
	}
	if msg != "" {
		finish(ctx, a, r, models.AbilityRunFailed, "", msg, res)
		return nil, final(errors.New(msg))
	}
	report(0.8, "Taking in what it made")
	made := models.AbilityMadeWith{Ability: v.Name + "@" + Short(v.Hash), Name: v.Name, Hash: v.Hash, Title: v.Title, RunID: r.ID, Inputs: r.Inputs, CostUSD: r.EstimateUSD}
	for _, o := range res.Outputs {
		r.Outputs = append(r.Outputs, takeIn(ctx, a, r, o, made))
	}
	finish(ctx, a, r, models.AbilityRunDone, summaryOf(r), "", res)
	return map[string]any{"run": r.ID, "outputs": len(r.Outputs), "message": summaryOf(r)}, nil
}

// sandboxRun runs a version in its room with these inputs, through MetaRush's
// proxy for the run when it reaches the network (its keys put in there), and
// records the run's requests, exit and log on r. msg says why a run that
// couldn't start, ran too long or failed didn't work (the caller finishes the
// run with it); canceled is a run stopped from outside. The caller closes res.
// Every run of a version goes through it — its own and those of AI work
// (D281) — so the local service (D284) and the files set up here (D285) are
// checked for each.
func sandboxRun(ctx context.Context, a *app.App, r *models.AbilityRun, v *models.AbilityVersion, arc *Archive, m *Manifest, paths map[string]string,
	values map[string]any, files map[string]string, report func(pct float64, msg string)) (res *roomResult, msg string, canceled bool) {
	f := v.Facts
	if ls := localCheck(ctx, a, v); ls.Problem != "" {
		return nil, ls.Problem, false // checked again as the run starts (D284)
	}
	// What may leave (D290), asked again: the Content Settings may have
	// changed since the run was planned.
	send := runSend(ctx, a, f, r.Inputs, r.ProjectID, v.Title)
	if kept, err := keptKinds(ctx, a, f, send); err != nil {
		return nil, "MetaRush couldn't read the Content Settings of the projects it uses.", false
	} else if kept != nil {
		leave.Record(ctx, a, kept, send)
		return nil, kept.Message, false
	}
	var refuse func(string) string
	if len(send.Kinds) > 0 {
		refuse = hostRefuser(context.WithoutCancel(ctx), a, send)
	}
	// The run itself stops when its local service changes under it (D284).
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	var (
		brokenMu  sync.Mutex
		brokenOff string
	)
	// The network: MetaRush's proxy for this run, with the keys.
	spec := roomSpec{Archive: arc, Manifest: m, Programs: paths, Values: values, Files: files, Net: sandbox.NetOff, StandIns: map[string]string{},
		Timeout: time.Duration(f.Minutes) * time.Minute, MaxBytes: int64(f.MB) << 20, Isolation: sandbox.Here(a.Tools)}
	if len(f.Files) > 0 {
		// The files a person set up here (D285), checked again as they are copied.
		setup, _ := getSetup(ctx, a, v.Name)
		for _, nf := range f.Files {
			for _, fp := range setupFiles(setup) {
				if fp.Name == nf.Name {
					spec.Needed = append(spec.Needed, fp)
				}
			}
		}
	}
	spec.Self, _ = os.Executable()
	var proxy *sandbox.Proxy
	var err error
	switch f.Network {
	case NetworkOpen:
		// The whole internet, still through MetaRush: any public host, never
		// this computer or a local network, no keys (D235).
		proxy, err = sandbox.StartProxy(sandbox.ProxyConfig{AnyHost: true, Token: token(), MaxBytes: int64(f.MB) << 20, Dial: testDial, RootCAs: testRootCAs, Refuse: refuse})
		if err != nil {
			return nil, "MetaRush couldn't open the way out for this run.", false
		}
		defer proxy.Close()
		spec.Net, spec.Proxy = sandbox.NetOpen, proxy
	case NetworkMetaRush:
		var keys []sandbox.ProxyKey
		for _, k := range f.Keys {
			// Checked again at run time: an integration's key goes only to its
			// own hosts, and a private key never goes (D235, D282).
			allowed, integration, err := providers.AbilityKeyHosts(k.Name)
			if err != nil {
				return nil, apperr.From(err).Message, false
			}
			if integration {
				for _, h := range k.Hosts {
					if !contains(allowed, h) {
						return nil, fmt.Sprintf("MetaRush's %s key goes only to its own service, not to %s.", k.Name, h), false
					}
				}
			}
			val, _, err := providers.Key(a.Cfg, k.Name)
			if err != nil || val == "" {
				return nil, fmt.Sprintf("This computer has no %s key.", k.Name), false
			}
			a.Log.Secret(sandbox.KeyForms(val)...) // the log never shows it, in any written form
			si := standIn(k.Name)
			spec.StandIns[k.Env] = si
			keys = append(keys, sandbox.ProxyKey{Name: k.Name, Value: val, StandIn: si, Hosts: k.Hosts, Header: keyHeader(k), Query: k.Query})
			spec.Keys = append(spec.Keys, val)
		}
		cfg := sandbox.ProxyConfig{Hosts: append([]string{}, f.Hosts...), Keys: keys, Token: token(), MaxBytes: int64(f.MB) << 20, Refuse: refuse}
		if f.Local != nil {
			// The one service on this computer an owner agreed to (D284), checked
			// again on the run's requests to it (a few seconds' memory): another
			// program there breaks the run off.
			cfg.LocalPort = f.Local.Port
			checks := &localChecks{every: localRecheck, check: func() string { return localCheck(ctx, a, v).Problem }}
			checks.at, checks.why = time.Now(), "" // just checked above
			cfg.LocalCheck, cfg.LocalCheckNow = checks.Check, checks.Now
			cfg.LocalChanged = func(why string) {
				brokenMu.Lock()
				brokenOff = why
				brokenMu.Unlock()
				stopRun()
			}
		}
		if !m.SendsAway() {
			cfg.MaxSend, cfg.MaxRequests = sendBudget, wordsRequests
		}
		if testDial != nil {
			cfg.Dial, cfg.RootCAs = testDial, testRootCAs
		}
		proxy, err = sandbox.StartProxy(cfg)
		if err != nil {
			return nil, "MetaRush couldn't open the way out for this run.", false
		}
		defer proxy.Close()
		spec.Net, spec.Proxy = sandbox.NetProxy, proxy
	}
	report(0.15, "Running")
	res, err = runInRoom(runCtx, a, spec)
	brokenMu.Lock()
	broken := brokenOff
	brokenMu.Unlock()
	if broken != "" {
		if res != nil {
			res.Close()
		}
		return nil, "It was stopped: " + broken, false
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", true
		}
		return nil, apperr.From(err).Message, false
	}
	r.Exit, r.Seconds = &res.Exit, res.Seconds
	r.Log = tailLines(res.Log, 256<<10)
	if proxy != nil {
		reqs, dropped := proxy.Requests()
		for _, q := range reqs {
			r.Requests = append(r.Requests, models.AbilityRequest{At: q.At, Method: q.Method, Host: q.Host, Port: q.Port, Path: q.Path, Query: q.Query, Status: q.Status, Sent: q.Sent,
				Received: q.Received, Millis: q.Millis, Keys: q.Keys, Refused: q.Refused})
		}
		r.RequestsLeftOut = dropped
	}
	switch {
	case res.TimedOut:
		return res, fmt.Sprintf("It took longer than %d minutes and was stopped.", f.Minutes), false
	case res.Exit != 0:
		return res, fmt.Sprintf("It stopped with an error (status %d); what it printed is in the run's log.", res.Exit), false
	}
	return res, "", false
}

// testDial and testRootCAs stand in for the internet in tests (local test
// servers under made-up names).
var (
	testDial    func(ctx context.Context, network, addr string) (net.Conn, error)
	testRootCAs *x509.CertPool
)

// loadArchive reads a version's archive from the media store and checks it
// is the version.
func loadArchive(ctx context.Context, a *app.App, v *models.AbilityVersion) (*Archive, error) {
	if v.Archive.SHA256 != v.Hash || !ValidHash(v.Hash) {
		return nil, apperr.New(apperr.Integrity, "The stored files of “%s” aren't its version.", v.Title)
	}
	p, err := a.Cache.Ensure(ctx, a.Media, v.Archive.Key, v.Archive.SHA256, v.Archive.Size, nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't fetch the files of “%s”.", v.Title)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	arc, err := Parse(b)
	if err != nil || arc.Hash != v.Hash {
		return nil, apperr.New(apperr.Integrity, "The stored files of “%s” aren't its version.", v.Title)
	}
	return arc, nil
}

// takeIn adds one output through the classifier, with how it was made.
func takeIn(ctx context.Context, a *app.App, r *models.AbilityRun, o roomOutput, made models.AbilityMadeWith) models.AbilityOutput {
	out := models.AbilityOutput{Path: o.Rel, Kind: o.Kind, Size: o.Size, SHA256: o.SHA256, Title: o.Title, Licence: o.Licence, Credit: o.Credit}
	if !o.OK {
		out.Status, out.Why = "left out", o.Why
		return out
	}
	made.Licence, made.Source, made.Credit, made.Author = o.Licence, o.Source, o.Credit, o.Author
	title := firstOf(o.Title, strings.TrimSuffix(o.Rel[strings.LastIndex(o.Rel, "/")+1:], extOf(o.Rel)))
	switch o.Kind {
	case OutCaptions:
		keepCaptions(ctx, a, r, o, made, &out)
	case OutFootage, OutSound:
		src := &models.AssetSource{Platform: models.PlatformAbility, URL: o.Source, Title: title, License: o.Licence, Uploader: o.Author, DownloadedAt: now(),
			Tool: "MetaRush ability " + made.Ability, Method: "ability", MadeWith: &made}
		res := ingest.File(ctx, a, o.Abs, ingest.Options{ProjectID: r.ProjectID, Via: a.Actor.Via, Transcribe: true, Title: title, Source: src, FootageOnly: true,
			Duplicates: ingest.DuplicatesKeep, DisplayName: o.Rel[strings.LastIndex(o.Rel, "/")+1:]})
		out.AssetID = res.AssetID
		switch res.Status {
		case "ingested", "resumed":
			out.Status = "added"
		case "deduped", "duplicate":
			out.Status = "already there"
		default:
			out.Status, out.Why = "left out", firstOf(res.Error, "MetaRush couldn't take it in.")
		}
		if out.AssetID != "" && (res.Status == "ingested" || res.Status == "resumed") {
			// Only a clip this run brought in: bytes already in the library
			// keep whatever rights they have (or don't).
			keepRights(ctx, a, out.AssetID, o)
		}
	default:
		scope := models.ScopeProject
		if r.ProjectID == "" {
			scope = models.ScopeWorkspace
		}
		mw := made
		res, err := resources.AddFile(ctx, a, o.Abs, resources.AddOptions{Scope: scope, ProjectID: r.ProjectID, Title: title, License: o.Licence, Via: a.Actor.Via, Fresh: true,
			Source: models.ResourceSource{Kind: models.FromGenerated, Tool: "ability", URL: o.Source, License: o.Licence, Author: o.Author, Name: o.Rel, Machine: r.Machine, At: now(),
				MadeWith: &models.ResourceMadeWith{Kind: "ability", Ability: &mw}}})
		switch {
		case err != nil:
			out.Status, out.Why = "left out", apperr.From(err).Message
		case res.Status == "same":
			out.Status = "already there"
		default:
			out.Status = "added"
		}
		if res != nil && res.Resource != nil {
			out.ResourceID = res.Resource.ID
		}
	}
	return out
}

// setupFiles are a setup's files (none without a setup).
func setupFiles(s *models.AbilitySetup) []models.AbilityFilePin {
	if s == nil {
		return nil
	}
	return s.Files
}

// keepCaptions keeps a captions file with the clip the run was given (D285):
// stored by its SHA-256, listed on the clip with its language, whether a
// machine translated it and how it was made; a newer file of the same
// language from the same ability takes its place.
func keepCaptions(ctx context.Context, a *app.App, r *models.AbilityRun, o roomOutput, made models.AbilityMadeWith, out *models.AbilityOutput) {
	clip := ""
	for _, in := range r.Inputs {
		if in.Ref != "" && (in.Kind == InFootage || in.Kind == InSound) {
			clip = in.Ref
		}
	}
	lines, vtt, ok := captionLines(o.Abs)
	switch {
	case clip == "":
		out.Status, out.Why = "left out", "captions are kept with the clip they were made from, and this run had none"
		return
	case !ok:
		out.Status, out.Why = "left out", "it isn't a captions file MetaRush can read"
		return
	case o.Language == "":
		out.Status, out.Why = "left out", "the run didn't say the captions' language"
		return
	}
	key := media.ObjectKey(a.WorkspaceID(), o.SHA256)
	if _, err := a.Media.PutFile(ctx, key, o.Abs, media.PutOptions{ContentType: "text/plain; charset=utf-8", SHA256: o.SHA256}, media.Resume{}, nil); err != nil {
		out.Status, out.Why = "left out", "MetaRush couldn't keep it in the media store"
		return
	}
	format := map[bool]string{true: "vtt", false: "srt"}[vtt] // by its bytes, not its name
	c := models.AssetCaptions{Language: o.Language, Format: format, Lines: lines, Translated: o.Translated, Model: o.Model, SHA256: o.SHA256, StorageKey: key, Size: o.Size, MadeWith: &made, At: now()}
	// One update: the same language from the same ability is replaced, never
	// kept twice when two runs end together. $literal: the run's words are
	// data, never field paths.
	keep := bson.M{"$filter": bson.M{"input": bson.M{"$ifNull": bson.A{"$captions", bson.A{}}}, "cond": bson.M{"$not": bson.A{bson.M{"$and": bson.A{
		bson.M{"$eq": bson.A{"$$this.language", bson.M{"$literal": c.Language}}}, bson.M{"$eq": bson.A{"$$this.madeWith.name", bson.M{"$literal": made.Name}}}}}}}}}
	res, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": clip, "workspaceId": a.WorkspaceID()}, bson.A{bson.M{"$set": bson.M{
		"captions": bson.M{"$concatArrays": bson.A{keep, bson.M{"$literal": bson.A{c}}}}, "updatedAt": now()}}})
	switch {
	case err != nil:
		out.Status, out.Why = "left out", "MetaRush couldn't keep it with the clip"
		return
	case res.MatchedCount == 0:
		out.Status, out.Why = "left out", "the clip it was made from isn't there any more"
		return
	}
	out.AssetID, out.Status = clip, "added"
}

// keepRights records the licence a run said a clip or sound has as its
// rights (D38, D283): the licence, the credit it asks for and where it came
// from, by the person whose run it was. A clip whose rights someone already
// recorded keeps them.
func keepRights(ctx context.Context, a *app.App, assetID string, o roomOutput) {
	if strings.TrimSpace(o.Licence) == "" {
		return
	}
	at, by := now(), a.Actor
	r := models.MusicRights{License: o.Licence, Attribution: o.Credit, Source: o.Source, RecordedBy: &by, RecordedAt: &at}
	_, _ = a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": assetID, "workspaceId": a.WorkspaceID(), "rights.license": bson.M{"$in": bson.A{nil, ""}}},
		bson.M{"$set": bson.M{"rights": r, "updatedAt": at}})
}

func extOf(p string) string {
	if i := strings.LastIndex(p, "."); i > strings.LastIndex(p, "/") {
		return p[i:]
	}
	return ""
}

// summaryOf says what a finished run made.
func summaryOf(r *models.AbilityRun) string {
	added, left := 0, 0
	for _, o := range r.Outputs {
		if o.Status == "left out" {
			left++
		} else {
			added++
		}
	}
	s := fmt.Sprintf("Run %d of “%s” is done", r.Number, r.Title)
	switch {
	case added == 0 && left == 0:
		s += "; it brought nothing back"
	case added > 0:
		s += "; it brought back " + plural(added, "file", "files")
	}
	if left > 0 {
		s += fmt.Sprintf(" (%s left out)", plural(left, "file", "files"))
	}
	refused := 0
	for _, q := range r.Requests {
		if q.Refused != "" {
			refused++
		}
	}
	if refused > 0 {
		s += fmt.Sprintf("; MetaRush refused %s", plural(refused, "request", "requests"))
	}
	return s + "."
}

// finish records how a run ended and tells the person who started it.
func finish(ctx context.Context, a *app.App, r *models.AbilityRun, state, summary, errText string, res *roomResult) {
	done := now()
	set := map[string]any{"state": state, "finishedAt": done, "requests": r.Requests, "requestsLeftOut": r.RequestsLeftOut, "outputs": r.Outputs}
	if r.Requests == nil {
		set["requests"] = []models.AbilityRequest{}
	}
	if r.Outputs == nil {
		set["outputs"] = []models.AbilityOutput{}
	}
	if summary != "" {
		set["summary"] = summary
	}
	if errText != "" {
		set["error"] = errText
	}
	if res != nil {
		set["exit"], set["seconds"], set["log"] = res.Exit, res.Seconds, r.Log
	}
	_ = setRun(ctx, a, r.ID, set)
	a.Audit(ctx, "ability.ran", "ability", docID(a.WorkspaceID(), r.Hash), r.ProjectID, map[string]any{"run": r.ID, "number": r.Number, "name": r.Name, "state": state,
		"requests": len(r.Requests), "outputs": len(r.Outputs), "machine": r.Machine, "work": r.Work})
	if r.Work != "" {
		return // AI work it did as a provider tells the person itself (D281)
	}
	tone, title := "success", firstOf(summary, fmt.Sprintf("Run %d of “%s” is done.", r.Number, r.Title))
	if state != models.AbilityRunDone {
		tone, title = "error", fmt.Sprintf("Run %d of “%s” didn't finish", r.Number, r.Title)
	}
	_, _ = notify.Upsert(ctx, a, models.Notification{UserID: r.By.UserID, Kind: models.NotifyAbilityRun, GroupKey: "ability-run:" + r.ID, ProjectID: r.ProjectID, Tone: tone,
		Title: strings.TrimSuffix(title, "."), Body: errText, Link: links.AbilityRun(r.Name, r.ID), JobID: r.JobID, Machine: r.Machine,
		Details: map[string]any{"ability": r.Name, "run": r.ID}}, notify.Once)
}
