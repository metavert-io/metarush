package fixes

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/oldnames"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// CleanVoices is the Sound tab's clean engine (DeepFilterNet and the measured
// polish, D89) on a whole recording; internal/sound sets it. in is a WAV;
// out gets a mono 48 kHz WAV lined up with it. engine names what ran.
var CleanVoices func(ctx context.Context, a *app.App, in, out string, progress func(string)) (engine string, err error)

// Report says how a job goes (a fraction and plain words).
type Report func(pct float64, msg string)

// Prepare is the prepare job: it makes a fixed copy, measures it, and
// switches the source to it when its ticked fixes still ask for it.
func Prepare(ctx context.Context, a *app.App, j *models.Job, report Report) (map[string]any, error) {
	if rid, _ := j.Payload["resourceId"].(string); rid != "" {
		return preparePicture(ctx, a, j, report)
	}
	assetID, _ := j.Payload["assetId"].(string)
	copyID, _ := j.Payload["copyId"].(string)
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return nil, Final(fmt.Errorf("clip %s not found", assetID))
	}
	var c models.SourceCopy
	if err := a.DB.GetByID(ctx, store.CSourceCopies, a.WorkspaceID(), copyID, &c); err != nil {
		return nil, Final(fmt.Errorf("fixed copy %s not found", copyID))
	}
	if c.State == models.FixReady {
		return map[string]any{"reused": true, "sha256": c.SHA256}, switchTo(ctx, a, as.ID, &c)
	}
	res, err := makeCopy(ctx, a, j, &as, &c, report)
	if err != nil {
		var fe *FinalError
		last := errors.As(err, &fe) || j.AttemptCount >= j.MaxAttempts
		if last {
			failCopy(context.WithoutCancel(ctx), a, &as, &c, err)
		}
		return nil, err
	}
	return res, nil
}

// failCopy records that a copy couldn't be made, in words.
func failCopy(ctx context.Context, a *app.App, as *models.Asset, c *models.SourceCopy, cause error) {
	msg := plainFailure(cause)
	now := time.Now().UTC()
	_ = a.DB.Update(ctx, store.CSourceCopies, a.WorkspaceID(), c.ID, bson.M{"$set": bson.M{"state": models.FixFailed, "error": msg, "updatedAt": now}})
	filter := oldnames.FingerprintIs(c.Fingerprint)
	filter["_id"], filter["workspaceId"] = as.ID, a.WorkspaceID()
	_, _ = a.DB.C(store.CAssets).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"fixes.state": models.FixFailed, "fixes.error": msg, "updatedAt": now}})
}

func plainFailure(err error) string {
	var fe *FinalError
	if errors.As(err, &fe) {
		return capFirst(strings.TrimSuffix(fe.Err.Error(), ".")) + "."
	}
	return "Something went wrong while making it; untick and tick the fixes to try again."
}

