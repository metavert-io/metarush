// Package improve is the improvement pipeline: a deterministic pass over the
// activity log finds signals (errors, retries, refused or ignored changes,
// people correcting Claude, Claude saying MetaRush cannot do something, long
// tool chains, slow operations), the metarush-improve skill has Claude Code
// cluster them into feature gaps and friction points, and the resulting report
// is saved as improvement items people keep or dismiss.
package improve

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/logbook"
)

// Signal kinds.
const (
	SigOpError        = "op_error"
	SigToolError      = "tool_error"
	SigProviderError  = "provider_error"
	SigJobFailure     = "job_failure"
	SigRetry          = "retry"
	SigActionRejected = "action_rejected"
	SigActionIgnored  = "action_ignored"
	SigCorrection     = "user_correction"
	SigRephrased      = "rephrased"
	SigMissing        = "missing_capability"
	SigDenied         = "denied"
	SigLongChain      = "long_chain"
	SigSlow           = "slow"
)

// Signal is one kind of trouble seen in the log, with examples.
type Signal struct {
	Kind     string    `json:"kind" jsonschema:"enum=op_error,enum=tool_error,enum=provider_error,enum=job_failure,enum=retry,enum=action_rejected,enum=action_ignored,enum=user_correction,enum=rephrased,enum=missing_capability,enum=denied,enum=long_chain,enum=slow"`
	Key      string    `json:"key" jsonschema:"description=What the examples have in common (an operation and error code\\, a program\\, a phrase)."`
	Title    string    `json:"title" jsonschema:"description=The signal in plain words."`
	Count    int       `json:"count"`
	FirstAt  time.Time `json:"firstAt"`
	LastAt   time.Time `json:"lastAt"`
	Examples []Example `json:"examples"`
}

// Example is one entry behind a signal.
type Example struct {
	EntryID        string    `json:"entryId"`
	At             time.Time `json:"at"`
	Excerpt        string    `json:"excerpt" jsonschema:"description=A short\\, redacted excerpt."`
	Link           string    `json:"link" jsonschema:"description=Dashboard path of the log entry."`
	Channel        string    `json:"channel"`
	ConversationID string    `json:"conversationId,omitempty"`
}

// Texts reads a Claude turn's prompt and reply (they may be stored outside
// the entry). It returns ok=false when the words are not available here.
type Texts func(e *logbook.Entry) (prompt, response string, ok bool)

// Thresholds.
const (
	maxExamples     = 5
	maxTextExamples = 8
	longChain       = 15
	slowOp          = 10 * time.Second
	slowTool        = 2 * time.Minute
	slowStart       = 10 * time.Minute
	ignoredAfter    = 24 * time.Hour
	rephraseWindow  = 15 * time.Minute
)

// longOps take long by nature (uploads, downloads, waiting for jobs).
var longOps = map[string]bool{"ingest.files": true, "cache.prefetch": true, "deliver.export": true, "render.fetch": true, "cache.repair": true,
	"footage.frames": true, "footage.sheet": true, "machine.invite": true, "preview.prepare": true, "assistant.ask": true, "assistant.continue": true,
	"assistant.stop": true, "assistant.interrupt": true, "footage.speakers.samples": true}

// longPrograms run long by nature (renders, Claude Code sessions, transcription).
var longPrograms = map[string]bool{"node": true, "claude": true, "whisper-cli": true, "sherpa-onnx-offline": true, "sherpa-onnx-offline-speaker-diarization": true, "osascript": true}

var correctionRe = regexp.MustCompile(`(?i)(^\s*(no|nope|nah|wrong)\b|that'?s (wrong|not (it|right|what i))|that is (wrong|not)|not what i (asked|meant|wanted|said)|\bi (said|meant|asked for)\b|\b(wrong|incorrect) (clip|one|part|video|take|time|person|speaker)\b|\bundo (that|it|this)\b|\b(try|do it) again\b|\bstill (wrong|not)\b|\byou (missed|forgot|ignored)\b|\bdidn'?t (work|ask for|mean)\b)`)

