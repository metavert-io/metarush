package editspec

import (
	"math"
	"sort"
)

// Live reads (D85). A live read is one recording a person made while the cut
// played: the audio belongs exactly where it was spoken. Its words (the
// recording's transcript) are matched to the script lines, and every line that
// was found gets its own piece of the recording, cut only in the pause before
// it — where the sound is quiet, never inside a word — and a start point at the
// moment it was spoken. The pieces follow each other without a gap, so until an
// edit moves a shot the read plays exactly as it was recorded (consecutive
// pieces even play as one audio element, see narrationPieces); a re-edit moves
// each line with its shot.

// Envelope is a recording's loudness over time (RMS of each Step seconds), used
// to put the cuts between lines in real pauses.
type Envelope struct {
	Step float64   `json:"step"`
	RMS  []float64 `json:"rms"`
}

// ReadLine is where one line of the script falls in a live read.
type ReadLine struct {
	BeatID string `json:"beatId"`
	Index  int    `json:"index"` // position in the lines given
	Found  bool   `json:"found"`
	// In and Out are the line's piece of the recording (seconds): from the pause
	// before it to the pause before the next line found.
	In  float64 `json:"in"`
	Out float64 `json:"out"`
	// Spoken is where its first word starts and its last word ends.
	SpokenFrom float64  `json:"spokenFrom"`
	SpokenTo   float64  `json:"spokenTo"`
	Match      float64  `json:"match"` // share of the line's words heard
	Matched    int      `json:"matched"`
	Missing    []string `json:"missing,omitempty"`
	Extra      []string `json:"extra,omitempty"`
}

// ReadSplit is a live read matched to the script.
type ReadSplit struct {
	Lines []ReadLine `json:"lines"`
	// VoiceFrom and VoiceTo are where any speech starts and ends in the recording.
	VoiceFrom float64 `json:"voiceFrom"`
	VoiceTo   float64 `json:"voiceTo"`
	Coverage  float64 `json:"coverage"`
	WPM       float64 `json:"wpm"`
}

// Air kept around the pieces of a read.
const (
	readLead = 0.06 // before a line's first word (inside the pause)
	readTail = 0.3  // after the last word of the read
	// readQuietRun is the shortest stretch of quiet that counts as a pause
	// between lines (shorter dips happen inside words).
	readQuietRun = 0.05
)

// SplitRead matches a live read to the lines and cuts it into one piece per line
// found. words is the recording's word-timed transcript; env (optional) its
// loudness, which moves each cut into the quiet of the pause and finds the
// moment a line really starts; duration is the recording's length.
func SplitRead(beats []Beat, words []TranscriptWord, env *Envelope, duration float64) ReadSplit {
	out := ReadSplit{Lines: make([]ReadLine, len(beats))}
	al, spans := matchLines(beats, words)
	var found []int
	matched, total, speech := 0, 0, 0.0
	for i, ba := range al {
		rl := ReadLine{BeatID: ba.BeatID, Index: i, Found: ba.Found, Matched: ba.Matched, Missing: ba.Missing, Extra: ba.Extra}
		if ba.Total > 0 {
			rl.Match = round3(float64(ba.Matched) / float64(ba.Total))
		}
		matched += ba.Matched
		total += ba.Total
		if ba.Found {
			rl.SpokenFrom, rl.SpokenTo = words[spans[i].first].From, words[spans[i].last].To
			speech += rl.SpokenTo - rl.SpokenFrom
			found = append(found, i)
		}
		out.Lines[i] = rl
	}
	if total > 0 {
		out.Coverage = round3(float64(matched) / float64(total))
	}
	if speech > 0 {
		out.WPM = math.Round(float64(matched) / speech * 60)
	}
	if len(words) == 0 {
		return out
	}
	q := newQuiet(env)
	// Where speech starts and ends at all (words that are not in the script
	// included: nothing the person said is left out). A transcript's first and
	// last word times can be a few tenths off; the sound says where they are:
	// the silence before the first word ends where speech starts, the silence
	// after the last word begins where it ends.
	out.VoiceFrom, out.VoiceTo = words[0].From, words[len(words)-1].To
	if q != nil {
		if t, ok := q.firstSound(); ok && t < words[0].To+1 {
			out.VoiceFrom = t
		} else if run, ok := q.longestRun(words[0].From-0.6, math.Min(words[0].To, words[0].From+0.6), readQuietRun); ok {
			out.VoiceFrom = run[1]
		}
		last := words[len(words)-1]
		if t, ok := q.lastSound(); ok && t > last.From-1 {
			out.VoiceTo = t
		}
	}
	if len(found) == 0 {
		return out
	}
	// The cuts: before the first line, between consecutive lines found, after the last.
	cuts := make([]float64, len(found)+1)
	cuts[0] = math.Max(0, out.VoiceFrom-readLead)
	for k := 1; k < len(found); k++ {
		prev, next := spans[found[k-1]], spans[found[k]]
		cuts[k] = pauseCut(q, words[prev.last], words[next.first])
	}
	cuts[len(found)] = out.VoiceTo + readTail
	if duration > 0 {
		cuts[len(found)] = math.Min(duration, cuts[len(found)])
	}
	for k := 1; k < len(cuts); k++ { // never backwards (overlapping transcript words)
		cuts[k] = math.Max(cuts[k], cuts[k-1])
	}
	for k, i := range found {
		out.Lines[i].In, out.Lines[i].Out = round3(cuts[k]), round3(cuts[k+1])
		out.Lines[i].SpokenFrom, out.Lines[i].SpokenTo = round3(out.Lines[i].SpokenFrom), round3(out.Lines[i].SpokenTo)
	}
	out.VoiceFrom, out.VoiceTo = round3(out.VoiceFrom), round3(out.VoiceTo)
	return out
}

