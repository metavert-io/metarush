package spoken

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
	"github.com/metavert-io/metarush/internal/voice"
)

// Origin is the prefix of the AI work a change asks for ("say:<change>").
const Origin = "say:"

func init() {
	aiwork.RegisterKeeper(Origin, keepSaid, endedSaid)
	generate.OnConsentRevoked(consentWithdrawn)
}

// consentWithdrawn marks the changes that relied on a withdrawn consent as
// withdrawn (D309), and the mouths matched with it (D312): none goes into a
// cut any more; versions that play one are held back from going out until a
// person takes it out, and the person who withdrew is told.
func consentWithdrawn(ctx context.Context, a *app.App, c *models.Consent) []string {
	var said []string
	now := time.Now().UTC()
	res, err := a.DB.C(store.CSpokenChanges).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "consentId": c.ID,
		"state": bson.M{"$in": bson.A{models.SpokenMaking, models.SpokenReady, models.SpokenPlaced, models.SpokenUndone}}},
		bson.M{"$set": bson.M{"state": models.SpokenWithdrawn, "why": c.PersonName + "'s consent for new words in their voice was withdrawn.", "updatedAt": now}})
	if err == nil && res.ModifiedCount > 0 {
		words := "1 change of words in their voice"
		if n := res.ModifiedCount; n != 1 {
			words = fmt.Sprintf("%d changes of words in their voice", n)
		}
		said = append(said, words+" can't go into a cut any more; take it out where a cut plays it (Undo).")
	}
	var gens []models.Generation
	if a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"consentIds": c.ID, "origin": bson.M{"$regex": "^" + regexp.QuoteMeta(MouthOrigin)}}, &gens) != nil || len(gens) == 0 {
		return said
	}
	ids := bson.A{}
	for _, g := range gens {
		ids = append(ids, g.ID)
	}
	res, err = a.DB.C(store.CSpokenChanges).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "mouth.generationId": bson.M{"$in": ids}, "mouth.state": models.SpokenReady},
		bson.M{"$set": bson.M{"mouth.state": models.SpokenWithdrawn, "mouth.why": c.PersonName + "'s consent for lip sync, which the matched mouth relied on, was withdrawn.", "updatedAt": now}})
	if err == nil && res.ModifiedCount > 0 {
		mouths := "1 mouth matched to new words"
		if n := res.ModifiedCount; n != 1 {
			mouths = fmt.Sprintf("%d mouths matched to new words", n)
		}
		said = append(said, mouths+" can't go into a cut any more; take the change out where a cut plays it (Undo).")
	}
	return said
}

