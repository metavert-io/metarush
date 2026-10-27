package remotion

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Sound in layers (D170): a composition rendered without its sound, and its
// <Audio> elements — Remotion's own record of where each plays, from which
// point of its file, at which level — brought into the cut as MetaRush's
// own sound: music and effects as the cut's tracks (levels, fades, music
// dipping under voices), voice lines as narration lines anchored to the clip
// (their words from the project's data), so the Sound and Voiceover tabs take
// over. The default stays the sound as the project made it.

// placementsOf turns a render's record of its sounds into placements, in
// seconds of the clip (a part of a composition starts at its first frame).
func placementsOf(ev runEvent, fps float64, frames []int) []models.RemotionSoundPlacement {
	if fps <= 0 {
		fps = 30
	}
	first, last := 0, math.MaxInt32
	if len(frames) == 2 {
		first, last = frames[0], frames[1]
	}
	out := []models.RemotionSoundPlacement{}
	for _, s := range ev.Sounds {
		if s.File == "" || len(s.Volumes) == 0 {
			continue
		}
		from, n := s.From, s.Frames
		start := s.Start
		rate := firstPositive(s.PlaybackRate, 1)
		vols := s.Volumes
		if from < first { // starts before the part rendered
			skip := first - from
			if skip >= n {
				continue
			}
			start += float64(skip) / fps * rate
			from, n = first, n-skip
			if len(vols) > 1 {
				vols = vols[min(skip, len(vols)-1):]
			}
		}
		if from > last {
			continue
		}
		if end := from + n - 1; end > last {
			n = last - from + 1
			if len(vols) > n {
				vols = vols[:n]
			}
		}
		hi := 0.0
		for _, x := range vols {
			hi = math.Max(hi, x)
		}
		if hi <= 0 {
			continue
		}
		out = append(out, models.RemotionSoundPlacement{File: s.File, From: round3(float64(from-first) / fps), Seconds: round3(float64(n) / fps), Start: round3(start),
			Rate: rate, Volumes: vols, FPS: fps, Video: s.Type == "video"})
	}
	return out
}

// layerTrack is one sound of a composition as the cut will have it.
type layerTrack struct {
	p       models.RemotionSoundPlacement
	role    string
	words   string
	assetID string
	length  float64 // the file's length (seconds)
}

var trackIDRe = regexp.MustCompile(`[^a-z0-9]+`)

// trackID makes a sound's name in the cut from its file (music-bed-teaser).
func trackID(prefix, file string, taken map[string]bool) string {
	base := strings.TrimSuffix(path.Base(file), path.Ext(file))
	id := prefix + "-" + strings.Trim(trackIDRe.ReplaceAllString(strings.ToLower(base), "-"), "-")
	if len(id) > 56 {
		id = strings.TrimRight(id[:56], "-")
	}
	out := id
	for i := 2; taken[out]; i++ {
		out = fmt.Sprintf("%s-%d", id, i)
	}
	taken[out] = true
	return out
}

