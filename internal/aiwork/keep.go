package aiwork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// What comes back is kept with how it was made (D125, D226): a picture in the
// resources library, sound and dubs as clips in Sources, a transcript as the
// clip's words (through the ordinary transcribe job, which carries people's
// fixes over), a voice copy as a voice. Every file is checked (it must be
// what the kind makes) before it is kept.

// keep downloads one output and keeps it.
func (r *runner) keep(ctx context.Context, key string, t *aiservices.Ticket, o *aiservices.Output, i, n int) (*models.GenOutput, error) {
	g := r.g
	switch r.kind.Output {
	case aiservices.OutputVoice:
		if o.VoiceID == "" {
			return nil, &generate.Final{Err: apperr.New(apperr.Provider, "%s didn't say which voice it made.", g.ProviderTitle)}
		}
		return &models.GenOutput{VoiceID: o.VoiceID, Delivery: "voice", Status: "voice"}, nil
	case aiservices.OutputTranscript:
		return r.keepTranscript(ctx, o)
	case aiservices.OutputPresenter:
		if o.PresenterID == "" {
			return nil, &generate.Final{Err: apperr.New(apperr.Provider, "%s didn't say which presenter it made.", g.ProviderTitle)}
		}
		return &models.GenOutput{PresenterID: o.PresenterID, VoiceID: o.VoiceID, Delivery: "presenter", Status: "presenter"}, nil
	}
	r.say(StageDownloading, "Downloading it", 0.82)
	path, done, err := generate.Scratch(r.a, fmt.Sprintf("made-%d.bin", i+1))
	if err != nil {
		return nil, err
	}
	defer done()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	size, err := r.ad.Fetch(ctx, key, t, o, f)
	_ = f.Close()
	if err != nil {
		return nil, err // an expired file ends it; anything else is retried (the provider keeps it until MetaRush deletes it)
	}
	head := make([]byte, 64)
	if fh, err := os.Open(path); err == nil {
		k, _ := fh.Read(head)
		head = head[:k]
		_ = fh.Close()
	}
	sniffed := aiservices.Sniff(head)
	named := path[:len(path)-len(".bin")] + aiservices.ExtFor(firstNonEmpty(sniffed, o.MIME))
	if err := os.Rename(path, named); err != nil {
		return nil, err
	}
	r.say(StageAdding, "Adding it to MetaRush", 0.9)
	if k := keeperFor(g.Origin); k != nil && r.kind.Output != aiservices.OutputPicture {
		// Work another feature asked for keeps its own way (new words in a speaker's voice, D310).
		out, err := k(ctx, r.a, g, named, firstNonEmpty(sniffed, o.MIME))
		if err != nil {
			return nil, err
		}
		if out.Delivery == "" {
			out.Delivery = o.Delivery()
		}
		if out.ProviderFileID == "" {
			out.ProviderFileID = o.FileID
		}
		return out, nil
	}
	switch r.kind.Output {
	case aiservices.OutputPicture:
		if !strings.HasPrefix(sniffed, "image/") {
			return nil, &generate.Final{Err: apperr.New(apperr.Provider, "%s sent something that isn't a picture MetaRush can read.", g.ProviderTitle)}
		}
		return r.keepPicture(ctx, named, size, sniffed, o, i, n)
	}
	return r.keepClip(ctx, named, size, firstNonEmpty(sniffed, o.MIME), o, i, n)
}

// title names what was made: the words it was made from, or the clip it changed.
func (r *runner) title(i, n int) string {
	g := r.g
	t := generate.Clip(g.Prompt, 60)
	switch g.Kind {
	case aiservices.KindVoiceChange, aiservices.KindDubbing:
		src := r.sourceTitle()
		if g.Kind == aiservices.KindDubbing && g.Options != nil {
			t = fmt.Sprintf("%s (dubbed into %s)", src, languageName(g.Options.TargetLanguage))
		} else {
			t = src + " (another voice)"
		}
	case aiservices.KindVoice:
		t = "AI speech: " + generate.Clip(g.Prompt, 50)
	case aiservices.KindPresenter:
		t = fmt.Sprintf("%s reads “%s”", r.presenterTitle(), generate.Clip(r.sourceTitle(), 50))
	case aiservices.KindLipSync:
		t = r.sourceTitle() + " (lips matched to new sound)"
	case aiservices.KindBackground:
		t = r.sourceTitle() + " (no background)"
	case aiservices.KindUpscale:
		scale := 0
		if g.Options != nil {
			scale = g.Options.Scale
		}
		t = fmt.Sprintf("%s (%d× larger)", r.sourceTitle(), max(scale, 2))
	}
	if strings.TrimSpace(t) == "" {
		t = upperFirst(r.kind.What)
	}
	if n > 1 {
		t += fmt.Sprintf(" (%d of %d)", i+1, n)
	}
	return t
}