func makeCopy(ctx context.Context, a *app.App, j *models.Job, as *models.Asset, c *models.SourceCopy, report Report) (map[string]any, error) {
	start := time.Now()
	dir := filepath.Join(a.Cfg.Scratch.Dir, "fix-"+j.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	report(0.02, "getting the original")
	orig, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
	if err != nil {
		return nil, err
	}
	// Variable frame rate video is fixed from its steady copy, so the fixed
	// copy plays at a constant rate with the same timing.
	in := orig
	if v, ok := as.OriginalVariant(models.VariantMezzanine); ok && as.Media.VFR && v.StorageKey != "" {
		if p, err := a.Cache.Ensure(ctx, a.Media, v.StorageKey, v.SHA256, v.Size, nil); err == nil {
			in = p
		}
	}
	steps := stepsOf(c)
	var picture, sound []string
	for _, id := range steps {
		f, _ := models.Fix(id)
		switch f.Affects {
		case models.AffectsPicture:
			picture = append(picture, id)
		case models.AffectsSound:
			sound = append(sound, id)
		}
	}
	made := c.FixSteps
	made.Steps = nil
	var notes []string
	vOut, aOut := "", ""
	if len(picture) > 0 && as.Media.HasVideo {
		out, st, n, err := processVideo(ctx, a, as, in, dir, picture, func(p float64, m string) { report(0.05+0.55*p, m) })
		if err != nil {
			return nil, err
		}
		vOut, notes = out, append(notes, n...)
		made.Steps = append(made.Steps, st...)
	}
	if len(sound) > 0 && as.Media.HasAudio {
		out, st, n, err := processSound(ctx, a, as, in, dir, sound, func(p float64, m string) { report(0.6+0.2*p, m) })
		if err != nil {
			return nil, err
		}
		aOut, notes = out, append(notes, n...)
		made.Steps = append(made.Steps, st...)
	}
	report(0.8, "putting the copy together")
	final, mime, err := mux(ctx, a, as, in, vOut, aOut, dir)
	if err != nil {
		return nil, err
	}
	mi, err := probe.Probe(ctx, a.Tools, final)
	if err != nil {
		return nil, err
	}
	key, sha, size, err := publish(ctx, a, final, mime)
	if err != nil {
		return nil, err
	}
	report(0.84, "making its preview copy")
	variants, err := copyVariants(ctx, a, final, dir, mi)
	if err != nil {
		return nil, err
	}
	report(0.9, "measuring the copy")
	if err := copyEvidence(ctx, a, as, sha, final, variants, dir, mi); err != nil {
		return nil, err
	}
	measures, err := beforeAfter(ctx, a, as, orig, final, steps)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	set := bson.M{"state": models.FixReady, "error": "", "sha256": sha, "storageKey": key, "size": size, "mime": mime, "media": mi, "variants": variants,
		"measures": measures, "notes": notes, "fingerprint": c.Fingerprint, "fixSteps": made, "jobId": j.ID, "madeOn": a.Cfg.Installation.ID, "took": round(time.Since(start).Seconds(), 1), "updatedAt": now}
	if err := a.DB.Update(ctx, store.CSourceCopies, a.WorkspaceID(), c.ID, bson.M{"$set": set, "$unset": bson.M{oldnames.Fingerprint: "", oldnames.FixSteps: ""}}); err != nil {
		return nil, err
	}
	var done models.SourceCopy
	if err := a.DB.GetByID(ctx, store.CSourceCopies, a.WorkspaceID(), c.ID, &done); err != nil {
		return nil, err
	}
	report(0.97, "switching to the fixed copy")
	if err := switchTo(ctx, a, as.ID, &done); err != nil {
		return nil, err
	}
	return map[string]any{"sha256": sha, "size": size, "fixes": steps, "notes": notes}, nil
}

// ---- the picture ----

// processVideo applies the picture fixes in their order: blockiness, grain,
// steadying, colour. It returns the picture-only file and what ran.
func processVideo(ctx context.Context, a *app.App, as *models.Asset, in, dir string, fixes []string, report Report) (string, []models.FixStep, []string, error) {
	var pre, post []string
	var steps []models.FixStep
	var notes []string
	ffv := a.Tools.Version(ctx, tools.FFmpeg)
	dur := as.Media.Duration
	if slices.Contains(fixes, models.FixBlocks) {
		b, err := Blockiness(ctx, a.Tools, in, dur)
		if err != nil {
			return "", nil, nil, err
		}
		strength := "weak"
		if b >= 1.45 {
			strength = "strong"
		}
		pre = append(pre, "deblock=filter="+strength+":block=8")
		steps = append(steps, models.FixStep{Fix: models.FixBlocks, Engine: "deblock", Params: map[string]string{"filter": strength, "measured": fmt.Sprintf("%.2f", b)}, Tool: "ffmpeg " + ffv})
	}
	if slices.Contains(fixes, models.FixGrain) {
		g, err := Grain(ctx, a.Tools, in, dur)
		if err != nil {
			return "", nil, nil, err
		}
		ls := math.Max(1.5, math.Min(6, g*0.9))
		cs, lt, ct := ls*0.75, ls*1.5, ls*1.1
		f := fmt.Sprintf("hqdn3d=%.1f:%.1f:%.1f:%.1f", ls, cs, lt, ct)
		pre = append(pre, f)
		steps = append(steps, models.FixStep{Fix: models.FixGrain, Engine: "hqdn3d", Params: map[string]string{"filter": f, "measured": fmt.Sprintf("%.2f", g)}, Tool: "ffmpeg " + ffv})
	}
	if slices.Contains(fixes, models.FixColour) {
		c, err := Colour(ctx, a.Tools, in, dur)
		if err != nil {
			return "", nil, nil, err
		}
		f := colourFilter(c)
		if f != "" {
			post = append(post, f)
		}
		steps = append(steps, models.FixStep{Fix: models.FixColour, Engine: "colorlevels", Params: map[string]string{"filter": f, "cast": fmt.Sprintf("%.3f", c.Cast), "brightness": fmt.Sprintf("%.3f", c.Brightness)}, Tool: "ffmpeg " + ffv})
	}
	enc := videoEncoder(as)
	out := filepath.Join(dir, "picture.mp4")
	fps := as.Media.FPS
	if fps <= 0 || fps > 120 {
		fps = 30
	}
	chain := func(mid ...string) string {
		parts := append(append(append([]string{}, pre...), mid...), post...)
		if len(parts) == 0 {
			return "null"
		}
		return strings.Join(parts, ",")
	}
	if slices.Contains(fixes, models.FixSteady) {
		stab := &Stabilizer{Home: a.Cfg.Home(), Tools: a.Tools}
		st, serr := stab.Ensure(ctx, func(m string) { report(0.02, m) })
		if kindOf(ctx, a, as) == models.SourceCall {
			// A call recording (D190): each stretch between the picture's switches
			// is steadied on its own (one with no switches is one stretch).
			if mi, err := probe.Probe(ctx, a.Tools, in); err == nil && mi.FPS > 0 && mi.FPS <= 120 {
				fps = mi.FPS
			}
			sw, ver, err := switchFrames(ctx, a, as, in, dir, fps)
			if err != nil {
				return "", nil, nil, fmt.Errorf("finding where the picture switches failed: %w", err)
			}
			if len(sw) > 0 {
				return steadyCall(ctx, a, as, in, dir, fps, sw, ver, st, serr, chain, enc, out, steps, notes, report)
			}
		}
		if st.Ready {
			// Pass 1: how the camera moved (vid.stab's motion, through the steps before it).
			report(0.05, "steadying the camera: following how it moves")
			detect := chain("vidstabdetect=shakiness=8:accuracy=15:stepsize=6:mincontrast=0.25:result=motion.trf")
			if err := runFF(ctx, a.Tools, tools.Stabilizer, dir, dur, func(p float64) { report(0.05+0.4*p, "steadying the camera: following how it moves") },
				"-i", in, "-an", "-vf", detect, "-f", "null", "-"); err != nil {
				return "", nil, nil, fmt.Errorf("steadying (finding the motion) failed: %w", err)
			}
			smoothing := int(math.Round(fps))
			transform := fmt.Sprintf("vidstabtransform=input=motion.trf:smoothing=%d:optzoom=1:interpol=bicubic,unsharp=5:5:0.6:3:3:0.3", smoothing)
			report(0.45, "steadying the camera: making the steady picture")
			args := append([]string{"-i", in, "-an", "-vf", chain(transform)}, enc...)
			if err := runFF(ctx, a.Tools, tools.Stabilizer, dir, dur, func(p float64) { report(0.45+0.55*p, "steadying the camera") }, append(args, out)...); err != nil {
				return "", nil, nil, fmt.Errorf("steadying failed: %w", err)
			}
			steps = append(steps, models.FixStep{Fix: models.FixSteady, Engine: EngineVidStab, Params: map[string]string{"shakiness": "8", "accuracy": "15", "smoothing": strconv.Itoa(smoothing), "zoom": "optimal", "interpolation": "bicubic"},
				Tool: "ffmpeg (" + st.Source + ") " + a.Tools.Version(ctx, tools.Stabilizer)})
			return out, steps, notes, nil
		}
		why := "the better steadying isn't set up on this machine"
		if serr != nil {
			why = serr.Error()
		}
		notes = append(notes, "Steadied with ffmpeg's basic method: "+why+". Make it again once the better steadying is set up.")
		args := append([]string{"-i", in, "-an", "-vf", chain("deshake=rx=48:ry=48")}, enc...)
		if err := runFF(ctx, a.Tools, tools.FFmpeg, dir, dur, func(p float64) { report(p, "steadying the camera (basic)") }, append(args, out)...); err != nil {
			return "", nil, nil, fmt.Errorf("steadying failed: %w", err)
		}
		steps = append(steps, models.FixStep{Fix: models.FixSteady, Engine: EngineDeshake, Params: map[string]string{"rx": "48", "ry": "48"}, Tool: "ffmpeg " + ffv})
		return out, steps, notes, nil
	}
	args := append([]string{"-i", in, "-an", "-vf", chain()}, enc...)
	if err := runFF(ctx, a.Tools, tools.FFmpeg, dir, dur, func(p float64) { report(p, "fixing the picture") }, append(args, out)...); err != nil {
		return "", nil, nil, fmt.Errorf("fixing the picture failed: %w", err)
	}
	return out, steps, notes, nil
}

// steadyCall steadies a call recording one stretch at a time (D190): the
// stretches between its switches (sw, frame numbers of in), each with its own
// motion, path and zoom, put back together frame for frame.
func steadyCall(ctx context.Context, a *app.App, as *models.Asset, in, dir string, fps float64, sw []int, switchesVersion string, st StabStatus, serr error,
	chain func(...string) string, enc []string, out string, steps []models.FixStep, notes []string, report Report) (string, []models.FixStep, []string, error) {
	parts := planStretches(sw)
	total := int(math.Round(as.Media.Duration * fps))
	run := stretchRun{keep: chain()}
	smoothing := int(math.Round(fps))
	step := models.FixStep{Fix: models.FixSteady}
	if st.Ready {
		run.tool = tools.Stabilizer
		run.detect = func(motion string) string {
			return chain("vidstabdetect=shakiness=8:accuracy=15:stepsize=6:mincontrast=0.25:result=" + motion)
		}
		run.steady = func(motion string) string {
			return chain(fmt.Sprintf("vidstabtransform=input=%s:smoothing=%d:optzoom=1:interpol=bicubic,unsharp=5:5:0.6:3:3:0.3", motion, smoothing))
		}
		step.Engine = EngineVidStab
		step.Params = map[string]string{"shakiness": "8", "accuracy": "15", "smoothing": strconv.Itoa(smoothing), "zoom": "optimal", "interpolation": "bicubic"}
		step.Tool = "ffmpeg (" + st.Source + ") " + a.Tools.Version(ctx, tools.Stabilizer)
	} else {
		why := "the better steadying isn't set up on this machine"
		if serr != nil {
			why = serr.Error()
		}
		notes = append(notes, "Steadied with ffmpeg's basic method: "+why+". Make it again once the better steadying is set up.")
		run.tool = tools.FFmpeg
		run.steady = func(string) string { return chain("deshake=rx=48:ry=48") }
		step.Engine = EngineDeshake
		step.Params = map[string]string{"rx": "48", "ry": "48"}
		step.Tool = "ffmpeg " + a.Tools.Version(ctx, tools.FFmpeg)
	}
	report(0.05, "steadying the camera, one picture at a time")
	n, err := steadyByStretch(ctx, a.Tools, in, dir, fps, total, parts, run, rawPix(enc), enc, out, func(p float64, m string) { report(0.05+0.95*p, m) })
	if err != nil {
		return "", nil, nil, err
	}
	step.Params["switches"] = strconv.Itoa(len(sw))
	step.Params["stretches"] = strconv.Itoa(n)
	if switchesVersion != "" {
		step.Params["switchesFrom"] = switchesVersion
	}
	notes = append(notes, fmt.Sprintf("The picture switches %s: each stretch between switches was steadied on its own.", timesWords(len(sw))))
	return out, append(steps, step), notes, nil
}

// timesWords says how many times: "once", "twice", "12 times".
func timesWords(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

// colourFilter balances colour and brightness from the measurements: each
// channel's levels from its darkest and brightest 0.5% (limited, so a dark
// scene stays dark), then a gentle brightness correction.
func colourFilter(c *ColourFacts) string {
	var lo, hi [3]float64
	for i := 0; i < 3; i++ {
		lo[i] = math.Min(c.Low[i], 0.12)  // lift at most 12% of black
		hi[i] = math.Max(c.High[i], 0.70) // stretch at most from 70% white
		if hi[i]-lo[i] < 0.3 {
			hi[i] = lo[i] + 0.3
		}
	}
	parts := []string{fmt.Sprintf("colorlevels=rimin=%.3f:gimin=%.3f:bimin=%.3f:rimax=%.3f:gimax=%.3f:bimax=%.3f", lo[0], lo[1], lo[2], hi[0], hi[1], hi[2])}
	// The brightness after the levels, from the mid-tone averages.
	var m float64
	for i := 0; i < 3; i++ {
		m += (c.Mean[i] - lo[i]) / (hi[i] - lo[i])
	}
	m /= 3
	// ffmpeg's eq raises each level to 1/gamma (over 1 brightens): the gamma
	// that moves m to the target is log(m)/log(target).
	switch {
	case m > 0.02 && m < 0.40:
		g := math.Min(1.6, math.Log(m)/math.Log(0.45))
		parts = append(parts, fmt.Sprintf("eq=gamma=%.3f", g))
	case m > 0.62 && m < 0.98:
		g := math.Max(0.75, math.Log(m)/math.Log(0.55))
		parts = append(parts, fmt.Sprintf("eq=gamma=%.3f", g))
	}
	return strings.Join(parts, ",")
}

// videoEncoder keeps the picture's depth: 10-bit HEVC with the source's
// colour description for HDR or 10-bit video, else high-quality H.264.
func videoEncoder(as *models.Asset) []string {
	var vs *models.Stream
	for i := range as.Media.Streams {
		if as.Media.Streams[i].Type == "video" {
			vs = &as.Media.Streams[i]
			break
		}
	}
	tenBit := as.Media.HDR || (vs != nil && strings.Contains(vs.PixFmt, "10"))
	if tenBit {
		args := []string{"-c:v", "libx265", "-preset", "medium", "-crf", "17", "-pix_fmt", "yuv420p10le", "-tag:v", "hvc1", "-fps_mode", "passthrough"}
		if vs != nil {
			if vs.ColorPrim != "" {
				args = append(args, "-color_primaries", vs.ColorPrim)
			}
			if vs.ColorTRC != "" {
				args = append(args, "-color_trc", vs.ColorTRC)
			}
			if vs.ColorSpace != "" {
				args = append(args, "-colorspace", vs.ColorSpace)
			}
		}
		return args
	}
	return []string{"-c:v", "libx264", "-preset", "medium", "-crf", "16", "-pix_fmt", "yuv420p", "-fps_mode", "passthrough"}
}

// ---- the sound ----

// processSound applies the sound fixes in their order: hum, voices, each
// person's volume, loudness. It returns a WAV (48 kHz) and what ran.
func processSound(ctx context.Context, a *app.App, as *models.Asset, in, dir string, fixes []string, report Report) (string, []models.FixStep, []string, error) {
	var steps []models.FixStep
	var notes []string
	ffv := a.Tools.Version(ctx, tools.FFmpeg)
	ch := 1
	for _, s := range as.Media.Streams {
		if s.Type == "audio" && s.Channels >= 2 {
			ch = 2
			break
		}
	}
	cur := filepath.Join(dir, "sound-0.wav")
	report(0.02, "getting the sound")
	if err := ff(ctx, a.Tools, "-i", in, "-vn", "-map", "0:a:0", "-ac", strconv.Itoa(ch), "-ar", "48000", "-c:a", "pcm_f32le", cur); err != nil {
		return "", nil, nil, err
	}
	n := 0
	next := func() string { n++; return filepath.Join(dir, fmt.Sprintf("sound-%d.wav", n)) }
	if slices.Contains(fixes, models.FixHum) {
		report(0.05, "removing the hum")
		db, f0, err := Hum(ctx, a.Tools, cur)
		if err != nil {
			return "", nil, nil, err
		}
		if f0 == 0 {
			f0 = 60
		}
		var notches []string
		for k := 1; k <= 8; k++ {
			notches = append(notches, fmt.Sprintf("bandreject=f=%d:width_type=q:w=30", f0*k))
		}
		out := next()
		if err := ff(ctx, a.Tools, "-i", cur, "-af", strings.Join(notches, ","), "-c:a", "pcm_f32le", out); err != nil {
			return "", nil, nil, err
		}
		cur = out
		steps = append(steps, models.FixStep{Fix: models.FixHum, Engine: "notches", Params: map[string]string{"frequency": strconv.Itoa(f0), "harmonics": "8", "measured": fmt.Sprintf("%.1f", db)}, Tool: "ffmpeg " + ffv})
	}
	if slices.Contains(fixes, models.FixVoices) {
		report(0.15, "cleaning up the voices")
		if CleanVoices == nil {
			return "", nil, nil, Final(errors.New("cleaning up voices isn't available on this machine"))
		}
		out := next()
		engine, err := CleanVoices(ctx, a, cur, out, func(m string) { report(0.2, m) })
		if err != nil {
			return "", nil, nil, fmt.Errorf("cleaning up the voices failed: %w", err)
		}
		cur = out
		steps = append(steps, models.FixStep{Fix: models.FixVoices, Engine: "clean", Tool: engine})
	}
	if slices.Contains(fixes, models.FixVolumes) {
		report(0.55, "evening out the voices")
		spans, _ := speechSpans(ctx, a, as)
		sf, err := Sound(ctx, a.Tools, cur, spans)
		if err != nil {
			return "", nil, nil, err
		}
		out := next()
		if len(sf.Speakers) >= 2 {
			gains := speakerGains(sf)
			if err := applyGains(ctx, a.Tools, cur, out, spans, gains); err != nil {
				return "", nil, nil, err
			}
			p := map[string]string{}
			for who, g := range gains {
				p[who] = fmt.Sprintf("%+.1f dB", g)
			}
			steps = append(steps, models.FixStep{Fix: models.FixVolumes, Engine: "per-person gain", Params: p, Tool: "metarush"})
		} else {
			if err := ff(ctx, a.Tools, "-i", cur, "-af", "speechnorm=e=12.5:r=0.0001:l=1", "-c:a", "pcm_f32le", out); err != nil {
				return "", nil, nil, err
			}
			steps = append(steps, models.FixStep{Fix: models.FixVolumes, Engine: "speechnorm", Params: map[string]string{"filter": "speechnorm=e=12.5:r=0.0001:l=1"}, Tool: "ffmpeg " + ffv})
			notes = append(notes, "MetaRush didn't know who speaks when yet, so it evened out the speech as a whole.")
		}
		cur = out
	}
	if slices.Contains(fixes, models.FixLoudness) {
		report(0.75, "evening out the loudness")
		music := kindOf(ctx, a, as) == models.SourceMusic
		target, lra, tp := "-16", "11", "-1.5"
		pre := "dynaudnorm=f=400:g=31:p=0.95:m=8,"
		if music {
			target, lra, tp, pre = "-14", "14", "-1", ""
		}
		out := next()
		params, err := loudnorm(ctx, a.Tools, cur, out, pre, target, lra, tp)
		if err != nil {
			return "", nil, nil, err
		}
		steps = append(steps, models.FixStep{Fix: models.FixLoudness, Engine: "loudnorm", Params: params, Tool: "ffmpeg " + ffv})
		cur = out
	}
	return cur, steps, notes, nil
}

// speakerGains brings each person to the level of the middle one (at most
// 12 dB up or down).
func speakerGains(sf *SoundFacts) map[string]float64 {
	var levels []float64
	for _, l := range sf.Speakers {
		levels = append(levels, l)
	}
	slices.Sort(levels)
	target := levels[len(levels)/2]
	if len(levels)%2 == 0 {
		target = (levels[len(levels)/2-1] + levels[len(levels)/2]) / 2
	}
	out := map[string]float64{}
	for who, l := range sf.Speakers {
		out[who] = math.Max(-12, math.Min(12, target-l))
	}
	return out
}

// applyGains streams a WAV through a gain envelope: each person's gain over
// their stretches, moving over 80 ms between people, holding between them.
func applyGains(ctx context.Context, ts *tools.Set, in, out string, spans []Span, gains map[string]float64) error {
	const rate = 48000
	var ch int
	res, err := ts.Run(tools.Quiet(ctx), tools.Spec{Tool: tools.FFprobe, Args: []string{"-v", "error", "-select_streams", "a:0", "-show_entries", "stream=channels", "-of", "csv=p=0", in}})
	if err != nil {
		return err
	}
	ch, _ = strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if ch < 1 {
		ch = 1
	}
	// The gain at each moment: a sorted list of (time, dB) points.
	type pt struct{ t, g float64 }
	var pts []pt
	for _, s := range spans {
		g, ok := gains[s.Speaker]
		if !ok {
			continue
		}
		pts = append(pts, pt{s.From, g}, pt{s.To, g})
	}
	slices.SortFunc(pts, func(a, b pt) int {
		if a.t < b.t {
			return -1
		}
		if a.t > b.t {
			return 1
		}
		return 0
	})
	gainAt := func(t float64) float64 {
		if len(pts) == 0 {
			return 0
		}
		i, _ := slices.BinarySearchFunc(pts, t, func(p pt, t float64) int {
			if p.t < t {
				return -1
			}
			if p.t > t {
				return 1
			}
			return 0
		})
		switch {
		case i == 0:
			return pts[0].g
		case i >= len(pts):
			return pts[len(pts)-1].g
		}
		a, b := pts[i-1], pts[i]
		// Hold, and move over 80 ms where the gain changes.
		if b.g == a.g || t < b.t-0.08 {
			return a.g
		}
		f := (t - (b.t - 0.08)) / 0.08
		return a.g + (b.g-a.g)*(0.5-0.5*math.Cos(math.Pi*math.Max(0, math.Min(1, f))))
	}
	pr, pw := io.Pipe()
	qr, qw := io.Pipe()
	errs := make(chan error, 2)
	go func() {
		_, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Stdout: pw, Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", in, "-f", "f32le", "-ar", strconv.Itoa(rate), "-"}})
		pw.CloseWithError(err)
		errs <- err
	}()
	go func() {
		_, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Stdin: qr, Args: []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "f32le", "-ar", strconv.Itoa(rate), "-ac", strconv.Itoa(ch), "-i", "-", "-c:a", "pcm_f32le", out}})
		qr.CloseWithError(err)
		errs <- err
	}()
	br := bufio.NewReaderSize(pr, 1<<20)
	bw := bufio.NewWriterSize(qw, 1<<20)
	frame := make([]byte, 4*ch)
	var n int64
	var werr error
	for {
		if _, err := io.ReadFull(br, frame); err != nil {
			break
		}
		g := math.Pow(10, gainAt(float64(n)/rate)/20)
		for c := 0; c < ch; c++ {
			v := math.Float32frombits(binary.LittleEndian.Uint32(frame[4*c:]))
			binary.LittleEndian.PutUint32(frame[4*c:], math.Float32bits(float32(math.Max(-1, math.Min(1, float64(v)*g)))))
		}
		if _, err := bw.Write(frame); err != nil {
			werr = err
			break
		}
		n++
	}
	if err := bw.Flush(); werr == nil {
		werr = err
	}
	qw.Close()
	pr.Close()
	e1, e2 := <-errs, <-errs
	switch {
	case werr != nil:
		return werr
	case e1 != nil:
		return e1
	}
	return e2
}