// addClipLayers puts a composition rendered without its sound into a cut,
// with its sounds as the cut's own: the sound files are added to the project
// (each once), music and effects become tracks, voice lines narration lines.
func addClipLayers(ctx context.Context, a *app.App, s *models.RemotionSource, r *models.RemotionRender, ad models.RemotionAddTo) (*saved, *layersResult, error) {
	v, err := getVersion(ctx, a, r.Version)
	if err != nil {
		return nil, nil, err
	}
	m, err := loadManifest(ctx, a, v)
	if err != nil {
		return nil, nil, err
	}
	p, rev, err := latestRevision(ctx, a, ad.ProjectID, ad.Branch)
	if err != nil {
		return nil, nil, err
	}
	// The clip, like any composition (D135).
	in, out := 0.0, r.Duration
	if ad.In != nil {
		in = math.Max(0, math.Min(*ad.In, r.Duration))
	}
	if ad.Out != nil && *ad.Out > in {
		out = math.Min(*ad.Out, r.Duration)
	}
	taken := map[string]bool{}
	if rev != nil {
		for _, c := range rev.Spec.Clips {
			taken[c.ID] = true
		}
	}
	clipIDv := clipID(r.Composition, ad.ClipID, taken)
	clip := editspec.Clip{ID: clipIDv, AssetID: r.AssetID, In: round3(in), Out: round3(out), Label: r.Composition}
	var plan editspec.Plan
	instruction := fmt.Sprintf("Add “%s” from the Remotion project “%s” with its sound in layers", r.Composition, s.Title)
	if rev == nil {
		plan = editspec.Plan{ProjectID: p.ID, Instruction: instruction, Origin: "remotion:use", Spec: &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Output: outputFor(r), Clips: []editspec.Clip{clip}}}
	} else {
		after := ad.After
		if after == "" && len(rev.Spec.Clips) > 0 {
			after = rev.Spec.Clips[len(rev.Spec.Clips)-1].ID
		}
		plan = editspec.Plan{BaseRevisionID: rev.ID, Branch: rev.Branch, Instruction: instruction, Origin: "remotion:use", Ops: []editspec.Op{{Op: "insertClip", After: after, Clip: &clip}}}
	}
	// Where the clip starts in the cut (the sounds go where it plays).
	clipAt, err := clipStart(ctx, a, plan, clipIDv)
	if err != nil {
		return nil, nil, err
	}
	// The sounds that play in the part of the composition the clip shows.
	words := soundWords(ctx, a, v, m)
	var tracks []*layerTrack
	files := map[string]*layerTrack{}
	for _, pl := range r.Sounds {
		if pl.Video {
			continue // a video's own sound plays with its picture (it isn't a separate file)
		}
		end := pl.From + pl.Seconds
		if end <= in || pl.From >= out {
			continue
		}
		t := &layerTrack{p: pl, words: words[pl.File]}
		tracks = append(tracks, t)
		files[pl.File] = t
	}
	res := &layersResult{Composition: r.Composition}
	if len(tracks) == 0 {
		res.Message = "It has no sounds to bring in."
	}
	// Each sound file once, as a clip of the project (voices are transcribed).
	assets := map[string]string{}
	lengths := map[string]float64{}
	for _, t := range tracks {
		if _, ok := assets[t.p.File]; ok {
			continue
		}
		f, ok := m.File("public/" + t.p.File)
		if !ok {
			return nil, nil, apperr.New(apperr.NotFound, "The sound “%s” isn't among the project's files, so MetaRush can't bring it in.", t.p.File)
		}
		local, err := localObject(ctx, a, f.SHA256, f.Size)
		if err != nil {
			return nil, nil, err
		}
		// Voices are transcribed (their words show in the transcript); music and effects aren't.
		voice := t.words != "" || voicePath.MatchString(strings.ToLower(t.p.File))
		src := &models.AssetSource{Platform: models.PlatformRemotion, Method: "sound", Title: path.Base(t.p.File), DownloadedAt: time.Now().UTC(), Tool: "MetaRush",
			Remotion: &models.RemotionClipSource{SourceID: s.ID, SourceName: s.Name, SourceTitle: s.Title, Version: v.Hash, VersionNumber: v.Number, Kind: models.SourceRemotionSound,
				Composition: r.Composition, Path: "public/" + t.p.File, SoundOf: r.AssetID, Origin: s.Origin.Where()}}
		ing := ingest.File(ctx, a, local, ingest.Options{ProjectID: s.ProjectID, FootageOnly: true, Title: fmt.Sprintf("%s (sound of %s, %s)", path.Base(t.p.File), r.Composition, s.Title),
			DisplayName: path.Base(t.p.File), Source: src, Duplicates: ingest.DuplicatesKeep, Transcribe: voice, Via: a.Actor.Via})
		if ing.AssetID == "" || ing.Status == "failed" {
			return nil, nil, fmt.Errorf("adding the sound %s didn't work: %s", t.p.File, ing.Error)
		}
		assets[t.p.File], lengths[t.p.File] = ing.AssetID, ing.Duration
	}
	// The cut's own sound: tracks and narration lines.
	var eops []editspec.Op
	ids := map[string]bool{}
	beatIDs := map[string]bool{}
	if rev != nil {
		for _, x := range rev.Spec.Audio {
			ids[x.ID] = true
		}
		if rev.Spec.Voiceover != nil {
			for _, b := range rev.Spec.Voiceover.Beats {
				beatIDs[b.ID] = true
			}
		}
	}
	for _, t := range tracks {
		t.assetID, t.length = assets[t.p.File], lengths[t.p.File]
		// What it is, from how long it really plays (a cue that "plays until the
		// end" in the code stops when its file does).
		heard := t.p.Seconds
		if t.length > 0 {
			heard = math.Min(heard, math.Max(0, t.length-t.p.Start)/firstPositive(t.p.Rate, 1))
		}
		t.role = soundRole(t.p.File, t.words, heard, r.Duration)
		// The part of it the clip shows.
		from, start := t.p.From, t.p.Start
		rate := firstPositive(t.p.Rate, 1)
		dur := t.p.Seconds
		if from < in {
			start += (in - from) * rate
			dur -= in - from
			from = in
		}
		if from+dur > out {
			dur = out - from
		}
		playable := dur * rate
		if t.length > 0 {
			playable = math.Min(playable, t.length-start)
		}
		if playable <= 0.02 {
			continue
		}
		at := clipAt + (from - in)
		vol, shape := levelOf(t.p.Volumes, t.p.FPS)
		switch {
		case t.role == "voiceover" && t.words != "" && math.Abs(rate-1) < 1e-6:
			// A voice line with its words: a narration line, anchored to the clip.
			id := trackID("vo", t.p.File, beatIDs)
			off := round3(from - in)
			anchor := editspec.Anchor{Kind: editspec.AnchorClip, ClipID: clipIDv, Edge: "start", Offset: off}
			if off > 60 {
				x := round3(at)
				anchor = editspec.Anchor{Kind: editspec.AnchorTime, At: &x}
			}
			b := &editspec.Beat{ID: id, Text: clipText(t.words, 2000), Anchor: anchor, State: "imported",
				Take:      &editspec.TakeRef{AssetID: t.assetID, In: round3(start), Out: round3(start + playable), Source: "imported"},
				Rationale: fmt.Sprintf("The project's own voice line (%s), brought in with its sound in layers.", t.p.File)}
			if g := gainDB(vol); g != 0 {
				b.GainDB = &g
			}
			eops = append(eops, editspec.Op{Op: "addBeat", Beat: b})
			res.Voices++
		default:
			role := map[string]string{"music": editspec.RoleMusic, "voiceover": editspec.RoleVoiceover}[t.role]
			prefix := map[string]string{"music": "music", "voiceover": "voice"}[t.role]
			if role == "" {
				role, prefix = editspec.RoleSFX, "sfx"
			}
			tr := &editspec.AudioTrack{ID: trackID(prefix, t.p.File, ids), AssetID: t.assetID, Role: role, In: round3(start), Out: round3(start + playable), At: round3(at),
				Rationale: fmt.Sprintf("From the Remotion project “%s” (%s), brought in with its sound in layers.", s.Title, t.p.File)}
			if g := gainDB(vol); g != 0 {
				tr.GainDB = &g
			}
			tr.FadeIn, tr.FadeOut = math.Min(10, shape.fadeIn), math.Min(10, shape.fadeOut)
			if shape.dips > 0 && t.role == "music" {
				// It dipped under the voice: MetaRush's mix does that now.
				yes := true
				tr.Duck = &yes
				if shape.dipTo > 0 && shape.dipTo < 1 {
					d := math.Max(-30, math.Min(0, round1(20*math.Log10(shape.dipTo))))
					tr.DuckDB = &d
				}
			}
			eops = append(eops, editspec.Op{Op: "addAudio", Track: tr})
			switch t.role {
			case "music":
				res.Music++
			case "voiceover":
				res.Voices++
			default:
				res.Effects++
			}
		}
	}
	if rev == nil {
		plan.Spec.Clips = []editspec.Clip{clip}
		sp, _, err := editspec.ApplyOps(plan.Spec, eops, false)
		if err != nil {
			return nil, nil, err
		}
		plan.Spec = sp
	} else {
		plan.Ops = append(plan.Ops, eops...)
	}
	sv, err := savePlan(ctx, a, plan)
	if err != nil {
		return nil, nil, err
	}
	res.Message = layersMessage(res)
	return sv, res, nil
}

