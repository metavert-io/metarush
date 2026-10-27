package assistant

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/ops"
)

// Plain words for what cards and notes show the person (D140). What Claude
// needs to carry on — versions, IDs, the exact problem with its file and line —
// goes into its notes only (forClaude).

// motionProblemWords says a motion graphic's check problem in plain words, about
// the graphic, never about its code ("It can stay on screen for at most a
// minute"). The exact problem is for Claude (motionCheckForClaude).
func motionProblemWords(p models.MotionProblem) string {
	msg := p.Message
	switch p.Code {
	case "bad_meta":
		switch {
		case strings.Contains(msg, "minDuration") || strings.Contains(msg, "maxDuration"):
			return "It can stay on screen for at most a minute"
		case strings.Contains(msg, "meta.summary"):
			return "It needs a one-sentence description for whoever approves it"
		case strings.Contains(msg, "meta.region"):
			return "It has to say where it goes on screen: the lower part, the middle or the whole picture"
		case strings.Contains(msg, "checkAt"):
			return "The moments it's tested at aren't set up right"
		}
		return "It doesn't say yet what it is, where it goes and how long it stays on screen"
	case "bad_schema":
		return "Its settings (what can be changed, like a name or a colour) can't be read"
	case "bad_preview":
		return "The sample settings for its test frames aren't right"
	case "import_not_allowed", "import_outside", "import_missing", "require", "dynamic_import", "forbidden_global", "code_from_text", "reference_directive":
		return "It uses something MetaRush doesn't allow in graphics"
	case "missing_export":
		return "A part of it is missing"
	case "syntax", "type", "bundle":
		return "It has a mistake Claude needs to fix"
	case "bundle_too_big":
		return "It's too big to draw quickly"
	case "resource_unknown":
		return "It uses a picture this project doesn't have"
	case "resource_param", "resource_name", "resource_kind", "resource_file":
		return "It asks for a picture in a way MetaRush can't follow"
	case "network":
		return "It tried to reach the internet while it was drawn, which graphics can't do"
	case "render":
		return "MetaRush couldn't draw it"
	case "outside_safe_area":
		return "It reaches the edges of the picture, where a phone app's buttons or captions can cover it"
	case "region_mismatch":
		return "It's meant for the lower part of the picture but draws higher up"
	case "draws_nothing":
		return "None of its test frames show anything"
	case "nondeterministic", "nondeterministic_frame":
		return "It may flicker: it doesn't come out the same each time it's drawn"
	case "slow_frames":
		return "It's slow to draw, so renders with it take longer"
	}
	if p.Severity == "warning" {
		return "There's something about it to look at"
	}
	return "It has a problem Claude needs to fix"
}

// motionCheckWords is a motion graphic check's outcome for the card.
func motionCheckWords(o *ops.MotionCheckOut) string {
	name := theirs(o.Name)
	var errs, warns []models.MotionProblem
	for _, p := range o.Problems {
		if p.Severity == "error" {
			errs = append(errs, p)
		} else {
			warns = append(warns, p)
		}
	}
	if !o.OK {
		s := name + " didn't pass the check"
		if len(errs) > 0 {
			s += ": " + lowerFirst(motionProblemWords(errs[0]))
			if len(errs) > 1 {
				s += " (and " + pluralize(len(errs)-1, "more problem") + ")"
			}
		}
		return s + ". Claude can fix it and check it again"
	}
	s := name + " passed the check"
	if len(warns) > 0 {
		s += ". " + upperFirst(pluralize(len(warns), "thing")) + " to look at: " + lowerFirst(motionProblemWords(warns[0]))
	}
	switch o.State {
	case models.MotionApproved:
		s += ". This version is already approved"
	case models.MotionWaiting:
		s += ". Look at its test frames, then approve it if it looks right"
	case models.MotionRejected:
		s += ". You turned this version down before"
	case models.MotionRevoked:
		s += ". Its approval was withdrawn"
	}
	return s
}