// keepSaid takes the speech made for a change (in the worker, D310): the
// bytes are kept by their content (the generation names them); the new words
// are found in it and checked — the words the recognizer hears, the voice's
// fingerprint — fitted into the stretch, checked again, and kept as a clip
// marked as AI speech in the person's voice. A change that doesn't pass is
// failed with why; what was paid for stays on the receipt.
func keepSaid(ctx context.Context, a *app.App, g *models.Generation, path, mime string) (*models.GenOutput, error) {
	st, err := generate.Keep(ctx, a, path, mime)
	if err != nil {
		return nil, err
	}
	out := &models.GenOutput{SHA256: st.SHA256, Size: st.Size, MIME: mime, HasAudio: true, Status: "kept"}
	if mi, err := probe.Probe(ctx, a.Tools, path); err == nil {
		out.Duration = mi.Duration
	}
	id := strings.TrimPrefix(g.Origin, Origin)
	var c models.SpokenChange
	if err := a.DB.GetByID(ctx, store.CSpokenChanges, a.WorkspaceID(), id, &c); err != nil {
		return out, nil // its record is gone (the project was cleared out): the speech stays on the generation only
	}
	if c.State != models.SpokenMaking || c.GenerationID != g.ID {
		return out, nil // not this change's own speech, or it has moved on: the speech stays on its generation only
	}
	tmp, done, err := generate.Scratch(a, "x")
	if err != nil {
		return nil, err
	}
	defer done()
	scratch := filepath.Dir(tmp)
	res, why := fitAndCheck(ctx, a, &c, path, scratch)
	now := time.Now().UTC()
	set := bson.M{"updatedAt": now, "generationId": g.ID}
	if res.check != nil {
		set["check"] = res.check
	}
	if res.fit != nil {
		set["fit"] = res.fit
	}
	if why != "" {
		set["state"], set["why"] = models.SpokenFailed, why
		if stillMaking(ctx, a, &c, g.ID, bson.M{"$set": set}) {
			tell(ctx, a, &c, false, why)
		}
		return out, nil
	}
	// Moved on while it was checked (its consent withdrawn, failed by a sweep): nothing is kept for it.
	var now2 models.SpokenChange
	if err := a.DB.GetByID(ctx, store.CSpokenChanges, a.WorkspaceID(), c.ID, &now2); err != nil || now2.State != models.SpokenMaking || now2.GenerationID != g.ID {
		return out, nil
	}
	clip, err := keepClip(ctx, a, g, &c, res)
	if err != nil {
		set["state"], set["why"] = models.SpokenFailed, "MetaRush couldn't keep the new words: "+err.Error()
		if stillMaking(ctx, a, &c, g.ID, bson.M{"$set": set}) {
			tell(ctx, a, &c, false, set["why"].(string))
		}
		return out, nil
	}
	out.AssetID = clip
	set["state"], set["sound"] = models.SpokenReady, clip
	if stillMaking(ctx, a, &c, g.ID, bson.M{"$set": set, "$unset": bson.M{"why": ""}}) {
		c.Sound = clip
		tell(ctx, a, &c, true, "")
	}
	return out, nil
}

// stillMaking writes a change's outcome only while it is still being made
// with this generation (D310): a consent withdrawn or a sweep meanwhile wins.
func stillMaking(ctx context.Context, a *app.App, c *models.SpokenChange, genID string, update bson.M) bool {
	res, err := a.DB.C(store.CSpokenChanges).UpdateOne(ctx, bson.M{"_id": c.ID, "workspaceId": a.WorkspaceID(), "state": models.SpokenMaking, "generationId": genID}, update)
	return err == nil && res.MatchedCount == 1
}

// endedSaid closes a change whose speech wasn't made (failed, refused or
// stopped): nothing was kept, and the person hears why.
func endedSaid(ctx context.Context, a *app.App, g *models.Generation) {
	if g.Outcome == nil || g.Outcome.State == models.GenDone {
		return
	}
	id := strings.TrimPrefix(g.Origin, Origin)
	var c models.SpokenChange
	if err := a.DB.GetByID(ctx, store.CSpokenChanges, a.WorkspaceID(), id, &c); err != nil || c.State != models.SpokenMaking || c.GenerationID != g.ID {
		return
	}
	why := strings.TrimSpace(g.Outcome.Message + " " + g.Outcome.Hint)
	if why == "" {
		why = "The speech couldn't be made."
	}
	if g.Outcome.State == models.GenCanceled {
		why = "It was stopped before the speech was made."
	}
	if stillMaking(ctx, a, &c, g.ID, bson.M{"$set": bson.M{"state": models.SpokenFailed, "why": why, "updatedAt": time.Now().UTC()}}) && g.Outcome.State != models.GenCanceled {
		tell(ctx, a, &c, false, why)
	}
}

type checked struct {
	path  string
	fit   *models.SpokenFit
	check *models.SpokenCheck
}