// layersResult says what came in.
type layersResult struct {
	Composition string
	Music       int
	Voices      int
	Effects     int
	Message     string
}

func layersMessage(r *layersResult) string {
	var parts []string
	if r.Music > 0 {
		parts = append(parts, plural(r.Music, "music track", "music tracks"))
	}
	if r.Voices > 0 {
		parts = append(parts, plural(r.Voices, "voice line", "voice lines"))
	}
	if r.Effects > 0 {
		parts = append(parts, plural(r.Effects, "sound effect", "sound effects"))
	}
	if len(parts) == 0 {
		return "It has no sounds of its own, so only its picture came in."
	}
	return "Its sound came in as the cut's own: " + joinAnd(parts) + "."
}

// clipStart is where a clip starts in the cut a plan makes (seconds).
func clipStart(ctx context.Context, a *app.App, plan editspec.Plan, id string) (float64, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return 0, err
	}
	out, err := ops.CallTyped[ops.EditPreviewOut](ctx, a, "edit.preview", ops.EditPreviewIn{Plan: raw}, ops.Caller{Human: a.Actor.Via != models.ViaAssistant})
	if err != nil {
		return 0, err
	}
	if out.RenderPlan == nil || out.RenderPlan.FPS <= 0 {
		msg := "the clip can't go into the cut"
		if len(out.Errors) > 0 {
			msg = out.Errors[0].Message
		}
		return 0, apperr.New(apperr.Validation, "%s", upperFirst(strings.TrimSuffix(msg, "."))+".")
	}
	for _, c := range out.RenderPlan.Clips {
		if c.ID == id {
			return round3(float64(c.From) / float64(out.RenderPlan.FPS)), nil
		}
	}
	return 0, apperr.New(apperr.Internal, "the new clip isn't in the cut it makes")
}

