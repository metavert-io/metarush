package spoken

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strconv"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/sound"
	"github.com/metavert-io/metarush/internal/tools"
)

// Fitting new words into the stretch where the old ones were (D310).
//
// The new sound covers [From, To) of the recording. Its first and last
// editspec.SaidEdge are the recording's own samples (the plan crossfades the
// clip into it there, so nothing changes at the seams); then the recording
// crossfades into the room's own quiet, taken from a pause nearby and laid
// under the whole new stretch, with the new words over it at the level the
// speaker had; then back. New words longer than the room for them are sped
// up by at most a quarter; longer still is refused.

const (
	fitRate     = 48000
	crossfade   = 0.03 // the recording into the room's quiet and back
	maxLead     = 0.2  // how much of the pause before the old words the new stretch may take
	maxSpeedUp  = 1.25 // the most new words are sped up to fit
	roomInset   = 0.08 // kept from the words on each side of a pause taken as the room's quiet
	roomMin     = 0.25 // the shortest pause taken as the room's quiet
	roomReach   = 20.0 // how far from the words a pause is looked for (seconds)
	pieceMargin = 0.04 // kept before the first new word and after the last
	pieceFade   = 0.008
	maxGainDB   = 15.0
)

// fitIn is what fitting needs: the recording, the old words and their
// neighbours, the pauses between words nearby, and the new words in the speech made.
type fitIn struct {
	src      string  // the recording's media file
	duration float64 // its length
	channels int
	// wf, wt are the old words; prevEnd, nextBeg where the words around them end and start.
	wf, wt, prevEnd, nextBeg float64
	pauses                   []span // between words, in the recording
	speech                   string // the speech made (any format)
	// newFrom, newTo are the new words in the speech made, as the recognizer
	// heard them; cutFrom, cutTo how far the piece may reach (the words around).
	newFrom, newTo, cutFrom, cutTo float64
	scratch                        string
}

type span struct{ from, to float64 }

// errTooLong is new words that can't fit even sped up.
type errTooLong struct{ need, room float64 }

func (e errTooLong) Error() string {
	return fmt.Sprintf("The new words take %.1f seconds; the old ones had room for %.1f, and MetaRush speeds new words up by at most a quarter. Choose a longer stretch to change, or fewer words.", e.need, e.room)
}