var loudnormJSON = regexp.MustCompile(`(?s)\{[^{}]*"input_i"[^{}]*\}`)

// loudnorm is two-pass loudness normalization (pre: filters before it).
func loudnorm(ctx context.Context, ts *tools.Set, in, out, pre, target, lra, tp string) (map[string]string, error) {
	first := fmt.Sprintf("%sloudnorm=I=%s:LRA=%s:TP=%s:print_format=json", pre, target, lra, tp)
	res, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Args: []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "info", "-i", in, "-af", first, "-f", "null", "-"}})
	if err != nil {
		return nil, err
	}
	m := loudnormJSON.Find(res.Stderr)
	if m == nil {
		return nil, errors.New("the loudness measurement said nothing")
	}
	var meas map[string]string
	if err := json.Unmarshal(m, &meas); err != nil {
		return nil, err
	}
	second := fmt.Sprintf("%sloudnorm=I=%s:LRA=%s:TP=%s:measured_I=%s:measured_LRA=%s:measured_TP=%s:measured_thresh=%s:offset=%s:linear=true",
		pre, target, lra, tp, meas["input_i"], meas["input_lra"], meas["input_tp"], meas["input_thresh"], meas["target_offset"])
	if err := ff(ctx, ts, "-i", in, "-af", second, "-ar", "48000", "-c:a", "pcm_f32le", out); err != nil {
		return nil, err
	}
	return map[string]string{"target": target, "range": lra, "peak": tp, "measured": meas["input_i"], "measuredRange": meas["input_lra"]}, nil
}