func (r *runner) sourceTitle() string {
	for _, ref := range r.g.References {
		if ref.ResourceID != "" {
			var res models.Resource
			if err := r.a.DB.GetByID(context.Background(), store.CResources, r.a.WorkspaceID(), ref.ResourceID, &res); err == nil {
				return res.Title
			}
		}
		if ref.AssetID != "" {
			var as models.Asset
			if err := r.a.DB.GetByID(context.Background(), store.CAssets, r.a.WorkspaceID(), ref.AssetID, &as); err == nil {
				return as.DisplayTitle()
			}
		}
	}
	return "A clip"
}

// keepPicture adds a picture to the resources library, marked as made by AI.
func (r *runner) keepPicture(ctx context.Context, path string, size int64, mime string, o *aiservices.Output, i, n int) (*models.GenOutput, error) {
	g := r.g
	sha, err := media.HashFile(path)
	if err != nil {
		return nil, err
	}
	src := models.ResourceSource{Kind: "generated", Tool: g.ProviderTitle + " · " + g.ModelTitle, At: time.Now().UTC(), Machine: r.a.Cfg.Installation.Name,
		MadeWith: &models.ResourceMadeWith{Kind: resources.MakeAI, GenerationID: g.ID, Provider: g.Provider, ProviderTitle: g.ProviderTitle, Model: g.Model,
			Prompt: generate.Clip(g.Prompt, 300), Likeness: len(g.ConsentIDs) > 0}}
	if m, err := aiservices.ModelFor(g.Provider, g.Kind, g.Model); err == nil {
		src.MadeWith.Watermark = m.Watermark
	}
	step := "changed by " + g.ProviderTitle
	if aiservices.OnePicture(g.Kind) {
		step = pictureStep(g)
	}
	for _, ref := range g.References {
		if ref.ResourceID != "" {
			src.Lineage = append(src.Lineage, models.ResourceLineage{ResourceID: ref.ResourceID, SHA256: ref.SHA256, Step: step})
		}
	}
	title := r.title(i, n)
	scope := "workspace"
	if g.ProjectID != "" {
		scope = "project"
	}
	tags := []string{models.TagAIGenerated}
	about := "Made by " + g.ProviderTitle + " from: “" + generate.Clip(g.Prompt, 200) + "”"
	if aiservices.OnePicture(g.Kind) {
		about = upperFirst(step) + ", from “" + r.sourceTitle() + "”."
		if r.kind.Edit {
			// The person's own picture with its background taken away: nothing
			// new was made, so it isn't marked as AI-generated (D279).
			tags, src.MadeWith.Edit = nil, true
		}
	}
	res, err := resources.AddFile(ctx, r.a, path, resources.AddOptions{Scope: scope, ProjectID: g.ProjectID, Title: title, Name: slug(title),
		Tags: tags, Description: about,
		Source: src, Fresh: true, FileName: slug(title) + aiservices.ExtFor(mime), Via: firstNonEmpty(g.CreatedBy.Via, "cli")})
	if err != nil {
		return nil, err
	}
	if res.Resource == nil {
		return nil, &generate.Final{Err: apperr.New(apperr.Provider, "The picture couldn't be added: %s", res.Message)}
	}
	return &models.GenOutput{ResourceID: res.Resource.ID, SHA256: sha, Size: size, MIME: mime, Delivery: o.Delivery(), ProviderFileID: o.FileID, Status: res.Status}, nil
}