// fit makes the new sound; out is a WAV exactly To−From long.
func fit(ctx context.Context, a *app.App, in fitIn) (out string, f fitResult, err error) {
	ch := min(max(in.channels, 1), 2)
	// The recording around the words, for the edges, the speaker's level and the room's quiet.
	wFrom := math.Max(0, in.wf-roomReach)
	wTo := in.wt + roomReach
	if in.duration > 0 {
		wTo = math.Min(wTo, in.duration)
	}
	around := filepath.Join(in.scratch, "around.wav")
	if err := extract(ctx, a, in.src, wFrom, wTo-wFrom, ch, around); err != nil {
		return "", f, err
	}
	rec, err := sound.ReadWAV(around)
	if err != nil {
		return "", f, err
	}
	at := func(t float64) int { return int(math.Round((t - wFrom) * float64(rec.Rate))) }
	// Where the old words really start and end: the quietest moment between
	// them and the words around (recognizers' word times are approximate, and
	// in connected speech the gap is a dip, not a silence).
	before, after := math.Min(in.prevEnd, in.wf), math.Max(in.nextBeg, in.wt)
	nFrom := quietMoment(rec, wFrom, math.Max(wFrom, before-0.06), in.wf+0.06, (before+in.wf)/2)
	nTo := quietMoment(rec, wFrom, in.wt-0.06, after+0.06, (in.wt+after)/2)
	// A clear pause on either side is room too, up to maxLead.
	if in.wf-in.prevEnd > 2*maxLead {
		nFrom = math.Min(nFrom, in.wf-maxLead)
	}
	if in.nextBeg-in.wt > 2*maxLead {
		nTo = math.Max(nTo, in.wt+maxLead)
	}
	f.From = math.Max(0, nFrom-crossfade-editspec.SaidEdge)
	f.To = nTo + crossfade + editspec.SaidEdge
	if in.duration > 0 {
		f.To = math.Min(f.To, in.duration)
	}
	f.From, f.To = round3(f.From), round3(f.To)
	// The new words, cut out of the speech made at the quietest moments
	// around them, and their silent ends trimmed.
	made := filepath.Join(in.scratch, "made.wav")
	if err := extract(ctx, a, in.speech, 0, 600, ch, made); err != nil {
		return "", f, err
	}
	gen, err := sound.ReadWAV(made)
	if err != nil {
		return "", f, err
	}
	pFrom := quietMoment(gen, 0, math.Max(in.cutFrom, in.newFrom-0.25), in.newFrom+0.05, in.newFrom-0.03)
	pTo := quietMoment(gen, 0, in.newTo-0.05, math.Min(in.cutTo, in.newTo+0.25), in.newTo+0.03)
	pw := trimQuiet(slice(gen, pFrom, pTo), 0.015)
	room := nTo - nFrom
	f.Speed = 1
	if need := pw.Seconds(); need > room+0.005 {
		if need/room > maxSpeedUp {
			return "", f, errTooLong{need: need, room: room}
		}
		f.Speed = math.Round(need/room*1000) / 1000
		piece := filepath.Join(in.scratch, "piece.wav")
		if err := sound.WriteWAV(piece, pw, true); err != nil {
			return "", f, err
		}
		faster := filepath.Join(in.scratch, "piece-faster.wav")
		if err := run(ctx, a, "-i", piece, "-filter:a", "atempo="+strconv.FormatFloat(f.Speed, 'f', 3, 64), "-ac", strconv.Itoa(ch), "-ar", strconv.Itoa(fitRate), "-c:a", "pcm_f32le", faster); err != nil {
			return "", f, err
		}
		if pw, err = sound.ReadWAV(faster); err != nil {
			return "", f, err
		}
	}
	// The speaker's level where the old words were.
	f.GainDB = 0
	if old, nw := rms(rec, at(nFrom), at(nTo)), rms(pw, 0, pw.Frames()); old > 0 && nw > 0 {
		f.GainDB = clamp(20*math.Log10(old/nw), -maxGainDB, maxGainDB)
	}
	g := float32(math.Pow(10, f.GainDB/20))
	// The room's quiet: the quietest pause nearby.
	quiet := quietest(rec, wFrom, in.pauses, in.wf, in.wt)
	f.RoomFrom, f.RoomTo = quiet.from, quiet.to
	roomBuf := loopRoom(rec, at(quiet.from), at(quiet.to), int(math.Ceil((nTo-nFrom+2*crossfade)*float64(rec.Rate)))+1)
	// Where the new words go: centred on the old ones, inside the room.
	plen := pw.Seconds()
	start := clamp((nFrom+nTo)/2-plen/2, nFrom, nTo-plen)
	f.WordsFrom, f.WordsTo = round3(start+pieceMargin*0.5), round3(math.Min(nTo, start+plen-pieceMargin*0.5))
	// Assemble.
	n := int(math.Round((f.To - f.From) * float64(rec.Rate)))
	outA := &sound.Audio{Rate: rec.Rate, Channels: ch, Data: make([]float32, n*ch)}
	inner0 := nFrom - crossfade
	for k := 0; k < n; k++ {
		t := f.From + float64(k)/float64(rec.Rate)
		w := 0.0
		switch {
		case t <= nFrom-crossfade || t >= nTo+crossfade:
		case t < nFrom:
			w = (t - (nFrom - crossfade)) / crossfade
		case t > nTo:
			w = ((nTo + crossfade) - t) / crossfade
		default:
			w = 1
		}
		ri := at(t)
		var ip int
		if w > 0 {
			ip = int(math.Round((t - inner0) * float64(rec.Rate)))
		}
		pk := int(math.Round((t - start) * float64(rec.Rate)))
		for c := 0; c < ch; c++ {
			var o float32
			if ri >= 0 && ri < rec.Frames() {
				o = rec.Data[ri*ch+c]
			}
			if w <= 0 {
				outA.Data[k*ch+c] = o
				continue
			}
			var inner float32
			if ip >= 0 && ip < len(roomBuf)/ch {
				inner = roomBuf[ip*ch+c]
			}
			if pk >= 0 && pk < pw.Frames() {
				inner += pw.Data[pk*ch+c] * g * float32(edgeFade(pk, pw.Frames(), int(pieceFade*float64(rec.Rate))))
			}
			// Equal power: the recording and the new stretch aren't the same sound.
			a, b := math.Cos(w*math.Pi/2), math.Sin(w*math.Pi/2)
			outA.Data[k*ch+c] = o*float32(a) + inner*float32(b)
		}
	}
	out = filepath.Join(in.scratch, "new-words.wav")
	if err := sound.WriteWAV(out, outA, false); err != nil {
		return "", f, err
	}
	// What the cut will play there: the recording a few seconds either side,
	// with the new sound in place (the recognizer hears a word best in its sentence).
	cFrom, cTo := math.Max(wFrom, f.From-checkAround), math.Min(wFrom+rec.Seconds(), f.To+checkAround)
	i0, i1, p0 := at(cFrom), min(rec.Frames(), at(cTo)), at(f.From)
	chkA := &sound.Audio{Rate: rec.Rate, Channels: ch, Data: append([]float32(nil), rec.Data[max(0, i0)*ch:i1*ch]...)}
	for k := 0; k < n; k++ {
		if j := p0 - i0 + k; j >= 0 && j < chkA.Frames() {
			copy(chkA.Data[j*ch:(j+1)*ch], outA.Data[k*ch:(k+1)*ch])
		}
	}
	f.CheckPath, f.CheckFrom, f.CheckTo = filepath.Join(in.scratch, "as-the-cut-plays-it.wav"), cFrom, cTo
	if err := sound.WriteWAV(f.CheckPath, chkA, false); err != nil {
		return "", f, err
	}
	return out, f, nil
}