// ---- the copy ----

// mux puts the fixed picture and sound together (the original's stream where
// a part isn't fixed): MP4 with AAC sound for video (MOV when the original's
// picture is copied as it is and MP4 can't hold it), FLAC for sound only.
func mux(ctx context.Context, a *app.App, as *models.Asset, in, vOut, aOut, dir string) (string, string, error) {
	if !as.Media.HasVideo {
		out := filepath.Join(dir, "fixed.flac")
		src := in
		if aOut != "" {
			src = aOut
		}
		if err := ff(ctx, a.Tools, "-i", src, "-vn", "-map", "0:a:0", "-c:a", "flac", "-sample_fmt", "s32", "-ar", "48000", out); err != nil {
			return "", "", err
		}
		return out, "audio/flac", nil
	}
	vcodec, acodec := "", ""
	for _, s := range as.Media.Streams {
		if s.Type == "video" && vcodec == "" {
			vcodec = s.Codec
		}
		if s.Type == "audio" && acodec == "" {
			acodec = s.Codec
		}
	}
	ext, mime := "mp4", "video/mp4"
	if vOut == "" && !slices.Contains([]string{"h264", "hevc", "av1", "mpeg4"}, vcodec) {
		ext, mime = "mov", "video/quicktime"
	}
	out := filepath.Join(dir, "fixed."+ext)
	args := []string{}
	vin := in
	if vOut != "" {
		vin = vOut
	}
	args = append(args, "-i", vin)
	audioIdx := -1
	switch {
	case aOut != "":
		args = append(args, "-i", aOut)
		audioIdx = 1
	case as.Media.HasAudio && vOut != "":
		args = append(args, "-i", in)
		audioIdx = 1
	case as.Media.HasAudio:
		audioIdx = 0
	}
	args = append(args, "-map", "0:v:0", "-c:v", "copy")
	if audioIdx >= 0 {
		args = append(args, "-map", fmt.Sprintf("%d:a:0", audioIdx))
		if aOut == "" && slices.Contains([]string{"aac", "mp3", "alac"}, acodec) {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, "-c:a", "aac", "-b:a", "256k")
		}
	}
	args = append(args, "-map_metadata", "-1", "-movflags", "+faststart", out)
	if err := ff(ctx, a.Tools, args...); err != nil {
		return "", "", err
	}
	return out, mime, nil
}