// keepClip adds sound (or a dub) as an ordinary clip with where it came from.
func (r *runner) keepClip(ctx context.Context, path string, size int64, mime string, o *aiservices.Output, i, n int) (*models.GenOutput, error) {
	g := r.g
	mi, err := probe.Probe(ctx, r.a.Tools, path)
	if err != nil || !mi.HasAudio || mi.Duration < 0.05 {
		return nil, &generate.Final{Err: apperr.New(apperr.Provider, "%s sent something that isn't sound MetaRush can read.", g.ProviderTitle)}
	}
	if g.Kind == aiservices.KindPresenter {
		// The picture is the service's; the sound is MetaRush's own (D273).
		if own, err := r.withOwnSound(ctx, path); err == nil && own != path {
			path = own
			if mi, err = probe.Probe(ctx, r.a.Tools, path); err != nil {
				return nil, err
			}
			if st, err := os.Stat(path); err == nil {
				size = st.Size()
			}
		}
		if !mi.HasVideo {
			return nil, &generate.Final{Err: apperr.New(apperr.Provider, "%s sent sound without a picture.", g.ProviderTitle)}
		}
	}
	if g.Kind == aiservices.KindDubbing {
		// A dub of a clip with a picture keeps the picture: the dubbed sound is put under it.
		if merged, err := r.underPicture(ctx, path); err != nil {
			return nil, err
		} else if merged != "" {
			path, mime = merged, "video/mp4"
			if mi, err = probe.Probe(ctx, r.a.Tools, path); err != nil {
				return nil, err
			}
			if st, err := os.Stat(path); err == nil {
				size = st.Size()
			}
		}
	}
	sha, err := media.HashFile(path)
	if err != nil {
		return nil, err
	}
	title := r.title(i, n)
	src := &models.AssetSource{Platform: models.PlatformGenerated, Title: generate.Clip(firstNonEmpty(g.Prompt, title), 200), Duration: mi.Duration, DownloadedAt: time.Now().UTC(),
		Tool: g.ProviderTitle, ToolVersion: g.Model, Method: models.SourceGenerated,
		Generated: &models.GeneratedSource{GenerationID: g.ID, Kind: g.Kind, Provider: g.Provider, ProviderTitle: g.ProviderTitle, Model: g.Model, ModelTitle: g.ModelTitle,
			Operation: g.Operation, Take: g.Take, Prompt: generate.Clip(g.Prompt, 300), Likeness: len(g.ConsentIDs) > 0 || g.Kind == aiservices.KindLipSync}}
	if m, err := aiservices.ModelFor(g.Provider, g.Kind, g.Model); err == nil {
		src.Generated.Watermark = m.Watermark
	}
	if g.Kind == aiservices.KindPresenter {
		src.Generated.Presenter, src.Generated.PresenterTitle = r.presenterRef(), r.presenterTitle()
		src.Generated.Prompt = ""
		if len(g.ConsentIDs) > 0 {
			src.Generated.Consent = g.ConsentIDs[0]
		}
		src.Title = title
	}
	ext := aiservices.ExtFor(mime)
	if ext == ".bin" {
		ext = ".mp3"
	}
	res := ingest.File(ctx, r.a, path, ingest.Options{ProjectID: g.ProjectID, DisplayName: slug(title) + ext, Title: title, Source: src, JobID: r.job.ID,
		Via: firstNonEmpty(g.CreatedBy.Via, "cli"), Tags: []string{models.TagAIGenerated}, Transcribe: speaks(g.Kind), FootageOnly: true, Duplicates: ingest.DuplicatesKeep})
	switch res.Status {
	case "failed", "skipped":
		return nil, errors.New("adding it didn't work: " + res.Error)
	}
	id := res.AssetID
	if id == "" && res.Duplicate != nil {
		id = res.Duplicate.AssetID
	}
	return &models.GenOutput{AssetID: id, SHA256: sha, Size: size, Duration: mi.Duration, Width: mi.Width, Height: mi.Height, FPS: mi.FPS, HasAudio: mi.HasAudio,
		MIME: mime, Delivery: o.Delivery(), ProviderFileID: o.FileID, Language: o.Language, Status: res.Status}, nil
}

// speaks reports whether a kind makes speech (its words are written down).
func speaks(kind string) bool {
	return kind == aiservices.KindVoice || kind == aiservices.KindVoiceChange || kind == aiservices.KindDubbing || kind == aiservices.KindPresenter || kind == aiservices.KindLipSync
}

// presenterRef names who read a presenter clip: a person's presenter
// (prs_…) or a stock presenter ("stock:<id>").
func (r *runner) presenterRef() string {
	o := r.g.Options
	if o == nil {
		return ""
	}
	if o.Stock {
		return StockPrefix + o.Presenter
	}
	var pr models.Presenter
	if r.a.DB.C(store.CPresenters).FindOne(context.Background(), bson.M{"workspaceId": r.a.WorkspaceID(), "providerId": o.Presenter}).Decode(&pr) == nil {
		return pr.ID
	}
	return ""
}

// presenterTitle is how a presenter clip's presenter is named.
func (r *runner) presenterTitle() string {
	o := r.g.Options
	if o == nil {
		return "A presenter"
	}
	if o.Stock {
		return r.g.ProviderTitle + "'s stock presenter"
	}
	var pr models.Presenter
	if r.a.DB.C(store.CPresenters).FindOne(context.Background(), bson.M{"workspaceId": r.a.WorkspaceID(), "providerId": o.Presenter}).Decode(&pr) == nil {
		return pr.Title
	}
	return "A presenter"
}