// soundWords finds the words of the project's voice lines in its data files:
// an object naming the sound file with its text ({text, file}).
func soundWords(ctx context.Context, a *app.App, v *models.RemotionVersion, m *Manifest) map[string]string {
	out := map[string]string{}
	if v.Map == nil || v.Map.State != models.RemotionMapDone {
		// Without a map, read the data files the project has (JSON, small).
		for _, f := range m.Files {
			if isPublic(f.Path) || !strings.HasSuffix(f.Path, ".json") || f.Size > 4<<20 || strings.HasSuffix(f.Path, "package.json") || strings.HasSuffix(f.Path, "package-lock.json") || strings.HasSuffix(f.Path, "tsconfig.json") {
				continue
			}
			b, err := readObject(ctx, a, f.SHA256, f.Size)
			if err != nil {
				continue
			}
			var doc any
			if json.Unmarshal(b, &doc) == nil {
				collectTexts(doc, out, 0)
			}
		}
		return out
	}
	if doc, err := loadMap(ctx, a, v); err == nil {
		for _, c := range doc.Compositions {
			for _, s := range c.Sounds {
				if s.Words != "" {
					out[s.File] = s.Words
				}
			}
		}
	}
	return out
}

var soundFile = regexp.MustCompile(`(?i)\.(mp3|wav|m4a|aac|ogg|flac)$`)

func collectTexts(v any, out map[string]string, depth int) {
	if depth > 12 {
		return
	}
	switch x := v.(type) {
	case []any:
		for _, y := range x {
			collectTexts(y, out, depth+1)
		}
	case map[string]any:
		var file, text string
		for k, y := range x {
			s, ok := y.(string)
			if !ok {
				continue
			}
			if soundFile.MatchString(s) {
				file = strings.TrimLeft(s, "/")
			}
			switch strings.ToLower(k) {
			case "text", "line", "words", "caption", "title", "script", "transcript":
				text = s
			}
		}
		if file != "" && text != "" {
			out[file] = text
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			collectTexts(x[k], out, depth+1)
		}
	}
}

// levelOf reads a sound's level: its highest, and the shape when it changes.
func levelOf(v []float64, fps float64) (float64, shape) {
	hi := 0.0
	for _, x := range v {
		hi = math.Max(hi, x)
	}
	if len(v) < 3 {
		return hi, shape{}
	}
	return hi, volumeShape(v, fps)
}

// gainDB is a level as decibels (0 for 1, within the track's limits).
func gainDB(vol float64) float64 {
	if vol <= 0 {
		return -60
	}
	g := round1(20 * math.Log10(vol))
	if math.Abs(g) < 0.05 {
		return 0
	}
	return math.Max(-30, math.Min(12, g))
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

var _ = store.CAssets