// pauseCut is where to cut between a line whose last word is prev and one whose
// first word is next: in the pause between them, just before the next line.
// With the loudness known it is the pause itself — the longest stretch of quiet
// around the transcript's gap, which can be a few tenths off (a word's own short
// silences, like the closure of a b or a t, are shorter than a pause between
// lines) — and the cut sits a breath before the sound starts again.
func pauseCut(q *quiet, prev, next TranscriptWord) float64 {
	prevEnd, nextStart := prev.To, next.From
	if q != nil {
		lo := math.Max(prev.From+0.5*(prev.To-prev.From), math.Min(prevEnd, nextStart)-0.4)
		hi := math.Min(next.From+0.5*(next.To-next.From), math.Max(prevEnd, nextStart)+0.4)
		if run, ok := q.longestRun(lo, hi, readQuietRun); ok {
			return math.Max(run[0], run[1]-readLead)
		}
		if nextStart > prevEnd {
			if run, ok := q.longestRun(prevEnd, nextStart, 0); ok {
				return (run[0] + run[1]) / 2
			}
			return q.quietest(prevEnd, nextStart)
		}
	}
	if nextStart <= prevEnd {
		return (prevEnd + nextStart) / 2
	}
	return math.Max(prevEnd, nextStart-math.Min(0.08, (nextStart-prevEnd)/2))
}

// quiet answers where a recording is quiet: frames below a threshold set from
// its noise floor and its loud parts (like the recorder's own trim, D63).
type quiet struct {
	step float64
	rms  []float64
	thr  float64
}

func newQuiet(env *Envelope) *quiet {
	if env == nil || env.Step <= 0 || len(env.RMS) < 4 {
		return nil
	}
	sorted := append([]float64(nil), env.RMS...)
	sort.Float64s(sorted)
	floor := sorted[len(sorted)/10]
	loud := sorted[len(sorted)*95/100]
	if loud <= 0 {
		return nil
	}
	return &quiet{step: env.Step, rms: env.RMS, thr: math.Max(math.Max(floor*3, loud*0.03), 0.001)}
}

func (q *quiet) frame(t float64) int { return int(math.Floor(t / q.step)) }
func (q *quiet) at(i int) bool       { return i >= 0 && i < len(q.rms) && q.rms[i] < q.thr }

