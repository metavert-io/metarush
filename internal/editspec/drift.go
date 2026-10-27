package editspec

import (
	"fmt"
	"math"
	"strconv"
)

// Drift copies (D119). The renderer plays a sound at a playback rate other
// than 1 with ffmpeg's atempo, which keeps the pitch by stretching time in
// short windows: where it puts the sound wanders by up to 20 ms (measured on
// a recording 208 ppm fast: 15 ms early near its start, 11 ms later on). A
// recording placed to the sample whose clock drifts is therefore rendered from
// a copy of the stretch it plays, resampled to the conversation's clock (its
// pitch moves by a fraction of a cent, which nobody hears) and played at rate
// 1, so its time stays exactly linear. The render worker makes the copies with
// ffmpeg just before rendering; the preview plays the recording itself,
// resampled by the browser (the renderer turns pitch preservation off for
// these sounds).

// DriftCopy is one copy the renderer plays instead of a drifting recording.
type DriftCopy struct {
	// Key is the source key the element plays from now (the worker serves
	// the copy under it).
	Key string `json:"key"`
	// AssetID is the file the copy is made of: the recording, or the clearer
	// copy of it the element played.
	AssetID string `json:"assetId"`
	// Start is where in that file the copy begins (whole seconds, so reading
	// from there is exact to the sample), and Seconds how much of it the copy
	// takes.
	Start   int     `json:"start"`
	Seconds float64 `json:"seconds"`
	// Rate is the file's seconds per conversation second. The file is
	// resampled to Down Hz, relabeled as Up Hz and resampled to 48 kHz: its
	// time runs Up/Down = Rate times faster, exactly.
	Rate float64 `json:"rate"`
	Up   int     `json:"up"`
	Down int     `json:"down"`
}

// DriftRate is the sample rate drift copies are made at.
const DriftRate = 48000

// DriftCopies rewrites a resolved plan so that every recording placed to the
// sample (trimFraction set) that plays at a rate other than 1 plays a drift
// copy at rate 1 instead (its clearer copy's sound when it played one), and
// returns the copies to make. Other elements are unchanged.
func DriftCopies(p *RenderPlan) []DriftCopy {
	if p == nil || p.FPS <= 0 {
		return nil
	}
	fps := float64(p.FPS)
	var out []DriftCopy
	for i := range p.Audio {
		a := &p.Audio[i]
		rate := a.PlaybackRate
		if a.TrimFraction == nil || rate <= 0 || rate == 1 || a.DurationInFrames <= 0 {
			continue
		}
		asset, exact := a.AssetID, ExactTrim(a.TrimBefore, a.TrimFraction)
		if v := a.Variant; v != nil {
			asset, exact = v.AssetID, ExactTrim(v.TrimBefore, v.TrimFraction)
		}
		at := exact / fps // seconds into the file at the element's first frame
		start := max(0, int(math.Floor(at))-1)
		up, down := rationalRate(rate)
		c := DriftCopy{Key: fmt.Sprintf("drift-%d", len(out)+1), AssetID: asset, Start: start, Rate: rate, Up: up, Down: down,
			Seconds: math.Ceil((at-float64(start)+float64(a.DurationInFrames)/fps*rate+1)*1000) / 1000}
		// In the copy, conversation time c is file time start + c·rate.
		a.AssetID, a.Variant, a.PlaybackRate = c.Key, nil, 1
		a.TrimBefore, a.TrimFraction = SplitTrim((at - float64(start)) / rate * fps)
		out = append(out, c)
	}
	return out
}

// rationalRate is the playback rate as a ratio of two sample rates (up/down,
// down between 48 and 192 kHz) as close to it as they get: at most a
// thousandth of a part per million off for the drifts recordings have.
func rationalRate(rate float64) (up, down int) {
	best := math.Inf(1)
	for d := DriftRate; d <= 4*DriftRate; d++ {
		u := int(math.Round(rate * float64(d)))
		if e := math.Abs(float64(u)/float64(d) - rate); e < best {
			best, up, down = e, u, d
			if e < 1e-13 {
				break
			}
		}
	}
	return up, down
}

// FFmpegArgs are ffmpeg's arguments to make the copy from the file in into
// out (a WAV file): the file decoded from its start, as the renderer decodes
// every sound (seeking into compressed sound can land a few hundred samples
// off: 256 measured on AAC), cut at Start for Seconds, resampled to Down Hz,
// relabeled as Up Hz (time runs Rate times faster) and resampled to 48 kHz.
// Channels are kept, 24-bit samples. The resampler is ffmpeg's own (every
// build has it), with a long Kaiser filter and fine phases.
func (c DriftCopy) FFmpegArgs(in, out string) []string {
	swr := ":filter_size=64:phase_shift=12:linear_interp=1:cutoff=0.97:filter_type=kaiser:kaiser_beta=9"
	end := strconv.FormatFloat(float64(c.Start)+c.Seconds, 'f', 3, 64)
	af := "atrim=start=" + strconv.Itoa(c.Start) + ":end=" + end + ",asetpts=PTS-STARTPTS," +
		"aresample=" + strconv.Itoa(c.Down) + swr + ",asetrate=" + strconv.Itoa(c.Up) + ",aresample=" + strconv.Itoa(DriftRate) + swr
	return []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-i", in, "-vn", "-map", "0:a:0", "-af", af, "-c:a", "pcm_s24le", "-f", "wav", out}
}