// motionCheckForClaude is what Claude needs after a check: the exact version
// and every problem with its file, line and hint.
func motionCheckForClaude(o *ops.MotionCheckOut) string {
	s := " (version " + o.Version + ")"
	for i, p := range o.Problems {
		if i == 5 {
			break
		}
		where := p.File
		if p.Line > 0 {
			where += " line " + strconv.Itoa(p.Line)
		}
		s += "; " + p.Severity + " " + p.Code
		if where != "" {
			s += " at " + where
		}
		s += ": " + clip(p.Message, 240)
		if p.Hint != "" {
			s += " (hint: " + clip(p.Hint, 200) + ")"
		}
	}
	return s
}

// motionDecideWords is a person's decision on a motion graphic, for the card.
func motionDecideWords(o *ops.MotionDecideOut) string {
	name := theirs(o.Row.Name)
	switch o.Row.State {
	case models.MotionApproved:
		return "Approved " + name + ": cuts can use it now"
	case models.MotionRejected:
		return "Turned down " + name + ": cuts won't use it"
	case models.MotionRevoked:
		return "Withdrew approval of " + name + ": new renders that use it wait until it's approved again"
	}
	return plainSummary(o.Summary)
}

// motionComponent reads a motion graphic's record (frames, sheet, preview).
func motionComponent(ctx context.Context, m *Manager, hash string) (*models.MotionComponent, error) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return motion.Get(cctx, m.App.DB, m.App.WorkspaceID(), hash)
}

var (
	// "13:11.732 → 13:34.708": a stretch of footage, to the second.
	timeRangeRe = regexp.MustCompile(`\b(\d{1,3}:\d{2})(?:\.\d+)?\s*→\s*(\d{1,3}:\d{2})(?:\.\d+)?\b`)
	// "13:11.732": a moment, to the second.
	fineTimeRe = regexp.MustCompile(`\b(\d{1,3}:\d{2})\.\d+\b`)
)

// plainTimes rounds times in a sentence to the second ("13:11.732 → 13:34.708"
// reads "13:11–13:34"): people place moments by ear, not by the millisecond.
func plainTimes(s string) string {
	s = timeRangeRe.ReplaceAllString(s, "$1–$2")
	return fineTimeRe.ReplaceAllString(s, "$1")
}

var (
	idInTextRe      = regexp.MustCompile(`\s*\((?:[a-z]{2,4}_[0-9a-z]{26}(?:,\s*)?)+\)|\b[a-z]{2,4}_[0-9a-z]{26}\b\s*`)
	versionInTextRe = regexp.MustCompile(`\s*\(version [a-z0-9-]+@[0-9a-f]{6,64}\)|\b([a-z][a-z0-9-]*)@[0-9a-f]{6,64}\b`)
)

// withoutCodes drops the IDs and version codes a text written for Claude
// carries ("Saved as version 2 (rev_…)", "Approved name-bar@9d6f681d").
func withoutCodes(s string) string {
	s = idInTextRe.ReplaceAllString(s, " ")
	s = versionInTextRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(strings.TrimSpace(m), "(") {
			return ""
		}
		return theirs(m[:strings.Index(m, "@")])
	})
	s = strings.ReplaceAll(s, "()", "")
	return strings.Join(strings.Fields(s), " ")
}

var (
	doneNoteRe   = regexp.MustCompile(`^Done from the dashboard: (.*?)\. Result: ([\s\S]*?)\.?$`)
	triedNoteRe  = regexp.MustCompile(`^Tried from the dashboard: (.*?)\. It failed: ([\s\S]*)$`)
	notNowNoteRe = regexp.MustCompile(`^Not now: (.*?)\. Don't ask for it again unless I bring it up\.$`)
)

// PlainNote is the person's line for a dashboard note of an older
// conversation, whose text was written for Claude (newer notes keep a plain
// line of their own, Turn.Shown).
func PlainNote(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case doneNoteRe.MatchString(s):
		m := doneNoteRe.FindStringSubmatch(s)
		s = plainCardText(m[2])
		if s == "" || s == "Done" {
			s = plainCardText(m[1])
		}
	case triedNoteRe.MatchString(s):
		m := triedNoteRe.FindStringSubmatch(s)
		s = plainCardText(m[1]) + " didn't work: " + plainCardText(m[2])
	case notNowNoteRe.MatchString(s):
		s = "Not now: " + plainCardText(notNowNoteRe.FindStringSubmatch(s)[1])
	default:
		s = plainCardText(s)
	}
	return s
}