// fitAndCheck finds, checks and fits the new words; why is the plain reason
// it didn't pass ("" when it did).
func fitAndCheck(ctx context.Context, a *app.App, c *models.SpokenChange, speech, scratch string) (res checked, why string) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), c.AssetID, &as); err != nil {
		return res, "The clip whose words were changed is gone."
	}
	lang := ""
	if as.Language != nil {
		lang = as.Language.Code
	}
	chk := &models.SpokenCheck{At: time.Now().UTC()}
	res.check = chk
	// 1. What the service said, written down on this computer.
	made16 := filepath.Join(scratch, "made-16k.wav")
	if err := to16k(ctx, a, speech, made16); err != nil {
		return res, "MetaRush couldn't read the speech that came back."
	}
	heard, err := Hear(ctx, a, made16, scratch, lang)
	if err != nil {
		return res, "MetaRush couldn't check the new words on this computer: " + lowerFirst(err.Error())
	}
	before, after := contextAround(c)
	i, j, ok := findNew(heard, before, strings.Fields(c.Words), after)
	if !ok {
		chk.Heard = joinWords(heard)
		return res, fmt.Sprintf("The new words weren't heard in what came back (it said “%s”), so nothing was put in the cut.", chk.Heard)
	}
	// 2. The voice: the whole speech made, against the person's voice.
	pcm, err := voice.ReadWAV(made16)
	if err != nil {
		return res, "MetaRush couldn't read the speech that came back."
	}
	secs := float64(len(pcm)) / voice.SampleRate
	chk.Seconds = math.Round(secs*10) / 10
	chk.VoiceNeeded = voiceNeeded(secs)
	if math.IsInf(chk.VoiceNeeded, 1) {
		chk.VoiceNeeded = 1
		return res, "The speech that came back is too short to tell whether it sounds like " + c.Speaker.Name + "."
	}
	own := ownVoice(ctx, a, c)
	score, err := voiceCheck(ctx, a, pcm, c.Speaker.PersonID, own)
	if err != nil {
		return res, "MetaRush couldn't compare the new voice with " + c.Speaker.Name + "'s: " + lowerFirst(err.Error())
	}
	chk.Voice = &score
	chk.VoiceMatch = score >= chk.VoiceNeeded
	if !chk.VoiceMatch {
		return res, fmt.Sprintf("The new words don't sound enough like %s (%.0f%% alike; %.0f%% needed), so nothing was put in the cut.", c.Speaker.Name, score*100, chk.VoiceNeeded*100)
	}
	// … and the stretch around the new words alone, so they can't pass on the words around them.
	w0, w1 := wordsWindow(heard[i].From, heard[j].To, secs)
	chk.WordsSeconds = math.Round((w1-w0)*10) / 10
	ws, err := voiceCheck(ctx, a, pcm[int(w0*voice.SampleRate):min(len(pcm), int(w1*voice.SampleRate))], c.Speaker.PersonID, own)
	if err != nil {
		return res, "MetaRush couldn't compare the new voice with " + c.Speaker.Name + "'s: " + lowerFirst(err.Error())
	}
	chk.WordsVoice = &ws
	if need := voiceNeeded(w1 - w0); ws < need {
		chk.VoiceMatch = false
		return res, fmt.Sprintf("The new words themselves don't sound enough like %s (%.0f%% alike; %.0f%% needed), so nothing was put in the cut.", c.Speaker.Name, ws*100, need*100)
	}
	// 3. Fit the new words into the stretch.
	// How far the new words may reach in the speech made: not into the words around them.
	cutFrom, cutTo := 0.0, math.Inf(1)
	if i > 0 {
		cutFrom = heard[i-1].To - 0.05
	}
	if j+1 < len(heard) {
		cutTo = heard[j+1].From + 0.05
	}
	src, err := generate.Original(ctx, a, &as)
	if err != nil {
		return res, "MetaRush couldn't read the clip: " + lowerFirst(err.Error())
	}
	prevEnd, nextBeg, pauses := neighbours(ctx, a, c)
	path, f, err := fit(ctx, a, fitIn{src: src, duration: as.Media.Duration, channels: ops.AudioChannelsOf(&as), wf: c.From, wt: c.To,
		prevEnd: prevEnd, nextBeg: nextBeg, pauses: pauses, speech: speech, newFrom: heard[i].From, newTo: heard[j].To, cutFrom: cutFrom, cutTo: cutTo, scratch: scratch})
	var tl errTooLong
	switch {
	case errors.As(err, &tl):
		return res, tl.Error()
	case err != nil:
		return res, "MetaRush couldn't fit the new words in: " + lowerFirst(err.Error())
	}
	res.fit = &models.SpokenFit{From: f.From, To: f.To, WordsFrom: f.WordsFrom, WordsTo: f.WordsTo, Speed: f.Speed, GainDB: math.Round(f.GainDB*10) / 10,
		RoomFrom: round3(f.RoomFrom), RoomTo: round3(f.RoomTo)}
	// 4. The new words as the cut will play them, in their sentence: the
	// recognizer must hear exactly the new words between the words around them.
	fit16 := filepath.Join(scratch, "fitted-16k.wav")
	if err := to16k(ctx, a, f.CheckPath, fit16); err != nil {
		return res, "MetaRush couldn't read the new sound it made."
	}
	got, err := Hear(ctx, a, fit16, scratch, lang)
	if err != nil {
		return res, "MetaRush couldn't check the new words on this computer: " + lowerFirst(err.Error())
	}
	segs, _ := transcripts.LoadSegments(ctx, a, c.AssetID)
	var heardBefore, heardAfter []string
	for _, u := range transcripts.Units(segs) {
		mid := (u.From + u.To) / 2
		switch {
		case mid >= f.CheckFrom && mid < c.From:
			heardBefore = append(heardBefore, u.Text)
		case mid > c.To && mid <= f.CheckTo:
			heardAfter = append(heardAfter, u.Text)
		}
	}
	if gi, gj, ok := findNew(got, heardBefore, strings.Fields(c.Words), heardAfter); ok {
		chk.Heard = joinWords(got[gi : gj+1])
	} else {
		var inside []string
		lo, hi := f.WordsFrom-f.CheckFrom-0.3, f.WordsTo-f.CheckFrom+0.3
		for _, w := range got {
			if mid := (w.From + w.To) / 2; mid >= lo && mid <= hi {
				inside = append(inside, w.Text)
			}
		}
		chk.Heard = strings.Join(inside, " ")
	}
	chk.WordsMatch = sameWords(c.Words, chk.Heard)
	if !chk.WordsMatch {
		heardSay := chk.Heard
		if heardSay == "" {
			heardSay = "nothing"
		}
		return res, fmt.Sprintf("In the cut the new words sound like “%s”, not “%s”, so nothing was put in the cut.", heardSay, c.Words)
	}
	chk.Passed = true
	res.path = path
	return res, ""
}