// copyVariants makes the fixed copy's preview copy, thumbnail and sound wave.
func copyVariants(ctx context.Context, a *app.App, file, dir string, mi *models.MediaInfo) (map[string]models.Variant, error) {
	d := &derive.Deriver{T: a.Tools}
	out := map[string]models.Variant{}
	type mk struct {
		name string
		run  func(string) (*models.Variant, error)
	}
	var list []mk
	if mi.HasVideo {
		list = append(list, mk{"proxy.mp4", func(p string) (*models.Variant, error) { return d.Proxy(ctx, file, p, mi) }},
			mk{"thumb.jpg", func(p string) (*models.Variant, error) { return d.Thumbnail(ctx, file, p, mi) }})
	}
	if mi.HasAudio {
		list = append(list, mk{"peaks.json", func(p string) (*models.Variant, error) { return d.Peaks(ctx, file, p, mi) }})
	}
	for _, m := range list {
		p := filepath.Join(dir, "v-"+m.name)
		v, err := m.run(p)
		if err != nil {
			return nil, err
		}
		key, sha, size, err := publish(ctx, a, p, v.MIME)
		if err != nil {
			return nil, err
		}
		v.StorageKey, v.SHA256, v.Size, v.CreatedAt = key, sha, size, time.Now().UTC()
		out[v.Kind] = *v
	}
	return out, nil
}

