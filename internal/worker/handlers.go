package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/transcripts"
)

func loadAsset(ctx context.Context, a *app.App, j *models.Job) (*models.Asset, error) {
	id, _ := j.Payload["assetId"].(string)
	if id == "" {
		id = j.AssetID
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
		return nil, permanent("asset %s not found", id)
	}
	if as.State != models.AssetAvailable {
		return nil, fmt.Errorf("asset %s is %s, not yet available", as.ID, as.State)
	}
	return &as, nil
}

// original returns a verified local copy of the file an asset uses now: its
// fixed copy when fixes are ticked (D152), else the original. Everything
// after a fix (previews, transcripts, voices, what's on screen) reads it.
func original(ctx context.Context, a *app.App, as *models.Asset, report Report) (string, error) {
	key, sha, size := as.CurrentMedia()
	return a.Cache.Ensure(ctx, a.Media, key, sha, size, func(done, total int64) {
		if report != nil && total > 0 {
			report(0.3*float64(done)/float64(total), "getting the original file")
		}
	})
}

// publish uploads a derived file to the canonical store (content-addressed) and
// keeps a verified copy in the local cache.
func publish(ctx context.Context, a *app.App, path, mime string) (key, sha string, size int64, err error) {
	sha, err = media.HashFile(path)
	if err != nil {
		return
	}
	key = media.ObjectKey(a.WorkspaceID(), sha)
	info, err := a.Media.PutFile(ctx, key, path, media.PutOptions{ContentType: mime, SHA256: sha}, media.Resume{}, nil)
	if err != nil {
		err = fmt.Errorf("upload to canonical store failed (job not marked done): %w", err)
		return
	}
	size = info.Size
	_, _ = a.Cache.Adopt(path, sha)
	return
}

func scratchDir(a *app.App, j *models.Job) (string, error) {
	d := filepath.Join(a.Cfg.Scratch.Dir, j.ID)
	return d, os.MkdirAll(d, 0o755)
}

func deriveHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	a := w.App
	as, err := loadAsset(ctx, a, j)
	if err != nil {
		return nil, err
	}
	src, err := original(ctx, a, as, report)
	if err != nil {
		return nil, err
	}
	dir, err := scratchDir(a, j)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	d := &derive.Deriver{T: a.Tools}
	var v *models.Variant
	doing := map[string]string{jobs.TypeProxy: "making the preview copy", jobs.TypeThumbnail: "making the thumbnail", jobs.TypeContactSheet: "making the contact sheet",
		jobs.TypeWaveform: "drawing the sound wave", jobs.TypeMezzanine: "making the steady copy"}[j.Type]
	if doing == "" {
		doing = "processing"
	}
	report(0.35, doing)
	switch j.Type {
	case jobs.TypeProxy:
		v, err = d.Proxy(ctx, src, filepath.Join(dir, "proxy.mp4"), &as.Media)
	case jobs.TypeThumbnail:
		v, err = d.Thumbnail(ctx, src, filepath.Join(dir, "thumb.jpg"), &as.Media)
	case jobs.TypeContactSheet:
		v, _, err = d.ContactSheet(ctx, src, filepath.Join(dir, "sheet.jpg"), dir, &as.Media, 12)
	case jobs.TypeWaveform:
		v, err = d.Peaks(ctx, src, filepath.Join(dir, "peaks.json"), &as.Media)
	case jobs.TypeMezzanine:
		v, err = d.Mezzanine(ctx, src, filepath.Join(dir, "mezzanine.mp4"), &as.Media)
	default:
		return nil, permanent("unknown derive type %s", j.Type)
	}
	if err != nil {
		return nil, err
	}
	report(0.85, "saving")
	out := filepath.Join(dir, map[string]string{jobs.TypeProxy: "proxy.mp4", jobs.TypeThumbnail: "thumb.jpg", jobs.TypeContactSheet: "sheet.jpg", jobs.TypeWaveform: "peaks.json", jobs.TypeMezzanine: "mezzanine.mp4"}[j.Type])
	key, sha, size, err := publish(ctx, a, out, v.MIME)
	if err != nil {
		return nil, err
	}
	v.StorageKey, v.SHA256, v.Size, v.CreatedAt = key, sha, size, time.Now().UTC()
	// The variant belongs to the media it was made from: the source's own
	// while that media is still the one it uses (a fix may have switched it
	// meanwhile, D152).
	if err := storeVariant(ctx, a, as, v); err != nil {
		return nil, err
	}
	if j.Type == jobs.TypeThumbnail {
		for _, pid := range as.ProjectIDs {
			_, _ = a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": pid, "workspaceId": a.WorkspaceID(), "coverAssetId": bson.M{"$in": bson.A{nil, ""}}}, bson.M{"$set": bson.M{"coverAssetId": as.ID}})
		}
	}
	return map[string]any{"variant": v.Kind, "sha256": sha, "size": size}, nil
}

func evidenceHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	a := w.App
	as, err := loadAsset(ctx, a, j)
	if err != nil {
		return nil, err
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"analysis.evidence": "running"}})
	src, srcName := "", "original"
	if px, ok := as.Variants[models.VariantProxy]; ok {
		if p, err := a.Cache.Ensure(ctx, a.Media, px.StorageKey, px.SHA256, px.Size, nil); err == nil {
			src, srcName = p, "proxy"
		}
	}
	if src == "" {
		if src, err = original(ctx, a, as, report); err != nil {
			return nil, err
		}
	}
	dir, err := scratchDir(a, j)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	report(0.4, "finding pauses, loud and quiet parts, and cuts")
	mi := as.Media
	if srcName == "proxy" {
		mi.HasAudio = as.Media.HasAudio
	}
	rep, err := evidence.Analyze(ctx, a.Tools, src, srcName, dir, &mi)
	if err != nil {
		return nil, err
	}
	// The evidence belongs to the media it measured (D152): a fixed copy's,
	// or the original's; a version reads the evidence of what it plays.
	_, mediaSHA, _ := as.CurrentMedia()
	digest := mediaSHA + ":" + evidence.Version
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: "technical", Provider: "ffmpeg", Model: a.Tools.Version(ctx, "ffmpeg"),
		PromptVersion: evidence.Version, Digest: digest, Data: rep.ToMap(), CreatedAt: time.Now().UTC(), Media: as.MediaSHA()}
	old := store.SameMedia(as.ID, as.MediaSHA())
	old["workspaceId"], old["kind"] = a.WorkspaceID(), bson.M{"$in": bson.A{"technical", evidence.InsightKind}}
	if _, err := a.DB.C(store.CInsights).DeleteMany(ctx, old); err != nil {
		return nil, err
	}
	if err := a.DB.Insert(ctx, store.CInsights, ins); err != nil {
		return nil, err
	}
	if rep.Momentary != nil {
		// The loudness every 0.1 s, so speech leveling knows how loud each part is.
		ml := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: evidence.InsightKind, Provider: "ffmpeg", Model: ins.Model,
			PromptVersion: evidence.Version, Digest: digest, Data: rep.Momentary.SeriesData(), CreatedAt: ins.CreatedAt, Media: ins.Media}
		if err := a.DB.Insert(ctx, store.CInsights, ml); err != nil {
			return nil, err
		}
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"analysis.evidence": "available", "updatedAt": time.Now().UTC()}})
	return map[string]any{"insightId": ins.ID, "scenes": len(rep.Scenes), "silences": len(rep.Silences), "black": len(rep.Black), "freezes": len(rep.Freezes),
		"visualChanges": len(rep.VisualChanges)}, nil
}

func transcribeHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	a := w.App
	as, err := loadAsset(ctx, a, j)
	if err != nil {
		return nil, err
	}
	if !as.Media.HasAudio {
		return nil, permanent("asset has no audio")
	}
	// When this job started, in the database server's time: a person's
	// choice of language on another computer is compared with it (D360).
	now := w.serverNow(ctx)
	// A transcript made in the cloud (opt-in, D228) is read from its record;
	// everything after — voices, people's fixes, spelling lists — is the same.
	txName, txModel := w.Tx.Name(), w.Tx.Model()
	var cloud *transcribe.Transcript
	if gid, _ := j.Payload["generationId"].(string); gid != "" {
		tr, name, model, err := aiwork.CloudTranscript(ctx, a, gid)
		if err != nil {
			return nil, permanent("%s", err.Error())
		}
		cloud, txName, txModel = tr, name, model
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"transcript": models.TranscriptStatus{State: "running", Provider: txName, Model: txModel, UpdatedAt: now}, "analysis.transcribe": "running"}})
	src, err := original(ctx, a, as, report)
	if err != nil {
		return nil, err
	}
	dir, err := scratchDir(a, j)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "audio.wav")
	report(0.3, "getting the sound")
	if err := (&derive.Deriver{T: a.Tools}).ExtractAudioWAV(ctx, src, wav); err != nil {
		return nil, err
	}
	// Each clip is written down in the language it is spoken in (D243): the
	// one a person set, else the one heard, else the workspace's. A transcript
	// made in the cloud (D228) comes with its own language.
	tx, language := w.Tx, a.Cfg.Transcription.Language
	var spoken *models.SpokenLanguage
	if cloud == nil && w.Langs != nil {
		report(0.35, "listening for the language")
		sl := clipLanguage(ctx, w, as, j, wav, dir)
		spoken, language = &sl, sl.Code
		if tx = w.Langs.For(language); tx == nil {
			return nil, permanent("“%s” is spoken in %s, and this computer can only write down English: other languages need whisper.cpp with a multilingual model (metarush doctor says how to set it up)",
				as.DisplayTitle(), transcribe.SpokenLanguageName(language))
		}
		if transcribe.IsEnglish(language) {
			language = "en"
		}
		txName, txModel = tx.Name(), tx.Model()
	}
	if cloud == nil {
		report(0.4, "writing down what's said, on this machine")
	} else {
		report(0.4, "taking the words the AI work wrote down")
	}
	// The spelling lists' words, for a transcriber that takes hints (D147).
	tr := cloud
	if h, ok := tx.(transcribe.Hinted); ok && tr == nil {
		if hints := transcripts.HintWords(ctx, a, as.ProjectIDs, 200); len(hints) > 0 {
			tr, err = h.TranscribeHinted(ctx, wav, dir, language, hints)
		}
	}
	if tr == nil && err == nil {
		tr, err = tx.Transcribe(ctx, wav, dir, language)
	}
	if err != nil {
		return nil, err
	}
	// Speakers come from diarization, the same way for every recognizer.
	hint := 0
	if as.Speakers != nil {
		hint = as.Speakers.Hint
	}
	var det *speakers.Detection
	prev := speakers.CapturePrevious(ctx, a, as.ID) // a transcript made again keeps the names people confirmed
	if len(tr.Segments) > 0 {
		tr.Segments, det = detectSpeakers(ctx, w, as, wav, tr.Segments, hint, report)
	}
	if det != nil {
		det.Previous = prev
	}
	// The transcript it had (and the fixes people made to it) are kept before
	// the new one replaces it; no fix is written meanwhile (D149).
	release, err := transcripts.Hold(ctx, a, as.ID)
	if err != nil {
		return nil, err
	}
	// A person who set another language while this ran asked for the words
	// in that language: these are dropped, and the job their choice queued
	// writes them (D243).
	if spoken != nil {
		if newer := personLanguageSince(ctx, a, as.ID, now, spoken); newer != nil {
			release()
			return map[string]any{"superseded": true, "language": spoken.Code, "personLanguage": newer.Code,
				"note": fmt.Sprintf("A person set “%s” to %s while it was being written down in %s; the words are written down again in %s.",
					as.DisplayTitle(), transcribe.SpokenLanguageName(newer.Code), transcribe.SpokenLanguageName(spoken.Code), transcribe.SpokenLanguageName(newer.Code))}, nil
		}
	}
	before, err := transcripts.BeforeTranscribing(ctx, a, as)
	if err != nil {
		release()
		return nil, err
	}
	words, spk, err := storeSegments(ctx, a, as, tr.Segments, txName, txModel)
	release()
	if err != nil {
		return nil, err
	}
	precision := "segment"
	if words > 0 {
		precision = "word"
	}
	st := models.TranscriptStatus{State: "available", Provider: txName, Model: txModel, Precision: precision, Language: tr.Language,
		Segments: len(tr.Segments), Words: words, Speakers: len(spk), UpdatedAt: time.Now().UTC()}
	if len(tr.Segments) == 0 {
		st.Precision = ""
	}
	set := bson.M{"transcript": st, "analysis.transcribe": "available", "updatedAt": time.Now().UTC()}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": set})
	if spoken != nil {
		// Never over a person's choice made after this job started.
		_, _ = a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": as.ID, "workspaceId": a.WorkspaceID(),
			"$or": bson.A{bson.M{"language.by": bson.M{"$ne": models.LanguagePerson}}, bson.M{"language.at": bson.M{"$lte": now}}}},
			bson.M{"$set": bson.M{"language": spoken}})
	}
	out := map[string]any{"segments": len(tr.Segments), "words": words, "language": tr.Language}
	if spoken != nil {
		out["languageBy"] = spoken.By
		if spoken.By == models.LanguageHeard {
			out["languageScore"] = spoken.Score
		}
		if spoken.Heard != "" {
			out["languageHeard"] = spoken.Heard
		}
	}
	if tr.Engine != "" {
		out["engine"] = tr.Engine // resident (model kept loaded) or per-batch (one program run per batch)
	}
	if det != nil {
		if res, err := speakers.StoreWith(ctx, a, as, det, w.Spk.Diarizer.Name()); err != nil {
			w.Log("storing speakers for %s failed: %v", as.ID, err)
		} else {
			out["voices"] = det.Result.Speakers()
			recognized(ctx, a, j, res)
		}
	}
	if tr.Hints > 0 {
		out["hints"] = tr.Hints
	}
	// People's fixes carry over to the new words, then the spelling lists
	// correct them (D147, D149).
	entries, err := transcripts.ListsFor(ctx, a, as.ProjectIDs)
	if err != nil {
		return nil, err
	}
	after, err := transcripts.AfterTranscribing(ctx, a, as, before, entries)
	if err != nil {
		return nil, err
	}
	if after.Spelled > 0 {
		out["spelled"] = after.Spelled
	}
	if c := after.Carry; c != nil {
		out["fixesCarried"], out["fixesMissed"] = c.Carried+c.Already, len(c.Missed)
		transcripts.NotifyMissed(ctx, a, as, c)
	}
	return out, nil
}