// underPicture puts dubbed sound under the picture of the part it dubs ("" when the clip has no picture).
func (r *runner) underPicture(ctx context.Context, sound string) (string, error) {
	var ref *models.GenReference
	for i := range r.g.References {
		if r.g.References[i].AssetID != "" {
			ref = &r.g.References[i]
		}
	}
	if ref == nil {
		return "", nil
	}
	var as models.Asset
	if err := r.a.DB.GetByID(ctx, store.CAssets, r.a.WorkspaceID(), ref.AssetID, &as); err != nil || !as.Media.HasVideo {
		return "", nil
	}
	src, err := generate.Original(ctx, r.a, &as)
	if err != nil {
		return "", err
	}
	out := strings.TrimSuffix(sound, ".mp3") + "-dub.mp4"
	whole := ref.From <= 0.05 && ref.To >= as.Media.Duration-0.05
	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"}
	if whole {
		args = append(args, "-i", src, "-i", sound, "-map", "0:v:0", "-map", "1:a:0", "-c:v", "copy")
	} else {
		args = append(args, "-ss", ff(ref.From), "-t", ff(ref.To-ref.From), "-i", src, "-i", sound, "-map", "0:v:0", "-map", "1:a:0",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-pix_fmt", "yuv420p")
	}
	args = append(args, "-c:a", "aac", "-b:a", "192k", "-shortest", "-movflags", "+faststart", out)
	if _, err := r.a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
		return "", apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't put the dubbed sound under the picture of “%s”.", as.DisplayTitle())
	}
	return out, nil
}

// keepTranscript keeps the transcript as a file and has the clip's words
// replaced through the ordinary transcribe job (D228).
func (r *runner) keepTranscript(ctx context.Context, o *aiservices.Output) (*models.GenOutput, error) {
	g := r.g
	tr := o.Transcript
	if tr == nil || len(tr.Words) == 0 {
		return nil, &generate.Final{Err: apperr.New(apperr.Provider, "%s heard no words in the recording.", g.ProviderTitle)}
	}
	var ref *models.GenReference
	for i := range g.References {
		if g.References[i].AssetID != "" {
			ref = &g.References[i]
		}
	}
	if ref == nil {
		return nil, &generate.Final{Err: apperr.New(apperr.InvalidInput, "The transcript names no clip.")}
	}
	// Times are the clip's: the provider heard from the part's start.
	shifted := *tr
	shifted.Words = nil
	for _, w := range tr.Words {
		w.Start, w.End = w.Start+ref.From, w.End+ref.From
		shifted.Words = append(shifted.Words, w)
	}
	b, err := json.Marshal(shifted)
	if err != nil {
		return nil, err
	}
	path, done, err := generate.Scratch(r.a, "transcript.json")
	if err != nil {
		return nil, err
	}
	defer done()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	s, err := generate.Keep(ctx, r.a, path, "application/json")
	if err != nil {
		return nil, err
	}
	_, _, err = r.a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeTranscribe, IdempotencyKey: "transcribe:cloud:" + g.ID, AssetID: ref.AssetID, Priority: 6,
		ProjectID: g.ProjectID, Actor: r.a.Actor, Payload: map[string]any{"assetId": ref.AssetID, "generationId": g.ID, "reason": "cloud"}})
	if err != nil {
		return nil, err
	}
	_ = r.a.DB.Update(ctx, store.CAssets, r.a.WorkspaceID(), ref.AssetID, bson.M{"$set": bson.M{"analysis.transcribe": "queued"}})
	return &models.GenOutput{AssetID: ref.AssetID, StorageKey: s.Key, SHA256: s.SHA256, Size: s.Size, MIME: "application/json", Delivery: "transcript",
		Language: tr.Language, Words: len(tr.Words), Status: "transcript"}, nil
}

// slug is a short name made from a title: lower case, dashes.
func slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "ai-made"
	}
	return s
}

// languageName says a language code as people do (es → Spanish); the code when unknown.
func languageName(code string) string {
	names := map[string]string{"en": "English", "es": "Spanish", "fr": "French", "de": "German", "it": "Italian", "pt": "Portuguese", "nl": "Dutch",
		"pl": "Polish", "ja": "Japanese", "ko": "Korean", "zh": "Chinese", "hi": "Hindi", "ar": "Arabic", "ru": "Russian", "tr": "Turkish", "sv": "Swedish",
		"da": "Danish", "fi": "Finnish", "no": "Norwegian", "cs": "Czech", "uk": "Ukrainian", "id": "Indonesian", "vi": "Vietnamese", "th": "Thai"}
	base := strings.ToLower(strings.SplitN(strings.SplitN(code, "-", 2)[0], "_", 2)[0])
	if n, ok := names[base]; ok {
		return n
	}
	return code
}

// pictureStep says how a picture kind changed a picture, for its lineage.
func pictureStep(g *models.Generation) string {
	where := "by " + g.ProviderTitle + " (" + g.ModelTitle + ")"
	if onThisComputer(g) {
		where = "on this computer (" + g.ModelTitle + ")"
	}
	if g.Kind == aiservices.KindBackground {
		return "background taken away " + where
	}
	scale := 2
	if g.Options != nil && g.Options.Scale > 0 {
		scale = g.Options.Scale
	}
	return fmt.Sprintf("made %d× larger %s", scale, where)
}