// copyEvidence measures the copy's pauses, loudness and cuts and keeps them
// with it (D152), so a version that pins it reads its own from the start.
func copyEvidence(ctx context.Context, a *app.App, as *models.Asset, sha, file string, variants map[string]models.Variant, dir string, mi *models.MediaInfo) error {
	src, srcName := file, "original"
	if px, ok := variants[models.VariantProxy]; ok {
		if p, err := a.Cache.Ensure(ctx, a.Media, px.StorageKey, px.SHA256, px.Size, nil); err == nil {
			src, srcName = p, "proxy"
		}
	}
	m := *mi
	rep, err := evidence.Analyze(ctx, a.Tools, src, srcName, dir, &m)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	digest := sha + ":" + evidence.Version
	old := store.SameMedia(as.ID, sha)
	old["workspaceId"], old["kind"] = a.WorkspaceID(), bson.M{"$in": bson.A{"technical", evidence.InsightKind}}
	if _, err := a.DB.C(store.CInsights).DeleteMany(ctx, old); err != nil {
		return err
	}
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: "technical", Provider: "ffmpeg", Model: a.Tools.Version(ctx, tools.FFmpeg),
		PromptVersion: evidence.Version, Digest: digest, Data: rep.ToMap(), CreatedAt: now, Media: sha}
	if err := a.DB.Insert(ctx, store.CInsights, ins); err != nil {
		return err
	}
	if rep.Momentary != nil {
		ml := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: evidence.InsightKind, Provider: "ffmpeg", Model: ins.Model,
			PromptVersion: evidence.Version, Digest: digest, Data: rep.Momentary.SeriesData(), CreatedAt: now, Media: sha}
		return a.DB.Insert(ctx, store.CInsights, ml)
	}
	return nil
}