// contextAround splits a change's context into the speaker's words before and after the new words.
func contextAround(c *models.SpokenChange) (before, after []string) {
	all := strings.Fields(c.Context)
	nw := strings.Fields(c.Words)
	for k := 0; k+len(nw) <= len(all); k++ {
		if strings.Join(all[k:k+len(nw)], " ") == strings.Join(nw, " ") {
			return all[:k], all[k+len(nw):]
		}
	}
	return nil, nil
}

// ownVoice is the fingerprint of the voice in the recording that says the old words.
func ownVoice(ctx context.Context, a *app.App, c *models.SpokenChange) []float32 {
	vs, err := speakers.Voices(ctx, a, c.AssetID, true)
	if err != nil {
		return nil
	}
	if v := voiceByLabel(vs, c.Speaker.Label); v != nil {
		return v.Centroid
	}
	return nil
}

// neighbours are where the words around the change end and start, and the
// pauses between words in the recording (the room's quiet is taken from one).
func neighbours(ctx context.Context, a *app.App, c *models.SpokenChange) (prevEnd, nextBeg float64, pauses []span) {
	prevEnd, nextBeg = math.Max(0, c.From-0.5), c.To+0.5
	segs, err := transcripts.LoadSegments(ctx, a, c.AssetID)
	if err != nil {
		return
	}
	us := transcripts.Units(segs)
	for k, u := range us {
		if u.To <= c.From+0.001 {
			prevEnd = u.To
		}
		if u.From >= c.To-0.001 && (k == 0 || us[k-1].From < c.To-0.001) {
			nextBeg = u.From
		}
		if k > 0 {
			if gap := u.From - us[k-1].To; gap >= roomMin+2*roomInset {
				pauses = append(pauses, span{us[k-1].To, u.From})
			}
		}
	}
	return
}