// checkAround is how much of the recording on each side the check hears with the new words.
const checkAround = 2.5

type fitResult struct {
	From, To, WordsFrom, WordsTo, Speed, GainDB, RoomFrom, RoomTo float64
	// CheckPath is the new words as the cut will play them, with the
	// recording from CheckFrom to CheckTo around them.
	CheckPath          string
	CheckFrom, CheckTo float64
}

// extract reads [from, from+dur) of a file's first sound at 48 kHz, as 32-bit float WAV.
func extract(ctx context.Context, a *app.App, src string, from, dur float64, ch int, out string) error {
	return run(ctx, a, "-ss", ff(from), "-t", ff(math.Max(0.01, dur)), "-i", src, "-map", "0:a:0", "-ac", strconv.Itoa(ch), "-ar", strconv.Itoa(fitRate), "-c:a", "pcm_f32le", out)
}

// to16k writes 16 kHz mono 16-bit WAV (what the recognizer and fingerprints read).
func to16k(ctx context.Context, a *app.App, src, out string) error {
	return run(ctx, a, "-i", src, "-map", "0:a:0", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", out)
}

func run(ctx context.Context, a *app.App, args ...string) error {
	_, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: append([]string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"}, args...), Background: true})
	return err
}

func ff(v float64) string { return strconv.FormatFloat(math.Max(0, v), 'f', 4, 64) }

func clamp(v, lo, hi float64) float64 {
	if hi < lo {
		return lo
	}
	return math.Max(lo, math.Min(hi, v))
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// rms is the level of frames [i, j) (all channels).
func rms(a *sound.Audio, i, j int) float64 {
	i, j = max(0, i), min(a.Frames(), j)
	if j <= i {
		return 0
	}
	s := 0.0
	for k := i * a.Channels; k < j*a.Channels; k++ {
		v := float64(a.Data[k])
		s += v * v
	}
	return math.Sqrt(s / float64((j-i)*a.Channels))
}

// quietest is the quietest pause between words near the change (inset from
// the words), else the quietest quarter second of the stretch read.
func quietest(rec *sound.Audio, wFrom float64, pauses []span, wf, wt float64) span {
	at := func(t float64) int { return int(math.Round((t - wFrom) * float64(rec.Rate))) }
	end := wFrom + rec.Seconds()
	best, bestL := span{}, math.Inf(1)
	for _, p := range pauses {
		a, b := p.from+roomInset, p.to-roomInset
		if b-a < roomMin || a < wFrom || b > end || (a < wt && wf < b) {
			continue
		}
		if l := rms(rec, at(a), at(b)); l < bestL {
			best, bestL = span{a, b}, l
		}
	}
	if !math.IsInf(bestL, 1) {
		return best
	}
	step := 0.05
	for t := wFrom; t+roomMin <= end; t += step {
		if t < wt && wf < t+roomMin {
			continue
		}
		if l := rms(rec, at(t), at(t+roomMin)); l < bestL {
			best, bestL = span{t, t + roomMin}, l
		}
	}
	if math.IsInf(bestL, 1) {
		return span{}
	}
	return best
}

// loopRoom repeats frames [i, j) of rec, crossfading each repeat into the
// next over 20 ms, until n frames (interleaved samples returned).
func loopRoom(rec *sound.Audio, i, j, n int) []float32 {
	ch := rec.Channels
	out := make([]float32, n*ch)
	i, j = max(0, i), min(rec.Frames(), j)
	if j-i < 2 {
		return out
	}
	seg := rec.Data[i*ch : j*ch]
	l := j - i
	xf := min(int(0.02*float64(rec.Rate)), l/3)
	pos := 0
	for pos < n {
		for k := 0; k < l && pos+k < n; k++ {
			v := seg[k*ch : k*ch+ch]
			wIn := 1.0
			if pos > 0 && k < xf {
				wIn = float64(k) / float64(xf)
			}
			for c := 0; c < ch; c++ {
				out[(pos+k)*ch+c] = out[(pos+k)*ch+c]*float32(1-wIn) + v[c]*float32(wIn)
			}
		}
		pos += l - xf
		if l-xf <= 0 {
			break
		}
	}
	return out
}

// edgeFade is 0→1 over the first n frames of a piece and 1→0 over its last.
func edgeFade(k, total, n int) float64 {
	if n <= 0 {
		return 1
	}
	switch {
	case k < n:
		return float64(k) / float64(n)
	case k >= total-n:
		return float64(total-1-k) / float64(n)
	}
	return 1
}

// quietMoment is the quietest moment of [from, to] (seconds; t0 is when the
// audio starts): the middle of its quietest 10 ms, the one nearest pref when
// several are about as quiet (a real gap between words is far quieter).
func quietMoment(a *sound.Audio, t0, from, to, pref float64) float64 {
	if to <= from {
		return (from + to) / 2
	}
	win := max(1, a.Rate/100)
	step := max(1, a.Rate/400)
	i0, i1 := int((from-t0)*float64(a.Rate)), int((to-t0)*float64(a.Rate))
	best, at := math.Inf(1), clamp(pref, from, to)
	for i := max(0, i0); i+win <= min(a.Frames(), i1+win); i += step {
		t := t0 + float64(i+win/2)/float64(a.Rate)
		if l := rms(a, i, i+win) * (1 + 2*math.Abs(t-pref)); l < best {
			best, at = l, t
		}
	}
	return clamp(at, from, to)
}

// slice is [from, to) of audio (seconds).
func slice(a *sound.Audio, from, to float64) *sound.Audio {
	i, j := max(0, int(from*float64(a.Rate))), min(a.Frames(), int(to*float64(a.Rate)))
	if j < i {
		j = i
	}
	return &sound.Audio{Rate: a.Rate, Channels: a.Channels, Data: append([]float32(nil), a.Data[i*a.Channels:j*a.Channels]...)}
}

// trimQuiet takes away the quiet at both ends of a piece of speech (below
// 45 dB under its loudest 10 ms), keeping keep seconds of it.
func trimQuiet(a *sound.Audio, keep float64) *sound.Audio {
	win := max(1, a.Rate/100)
	n := a.Frames()
	peak := 0.0
	for i := 0; i+win <= n; i += win {
		peak = math.Max(peak, rms(a, i, i+win))
	}
	if peak <= 0 {
		return a
	}
	floor := peak * math.Pow(10, -45.0/20)
	first, last := 0, n
	for first+win <= n && rms(a, first, first+win) < floor {
		first += win / 4
	}
	for last-win >= first && rms(a, last-win, last) < floor {
		last -= win / 4
	}
	k := int(keep * float64(a.Rate))
	first, last = max(0, first-k), min(n, last+k)
	if last <= first {
		return a
	}
	return &sound.Audio{Rate: a.Rate, Channels: a.Channels, Data: append([]float32(nil), a.Data[first*a.Channels:last*a.Channels]...)}
}