// missingRes find Claude saying MetaRush lacks a capability, in words.
var missingRes = []struct {
	key string
	re  *regexp.Regexp
}{
	{"no built-in", regexp.MustCompile(`(?i)\b(metarush|it|the cli|there) (has|have|offers?|provides?) no (built-in|native|direct|dedicated)? ?(way|command|option|operation|op|support|feature|tool)\b|\bno (built-in|native|dedicated) (way|command|option|operation|support|feature)\b`)},
	{"not supported", regexp.MustCompile(`(?i)\b(doesn'?t|does not|don'?t|do not|can'?t|cannot|won'?t) (yet )?(support|handle|allow|offer|provide|expose)\b|\b(is|are)(n'?t| not) (yet )?supported\b|\bnot (yet )?supported\b|\bunsupported\b`)},
	{"workaround", regexp.MustCompile(`(?i)\bwork(-| )?around\b|\bworkaround\b`)},
	{"manually", regexp.MustCompile(`(?i)\bmanually\b|\bby hand\b`)},
	{"not directly", regexp.MustCompile(`(?i)\b(can'?t|cannot|can not|unable to|no way to)\b[^.!?\n]{0,60}\bdirectly\b`)},
	{"no command", regexp.MustCompile(`(?i)\bthere(?:'s| is| are) no (metarush )?(command|way|option|operation|op|flag|template)s? (to|for|that)\b`)},
}

var sentenceEnd = regexp.MustCompile(`[.!?\n]`)

// sentenceAround returns the sentence containing [i, j) of s, clipped.
func sentenceAround(s string, i, j int) string {
	start := 0
	if locs := sentenceEnd.FindAllStringIndex(s[:i], -1); len(locs) > 0 {
		start = locs[len(locs)-1][1]
	}
	end := len(s)
	if loc := sentenceEnd.FindStringIndex(s[j:]); loc != nil {
		end = j + loc[1]
	}
	return logbook.Clip(strings.TrimSpace(strings.Join(strings.Fields(s[start:end]), " ")), 240)
}

var normRe = regexp.MustCompile(`\b[a-z]{2,4}_[0-9a-z]{26}\b|\b[0-9a-f]{12,}\b|\d+(\.\d+)?|/[^\s:'"]+`)

// normalize makes similar error messages group together (IDs, numbers and
// paths become placeholders).
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(firstLine(s)))
	s = normRe.ReplaceAllString(s, "#")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

type collector struct {
	sigs map[string]*Signal
	red  func(string) string
}

func (c *collector) add(kind, key, title string, e *logbook.Entry, excerpt string) {
	k := kind + "|" + key
	s := c.sigs[k]
	if s == nil {
		s = &Signal{Kind: kind, Key: key, Title: title, FirstAt: e.At, LastAt: e.At, Examples: []Example{}}
		c.sigs[k] = s
	}
	s.Count++
	if e.At.Before(s.FirstAt) {
		s.FirstAt = e.At
	}
	if e.At.After(s.LastAt) {
		s.LastAt = e.At
	}
	limit := maxExamples
	if kind == SigMissing || kind == SigCorrection || kind == SigRephrased {
		limit = maxTextExamples
	}
	if len(s.Examples) < limit {
		s.Examples = append(s.Examples, Example{EntryID: e.ID, At: e.At, Excerpt: logbook.Clip(c.red(excerpt), 240), Link: links.Log("entry", e.ID),
			Channel: e.Channel, ConversationID: e.Refs.ConversationID})
	}
}