// keepClip keeps the new sound as a clip of the project, marked as AI speech
// in the person's voice, saying what it replaces.
func keepClip(ctx context.Context, a *app.App, g *models.Generation, c *models.SpokenChange, res checked) (string, error) {
	title := fmt.Sprintf("“%s” in %s's own voice (was “%s”)", clipWords(c.Words, 50), c.Speaker.Name, clipWords(c.Before, 40))
	src := &models.AssetSource{Platform: models.PlatformGenerated, Title: title, Duration: res.fit.To - res.fit.From, DownloadedAt: time.Now().UTC(),
		Tool: g.ProviderTitle, ToolVersion: g.Model, Method: models.SourceGenerated,
		Generated: &models.GeneratedSource{GenerationID: g.ID, Kind: g.Kind, Provider: g.Provider, ProviderTitle: g.ProviderTitle, Model: g.Model, ModelTitle: g.ModelTitle,
			Operation: g.Operation, Prompt: generate.Clip(c.Words, 300), Likeness: true,
			Said: &models.GeneratedSaid{ChangeID: c.ID, AssetID: c.AssetID, From: res.fit.From, To: res.fit.To, Before: c.Before, Words: c.Words, Who: c.Speaker.Name}}}
	r := ingest.File(ctx, a, res.path, ingest.Options{ProjectID: c.ProjectID, DisplayName: "new-words-" + strings.TrimPrefix(c.ID, "swc_") + ".wav", Title: title, Source: src,
		Via: firstNonEmpty(c.CreatedBy.Via, "cli"), Tags: []string{models.TagAIGenerated}, Transcribe: false, FootageOnly: true, Duplicates: ingest.DuplicatesKeep})
	switch r.Status {
	case "failed", "skipped":
		return "", errors.New(r.Error)
	}
	if r.AssetID != "" {
		return r.AssetID, nil
	}
	if r.Duplicate != nil {
		return r.Duplicate.AssetID, nil
	}
	return "", errors.New("it wasn't added")
}

// tell lets the person who asked know how it went.
func tell(ctx context.Context, a *app.App, c *models.SpokenChange, ok bool, why string) {
	if c.CreatedBy.UserID == "" {
		return
	}
	n := models.Notification{UserID: c.CreatedBy.UserID, GroupKey: "spoken:" + c.ID, ProjectID: c.ProjectID, JobID: c.JobID,
		Kind: models.NotifyGenerationReady, Tone: "success",
		Title:   fmt.Sprintf("“%s” in %s's voice is ready to hear", clipWords(c.Words, 40), c.Speaker.Name),
		Body:    "Listen to it, then put it in the cut (it's marked as changed with AI everywhere).",
		Link:    links.Asset(c.ProjectID, c.AssetID),
		Details: map[string]any{"changeId": c.ID, "assetId": c.AssetID, "sound": c.Sound}}
	if !ok {
		n.Kind, n.Tone = models.NotifyGenerationFailed, "error"
		n.Title = fmt.Sprintf("“%s” in %s's voice didn't pass its check", clipWords(c.Words, 40), c.Speaker.Name)
		n.Body = why
	}
	_, _ = notify.Upsert(ctx, a, n, notify.Once)
}

func joinWords(ws []models.Word) string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Text)
	}
	return strings.Join(out, " ")
}

func clipWords(s string, n int) string { return generate.Clip(s, n) }

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