// longestRun is the longest stretch of quiet (at least min seconds) inside
// [from, to], clipped to it.
func (q *quiet) longestRun(from, to, min float64) ([2]float64, bool) {
	var best [2]float64
	found := false
	start := -1
	lo, hi := max(0, q.frame(from)), q.frame(to)
	for i := lo; i <= hi+1; i++ {
		if i <= hi && i < len(q.rms) && q.at(i) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			s, e := math.Max(from, float64(start)*q.step), math.Min(to, float64(i)*q.step)
			if e-s >= min && e > s && (!found || e-s > best[1]-best[0]) {
				best, found = [2]float64{s, e}, true
			}
			start = -1
		}
		if i >= len(q.rms) {
			break
		}
	}
	return best, found
}

// firstSound is where the first sound of a recording that starts quiet begins
// (sound lasting at least 30 ms, so a click is not taken for speech).
func (q *quiet) firstSound() (float64, bool) {
	run := 0
	for i := range q.rms {
		if q.at(i) {
			run = 0
			continue
		}
		run++
		if run*int(math.Round(q.step*1000)) >= 30 {
			return float64(i-run+1) * q.step, true
		}
	}
	return 0, false
}

// lastSound is where the last sound of a recording ends.
func (q *quiet) lastSound() (float64, bool) {
	run := 0
	for i := len(q.rms) - 1; i >= 0; i-- {
		if q.at(i) {
			run = 0
			continue
		}
		run++
		if run*int(math.Round(q.step*1000)) >= 30 {
			return float64(i+run) * q.step, true
		}
	}
	return 0, false
}

// quietest is the quietest moment in [from, to].
func (q *quiet) quietest(from, to float64) float64 {
	best, bi := math.Inf(1), -1
	for i := max(0, q.frame(from)); i <= q.frame(to) && i < len(q.rms); i++ {
		if q.rms[i] < best {
			best, bi = q.rms[i], i
		}
	}
	if bi < 0 {
		return (from + to) / 2
	}
	return math.Min(to, math.Max(from, (float64(bi)+0.5)*q.step))
}

// narrationPiece is what one narration audio element plays: one line, or
// consecutive lines whose recordings continue each other.
type narrationPiece struct {
	first, last ResolvedBeat
	speech      int // frames from first.Start to the end of the last line
	ids         []string
}

// narrationPieces groups the lines that play (beats layout) into audio
// elements. Consecutive lines whose takes are consecutive pieces of one
// recording, placed the way the recording runs (normal speed, the same level) —
// a live read where it was spoken (D85) — play as one element, so the recording
// is never cut or faded between them. A line an edit moved, or one with a take
// of its own, plays on its own (faded in and out over two frames).
func narrationPieces(s *Spec, beats []ResolvedBeat, total int) []narrationPiece {
	var out []narrationPiece
	for _, b := range beats {
		bt := s.Voiceover.Beats[b.Index]
		if b.Problem != "" || bt.Take == nil || b.PlayAsset == "" || b.Start >= total {
			continue
		}
		if n := len(out); n > 0 && continuesPiece(s, out[n-1], b) {
			p := &out[n-1]
			p.speech = b.End() - p.first.Start
			p.ids = append(p.ids, bt.ID)
			p.last = b
			continue
		}
		out = append(out, narrationPiece{first: b, last: b, speech: b.Speech, ids: []string{bt.ID}})
	}
	return out
}

// continuesPiece reports whether line b carries on the recording of piece p:
// the same audio, its piece starting where the previous one ends, and placed so
// that the timeline and the recording advance together (within a frame).
func continuesPiece(s *Spec, p narrationPiece, b ResolvedBeat) bool {
	prev := p.last
	pb, bt := s.Voiceover.Beats[prev.Index], s.Voiceover.Beats[b.Index]
	if b.PlayAsset != prev.PlayAsset || prev.Rate != 1 || b.Rate != 1 || pb.Take.AIVoice != bt.Take.AIVoice || f64(pb.GainDB, 0) != f64(bt.GainDB, 0) {
		return false
	}
	near := func(d int) bool { return d >= -1 && d <= 1 }
	return near(b.TrimBefore-(prev.TrimBefore+prev.Speech)) && near(b.Start-prev.End()) &&
		near((b.Start-b.TrimBefore)-(p.first.Start-p.first.TrimBefore))
}