/* ---------- Conversations from before the panel spoke plainly ---------- */

// Cards, notes and steps stored by older versions carry the technical words
// this build stopped writing (D140). They are cleaned when shown (never in
// the raw view, never in what is stored), so an older conversation reads like
// a new one.

// rawMotionDuration is the motion check's old raw problem about how long a
// graphic may stay on screen.
const rawMotionDuration = "meta.minDuration and meta.maxDuration are seconds between 0.2 and 60, the first not above the second"

var (
	codeCountRe   = regexp.MustCompile(`^\d+ lines? of code$`)
	oldSpillRe    = regexp.MustCompile(`^Read (?:toolu_[A-Za-z0-9]+|[a-z0-9]{6,16})\.(?:txt|output)$`)
	oldFileRe     = regexp.MustCompile(`^(?:Wrote|Revised) [\w.@-]+\.[A-Za-z0-9]{1,5}$`)
	oldReadFileRe = regexp.MustCompile(`^Read [\w.@-]+\.[A-Za-z0-9]{1,5}$`)
	oldNotesRe    = regexp.MustCompile(`^Read the notes on (.+)$`)
	zeroStillsRe  = regexp.MustCompile(`^Pulled 0 stills from (.+)$`)
)

// plainCardText is a card's or note's text with what is for Claude dropped:
// IDs, version codes, the motion check's raw problem, milliseconds.
func plainCardText(s string) string {
	s = strings.ReplaceAll(s, rawMotionDuration, "It can stay on screen for at most a minute")
	return plainTimes(withoutCodes(s))
}

// plainCard cleans a stored card for the person (older versions wrote code
// counts, version codes and IDs on cards).
func plainCard(a *AssistantAction) {
	var details []string
	for _, d := range a.Details {
		if codeCountRe.MatchString(strings.TrimSpace(d)) {
			continue
		}
		details = append(details, plainCardText(d))
	}
	a.Details = details
	a.Result, a.Error = plainCardText(a.Result), plainCardText(a.Error)
}

// plainStep cleans a stored step line written by an older version.
func plainStep(t *ToolCall) {
	l := t.Label
	switch {
	case oldSpillRe.MatchString(l):
		t.Label, t.Group, t.Many = "Read the rest of a long result", "spill", "Read the rest of %d long results"
	case oldFileRe.MatchString(l):
		t.Label, t.Group, t.Many = "Wrote a draft", "draft", "Wrote %d drafts"
	case l == "Ran a command":
		t.Label = "Tried something that isn't allowed here"
	case l == "Looked for files":
		t.Label, t.Group, t.Many = "Looked through MetaRush's notes", "notes", "Read %d of MetaRush's notes"
	case oldNotesRe.MatchString(l):
		topic := noteTopics[strings.ReplaceAll(oldNotesRe.FindStringSubmatch(l)[1], " ", "-")]
		st := notesStep(topic)
		t.Label, t.Group, t.Many = st.label, st.group, st.many
	case zeroStillsRe.MatchString(l):
		t.Label = "Pulled stills from " + zeroStillsRe.FindStringSubmatch(l)[1]
	case oldReadFileRe.MatchString(l):
		t.Label = "Opened something MetaRush saved"
	}
	switch t.Outcome {
	case "blocked":
		t.Outcome = ""
	case "failed":
		t.Outcome = "didn't work"
	}
}

// plainEvents cleans what older versions stored in a transcript (caller
// passes a copy): notes without a plain line, step lines, the resume notice.
func plainEvents(evs []AssistantEvent, turns []Turn) []AssistantEvent {
	shown := map[int]bool{}
	for _, t := range turns {
		if t.Shown != "" {
			shown[t.N] = true
		}
	}
	out := evs[:0]
	for _, e := range evs {
		switch {
		case e.Kind == "notice" && e.Text == "Resumed the Claude Code session":
			continue
		case e.Kind == "turn" && e.State == "note" && !shown[e.Turn]:
			e.Text = PlainNote(e.Text)
		case (e.Kind == "tool" || e.Kind == "tool_result") && e.Tool != nil:
			cp := *e.Tool
			plainStep(&cp)
			e.Tool = &cp
		}
		out = append(out, e)
	}
	return out
}