// beforeAfter measures the original and the copy the same way, for each fix.
func beforeAfter(ctx context.Context, a *app.App, as *models.Asset, orig, copyFile string, fixes []string) ([]models.FixMeasure, error) {
	kind := kindOf(ctx, a, as)
	before, err := Measure(ctx, a, as, orig, kind, fixes)
	if err != nil {
		return nil, err
	}
	after, err := Measure(ctx, a, as, copyFile, kind, fixes)
	if err != nil {
		return nil, err
	}
	am := map[string]models.FixMark{}
	for _, m := range after {
		am[m.Fix] = m
	}
	var out []models.FixMeasure
	for _, b := range before {
		af, ok := am[b.Fix]
		if !ok || b.Pending != "" {
			continue
		}
		out = append(out, models.FixMeasure{Fix: b.Fix, Unit: b.Unit, Before: b.Value, After: af.Value, Words: measureWords(b.Fix, b.Value, af.Value)})
	}
	return out, nil
}

// measureWords says a before and after plainly.
func measureWords(fix string, before, after float64) string {
	switch fix {
	case models.FixSteady:
		return fmt.Sprintf("shake: %.2f%% → %.2f%% of the picture's width a frame", before, after)
	case models.FixColour:
		return fmt.Sprintf("colour cast: %.0f%% → %.0f%%", before*100, after*100)
	case models.FixGrain:
		return fmt.Sprintf("grain: %.1f → %.1f levels", before, after)
	case models.FixBlocks:
		return fmt.Sprintf("blockiness: %.2f → %.2f (1 is none)", before, after)
	case models.FixVolumes:
		return fmt.Sprintf("difference between voices: %.0f dB → %.0f dB", before, after)
	case models.FixVoices:
		return fmt.Sprintf("speech over the background: %.0f dB → %.0f dB", before, after)
	case models.FixLoudness:
		return fmt.Sprintf("loudness moves over %.0f dB → %.0f dB", before, after)
	case models.FixHum:
		return fmt.Sprintf("hum: %.0f dB → %.0f dB over the sound around it", before, after)
	}
	return fmt.Sprintf("%.2f → %.2f", before, after)
}