// Extract finds the signals in a set of entries. now decides when an
// undecided action card counts as ignored.
func Extract(entries []logbook.Entry, texts Texts, redact func(string) string, now time.Time) (sigs []Signal, unreadable int) {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	c := &collector{sigs: map[string]*Signal{}, red: redact}
	decided := map[string]bool{}
	for i := range entries {
		if a := entries[i].Action; a != nil && a.Decision != logbook.DecisionProposed {
			decided[a.ID] = true
		}
	}
	type prompt struct {
		at   time.Time
		text string
		e    *logbook.Entry
	}
	byConv := map[string][]prompt{}
	for i := range entries {
		e := &entries[i]
		switch e.Kind {
		case logbook.KindOp:
			op := ""
			if e.Op != nil {
				op = e.Op.Name
			}
			if e.Error != nil {
				// Titles are plain words; the key and the excerpt keep the operation and the error code.
				msg := plainMessage(e.Error)
				if e.Error.Reason == "assistant_read_only" {
					c.add(SigDenied, "op:"+op, fmt.Sprintf("Claude in the dashboard tried to %s and was stopped", logbook.OpWords(op)), e, msg)
					break
				}
				title := "Couldn't " + logbook.OpWords(op)
				if m := strings.TrimSpace(firstLine(e.Error.Message)); m != "" {
					title += ": " + logbook.Clip(m, 100)
				}
				c.add(SigOpError, op+" "+e.Error.Code+" "+normalize(e.Error.Message), title, e, msg)
			}
			if e.DurationMs >= float64(slowOp/time.Millisecond) && !longOps[op] && e.Error == nil {
				c.add(SigSlow, "op:"+op, "It took a long time to "+logbook.OpWords(op), e, fmt.Sprintf("%s took %s", op, logbook.FormatDuration(e.DurationMs)))
			}
		case logbook.KindTool:
			t := e.Tool
			if t == nil {
				break
			}
			if !t.OK && !t.Canceled {
				detail := t.StderrTail
				if e.Error != nil && detail == "" {
					detail = e.Error.Message
				}
				last := lastLine(detail)
				c.add(SigToolError, t.Program+" "+normalize(last), capFirst(logbook.ToolName(t.Program))+" failed", e, last)
			}
			if t.OK && e.DurationMs >= float64(slowTool/time.Millisecond) && !longPrograms[t.Program] {
				c.add(SigSlow, "tool:"+t.Program, capFirst(logbook.ToolName(t.Program))+" was slow", e, fmt.Sprintf("%s ran for %s", t.Program, logbook.FormatDuration(e.DurationMs)))
			}
		case logbook.KindProvider:
			p := e.Provider
			if p == nil || p.OK {
				break
			}
			msg, code := "", ""
			if e.Error != nil {
				msg, code = plainMessage(e.Error), e.Error.Code
			}
			c.add(SigProviderError, p.Name+" "+p.What+" "+code+" "+normalize(msg), fmt.Sprintf("%s couldn't %s", firstNonEmpty(p.Title, p.Name), p.What), e, firstLine(msg))
		case logbook.KindJob:
			j := e.Job
			if j == nil {
				break
			}
			switch j.Event {
			case logbook.JobFailed:
				msg := ""
				if e.Error != nil {
					msg = e.Error.Message
				}
				c.add(SigJobFailure, j.Type+" "+normalize(msg), fmt.Sprintf("%s jobs failed", jobLabel(j.Type)), e, firstLine(msg))
			case logbook.JobRetry:
				msg := ""
				if e.Error != nil {
					msg = e.Error.Message
				}
				c.add(SigRetry, "auto:"+j.Type, fmt.Sprintf("%s jobs had to be tried again", jobLabel(j.Type)), e, firstLine(msg))
			case logbook.JobRequeued:
				c.add(SigRetry, "person:"+j.Type, fmt.Sprintf("Someone retried a %s job by hand", strings.ToLower(jobLabel(j.Type))), e, "retried by a person")
			case logbook.JobLeased:
				if j.WaitedMs >= float64(slowStart/time.Millisecond) {
					c.add(SigSlow, "wait:"+j.Type, fmt.Sprintf("%s jobs waited long to start", jobLabel(j.Type)), e,
						fmt.Sprintf("waited %s before a machine started it", logbook.FormatDuration(j.WaitedMs)))
				}
			}
		case logbook.KindAction:
			a := e.Action
			if a == nil {
				break
			}
			switch a.Decision {
			case logbook.DecisionDismissed:
				c.add(SigActionRejected, "dismissed:"+firstNonEmpty(a.Op, a.Kind), "People said no to changes Claude asked for", e, a.Title)
			case logbook.DecisionFailed:
				c.add(SigActionRejected, "failed:"+firstNonEmpty(a.Op, a.Kind), "Changes Claude asked for failed when a person ran them", e, a.Title+": "+a.Error)
			case logbook.DecisionUnavailable:
				c.add(SigActionRejected, "unavailable:"+a.Kind, "Claude asked for changes the dashboard cannot make", e, a.Title)
			}
		case logbook.KindClaude:
			cl := e.Claude
			if cl == nil {
				break
			}
			for _, t := range cl.Tools {
				if t.Denied {
					c.add(SigDenied, "tool:"+t.Name+" "+commandWord(t.Summary), "Claude tried something it is not allowed to do", e, t.Summary)
				}
			}
			if cl.ToolCount >= longChain {
				c.add(SigLongChain, "long", "One request took many tool calls", e, fmt.Sprintf("%d tool calls for one request", cl.ToolCount))
			}
			if !cl.Note {
				for _, a := range cl.Actions {
					if a.Decision == logbook.DecisionProposed && !decided[a.ID] && now.Sub(e.At) >= ignoredAfter {
						c.add(SigActionIgnored, "ignored:"+firstNonEmpty(a.Op, a.Kind), "Changes Claude asked for were left without an answer", e, a.Title)
					}
				}
			}
			p, r, ok := texts(e)
			if !ok {
				unreadable++
				break
			}
			if !cl.Note && p != "" {
				if correctionRe.MatchString(p) {
					c.add(SigCorrection, "correction", "People corrected Claude", e, p)
				}
				if conv := e.Refs.ConversationID; conv != "" {
					byConv[conv] = append(byConv[conv], prompt{at: e.At, text: p, e: e})
				}
			}
			for _, m := range missingRes {
				if loc := m.re.FindStringIndex(r); loc != nil {
					c.add(SigMissing, m.key, "Claude said MetaRush cannot do something ("+m.key+")", e, sentenceAround(r, loc[0], loc[1]))
				}
			}
		}
	}
	for _, ps := range byConv {
		sort.Slice(ps, func(i, j int) bool { return ps[i].at.Before(ps[j].at) })
		for i := 1; i < len(ps); i++ {
			if ps[i].at.Sub(ps[i-1].at) <= rephraseWindow && similar(ps[i-1].text, ps[i].text) {
				c.add(SigRephrased, "rephrased", "People asked the same thing again in other words", ps[i].e, ps[i-1].text+" → "+ps[i].text)
			}
		}
	}
	for _, s := range c.sigs {
		sigs = append(sigs, *s)
	}
	sort.Slice(sigs, func(i, j int) bool {
		if sigs[i].Count != sigs[j].Count {
			return sigs[i].Count > sigs[j].Count
		}
		if !sigs[i].LastAt.Equal(sigs[j].LastAt) {
			return sigs[i].LastAt.After(sigs[j].LastAt)
		}
		return sigs[i].Kind+sigs[i].Key < sigs[j].Kind+sigs[j].Key
	})
	if sigs == nil {
		sigs = []Signal{}
	}
	return sigs, unreadable
}

func plainMessage(e *logbook.EntryError) string {
	if e.Code != "" {
		return e.Code + ": " + e.Message
	}
	return e.Message
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

func commandWord(s string) string {
	f := strings.Fields(s)
	switch {
	case len(f) >= 3 && f[0] == "metarush":
		return f[0] + " " + f[1] + " " + f[2]
	case len(f) >= 2:
		return f[0] + " " + f[1]
	case len(f) == 1:
		return f[0]
	}
	return ""
}

func jobLabel(t string) string {
	if logbook.JobLabel != nil {
		return logbook.JobLabel(t)
	}
	return t
}

func capFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// words splits a prompt into lowercase words for comparisons.
func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len(w) > 2 {
			out[w] = true
		}
	}
	return out
}

// similar reports whether two prompts ask the same thing in other words
// (Jaccard similarity of their words ≥ 0.5, not identical).
func similar(a, b string) bool {
	if strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) {
		return false
	}
	wa, wb := words(a), words(b)
	if len(wa) < 3 || len(wb) < 3 {
		return false
	}
	inter := 0
	for w := range wa {
		if wb[w] {
			inter++
		}
	}
	union := len(wa) + len(wb) - inter
	return union > 0 && float64(inter)/float64(union) >= 0.5
}