// publish stores a file by its content and keeps a verified local copy.
func publish(ctx context.Context, a *app.App, path, mime string) (key, sha string, size int64, err error) {
	sha, err = media.HashFile(path)
	if err != nil {
		return
	}
	key = media.ObjectKey(a.WorkspaceID(), sha)
	info, err := a.Media.PutFile(ctx, key, path, media.PutOptions{ContentType: mime, SHA256: sha}, media.Resume{}, nil)
	if err != nil {
		err = fmt.Errorf("storing the fixed copy failed: %w", err)
		return
	}
	size = info.Size
	_, _ = a.Cache.Adopt(path, sha)
	return
}

// ff runs ffmpeg with MetaRush's own arguments.
func ff(ctx context.Context, ts *tools.Set, args ...string) error {
	_, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Args: append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}, args...)})
	return err
}

// runFF runs an ffmpeg (tool) in dir and reports its progress through the
// file (dur seconds long).
func runFF(ctx context.Context, ts *tools.Set, tool, dir string, dur float64, progress func(float64), args ...string) error {
	full := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-progress", "pipe:1", "-nostats"}, args...)
	_, err := ts.Run(ctx, tools.Spec{Tool: tool, Dir: dir, Background: true, Args: full, OnLine: func(line string) {
		if v, ok := strings.CutPrefix(line, "out_time_us="); ok && dur > 0 {
			if us, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && us > 0 {
				progress(math.Min(1, us/1e6/dur))
			}
		}
	}})
	return err
}
