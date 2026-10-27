package conversation

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
)

// The automatic cut (D102): who is on screen when, from each person's speech
// activity, as ordinary shots with a reason each.

// CallStyle is how a cut moves between people.
type CallStyle struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	About string `json:"about"`
	// MinShot is the shortest a shot may be; MaxShot the longest on one picture.
	MinShot float64 `json:"minShot"`
	MaxShot float64 `json:"maxShot"`
	// JDelay is how long after a new speaker starts the picture follows them;
	// LLead how long before a turn the picture goes to the next speaker (L-cut,
	// reaction-heavy styles; 0 otherwise).
	JDelay float64 `json:"jDelay"`
	LLead  float64 `json:"lLead"`
	// ReactAfter: a turn longer than this gets a listener's reaction; ReactLen how long.
	ReactAfter float64 `json:"reactAfter"`
	ReactLen   float64 `json:"reactLen"`
	// Open shows everyone for this long first (0: no opening shot).
	Open float64 `json:"open"`
	// Together: show the split layout when people talk or laugh together.
	Together bool `json:"together"`
	// Interviewer: an off-camera interviewer is never shown.
	Interviewer bool `json:"interviewer"`
	// BRoll: cutaways to other footage during long turns.
	BRoll bool `json:"broll"`
}

// Styles are the vetted styles.
var Styles = map[string]CallStyle{
	"podcast": {Name: "podcast", Label: "Host and guest podcast", About: "Opens on both, then whoever is talking; reactions on laughs and short replies; side by side when both talk.",
		MinShot: 2, MaxShot: 30, JDelay: 0.4, ReactAfter: 18, ReactLen: 2.2, Open: 3, Together: true},
	"interview": {Name: "interview", Label: "Off-camera interviewer", About: "The guest only: the interviewer is never shown; questions are heard over the guest listening, or shown as text cards.",
		MinShot: 2.5, MaxShot: 45, JDelay: 0.4, ReactAfter: 0, Interviewer: true},
	"panel": {Name: "panel", Label: "Panel", About: "Opens on everyone in a grid, then whoever is talking; the grid when several talk.",
		MinShot: 2.5, MaxShot: 30, JDelay: 0.5, ReactAfter: 20, ReactLen: 2.2, Open: 3.5, Together: true},
	"reactions": {Name: "reactions", Label: "Reaction-heavy", About: "Short shots and lots of listening faces: the picture often goes to the listener before the speaker finishes.",
		MinShot: 1.2, MaxShot: 12, JDelay: 0.25, LLead: 0.6, ReactAfter: 5, ReactLen: 1.6, Open: 2, Together: true},
	"broll": {Name: "broll", Label: "Talking head and cutaways", About: "Whoever is talking, with cutaways to other footage during long answers.",
		MinShot: 2.5, MaxShot: 40, JDelay: 0.4, ReactAfter: 0, BRoll: true},
}

func styleNames() []string {
	out := make([]string, 0, len(Styles))
	for k := range Styles {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// turn is a stretch one person holds the floor.
type turn struct {
	member     string
	from, to   float64
	short      bool // a short reply or a laugh: doesn't take the picture
	question   bool // ends with a question
	text       string
	words      []editspec.TranscriptWord
	sentenceAt []float64 // where sentences end inside it
}

// cutInput is what the automatic cut works from.
type cutInput struct {
	pin    editspec.ConversationPin
	view   *editspec.ConversationView
	from   float64
	to     float64
	style  CallStyle
	layout string // the split layout
	// interviewer is never shown (interview style).
	interviewer string
	// cards: questions become text cards (interview style).
	cards bool
	// broll are footage for cutaways: ID, title and words seen or said in it.
	broll []brollSource
	names map[string]string // member → name
}

type brollSource struct {
	id, title string
	words     map[string]bool
	duration  float64
}

// plannedClip is one clip of the automatic cut.
type plannedClip struct {
	in, out float64
	shots   []editspec.Shot
	muted   []string
	why     string
}

const shortTurn = 0.8 // a reply shorter than this doesn't take the picture

// turns builds who holds the floor when, from the activity and words.
func turns(in *cutInput) []turn {
	var out []turn
	for _, m := range in.pin.Members {
		if !m.HasSound() {
			continue
		}
		for _, sp := range mergeAct(in.view.Activity[m.ID], 0.7) {
			if sp.To <= in.from || sp.From >= in.to {
				continue
			}
			t := turn{member: m.ID, from: math.Max(sp.From, in.from), to: math.Min(sp.To, in.to)}
			for _, sg := range in.view.Own[m.ID] {
				for _, w := range sg.Words {
					if mid := (w.From + w.To) / 2; mid >= t.from && mid < t.to {
						t.words = append(t.words, w)
					}
				}
			}
			var texts []string
			for _, w := range t.words {
				texts = append(texts, strings.TrimSpace(w.Text))
				if endsSentence(w.Text) {
					t.sentenceAt = append(t.sentenceAt, w.To)
				}
			}
			t.text = strings.Join(texts, " ")
			t.question = strings.HasSuffix(strings.TrimSpace(t.text), "?")
			t.short = t.to-t.from < shortTurn || isReply(t.text)
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].from < out[j].from })
	return out
}

func mergeAct(sp []editspec.Span, join float64) []editspec.Span {
	var out []editspec.Span
	for _, s := range sp {
		if n := len(out); n > 0 && s.From-out[n-1].To < join {
			out[n-1].To = math.Max(out[n-1].To, s.To)
			continue
		}
		out = append(out, s)
	}
	return out
}

func endsSentence(w string) bool {
	w = strings.TrimSpace(w)
	return strings.HasSuffix(w, ".") || strings.HasSuffix(w, "?") || strings.HasSuffix(w, "!")
}

// isReply: a few words that acknowledge rather than speak ("yeah", "mm-hmm",
// "ha ha"), however long they last.
func isReply(text string) bool {
	ws := strings.Fields(strings.ToLower(text))
	if len(ws) == 0 || len(ws) > 4 {
		return len(ws) == 0
	}
	replies := map[string]bool{"yeah": true, "yes": true, "yep": true, "mm": true, "mm-hmm": true, "mhm": true, "uh-huh": true, "right": true, "okay": true,
		"ok": true, "sure": true, "wow": true, "ha": true, "haha": true, "hmm": true, "exactly": true, "totally": true, "absolutely": true, "true": true, "oh": true, "nice": true}
	for _, w := range ws {
		if !replies[strings.Trim(w, ".,!?;:\"'“”")] {
			return false
		}
	}
	return true
}

// gaps are the pauses between consecutive words of everyone (centers), for
// switches that never land inside a word.
func gaps(view *editspec.ConversationView) (centers []float64, edges []float64) {
	var ws []editspec.TranscriptWord
	for _, sg := range view.Words {
		ws = append(ws, sg.Words...)
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].From < ws[j].From })
	end := math.Inf(-1)
	for _, w := range ws {
		if w.From-end >= 0.06 {
			centers = append(centers, (w.From+end)/2)
		}
		edges = append(edges, w.From, w.To)
		end = math.Max(end, w.To)
	}
	sort.Float64s(edges)
	return centers, edges
}

// snap moves a switch into the nearest pause within reach (else the nearest
// word edge, else leaves it): never inside a word.
func snap(t float64, centers, edges []float64, reach float64) (float64, string) {
	best, bd := t, math.Inf(1)
	for _, c := range centers {
		if d := math.Abs(c - t); d <= reach && d < bd {
			best, bd = c, d
		}
	}
	if !math.IsInf(bd, 1) {
		return best, "in a pause"
	}
	for _, e := range edges {
		if d := math.Abs(e - t); d <= reach && d < bd {
			best, bd = e, d
		}
	}
	if !math.IsInf(bd, 1) {
		return best, "between words"
	}
	return t, ""
}

// snapAfter is the first word edge at or after t within reach (else t).
func snapAfter(t float64, edges []float64, reach float64) (float64, bool) {
	i := sort.SearchFloat64s(edges, t-1e-9)
	if i < len(edges) && edges[i]-t <= reach {
		return edges[i], true
	}
	return t, false
}

// together finds stretches where two or more people talk or laugh at once
// for at least 0.8 s.
// togetherMin is how long two people must be heard at once (talking over each
// other, laughing together) for everyone to be put on screen; shorter overlaps
// are turns changing hands.
const togetherMin = 0.6

func together(in *cutInput) []editspec.Span {
	type ev struct {
		t     float64
		delta int
	}
	var evs []ev
	for _, m := range in.pin.Members {
		if !m.HasSound() {
			continue
		}
		for _, s := range in.view.Activity[m.ID] {
			evs = append(evs, ev{s.From, 1}, ev{s.To, -1})
		}
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].t == evs[j].t {
			return evs[i].delta < evs[j].delta
		}
		return evs[i].t < evs[j].t
	})
	var out []editspec.Span
	n, start := 0, 0.0
	for _, e := range evs {
		was := n
		n += e.delta
		switch {
		case was < 2 && n >= 2:
			start = e.t
		case was >= 2 && n < 2:
			if e.t-start >= togetherMin && e.t > in.from && start < in.to {
				out = append(out, editspec.Span{From: math.Max(start, in.from), To: math.Min(e.t, in.to)})
			}
		}
	}
	return mergeAct(out, 1.0)
}

// shotEvent is one decision: from at, show what, because why.
type shotEvent struct {
	at   float64
	shot editspec.Shot
	pri  int // higher wins a conflict
	// held is the reason when the shot had to wait for the one before (nil:
	// the reason is kept, with a note).
	held func(at float64) string
}

// autoShots chooses the picture for [in.from, in.to): the clip's shots.
func autoShots(in *cutInput) []editspec.Shot {
	st := in.style
	ts := turns(in)
	centers, edges := gaps(in.view)
	pics := in.pin.PictureMembers()
	shown := func(id string) bool {
		if id == in.interviewer {
			return false
		}
		for _, p := range pics {
			if p == id {
				return true
			}
		}
		return false
	}
	name := func(id string) string {
		if n, ok := in.names[id]; ok {
			return n
		}
		return id
	}
	var showable []string
	for _, p := range pics {
		if shown(p) {
			showable = append(showable, p)
		}
	}
	layoutShot := func(why string) editspec.Shot {
		lay := in.layout
		if len(showable) > 3 || (lay == editspec.LayoutSideBySide || lay == editspec.LayoutStacked) && len(showable) > 3 {
			lay = editspec.LayoutGrid
		}
		sh := editspec.Shot{Layout: lay, Rationale: why}
		if len(showable) != len(pics) {
			sh.Members = append([]string(nil), showable...)
		}
		return sh
	}
	var evs []shotEvent
	// Who the picture follows: the floor holder's turns.
	var long []turn
	for _, t := range ts {
		if !t.short {
			long = append(long, t)
		}
	}
	lastSpeaker := ""
	for i, t := range long {
		if !shown(t.member) {
			if in.interviewer != "" && t.member == in.interviewer {
				// The interviewer stays off camera: the guest listens.
				continue
			}
			continue
		}
		if t.member == lastSpeaker {
			continue
		}
		target := t.from + st.JDelay
		kind := "we hear them before we see them"
		if st.LLead > 0 && i > 0 {
			target = math.Max(long[i-1].from+st.MinShot, t.from-st.LLead)
			kind = "the picture goes to them as the other finishes"
		}
		at, where := snap(target, centers, edges, 0.4)
		why := fmt.Sprintf("%s starts talking at %s; the picture follows", name(t.member), editspec.Clock(round1s(t.from)))
		switch {
		case at < t.from:
			why = fmt.Sprintf("%s answers at %s; the picture goes to them %s before (%s)", name(t.member), editspec.Clock(round1s(t.from)), secs(t.from-at), kind)
		case at > t.from:
			why = fmt.Sprintf("%s starts talking at %s; the picture follows %s later (%s)", name(t.member), editspec.Clock(round1s(t.from)), secs(at-t.from), kind)
		}
		if where != "" {
			why += ", " + where
		}
		who, from := name(t.member), t.from
		evs = append(evs, shotEvent{at: at, shot: editspec.Shot{Member: t.member, Rationale: why + "."}, pri: 2,
			held: func(at float64) string {
				return fmt.Sprintf("%s starts talking at %s; the picture follows at %s, once the shot before has had its %s.", who, editspec.Clock(round1s(from)), editspec.Clock(round1s(at)), secs(st.MinShot))
			}})
		lastSpeaker = t.member
	}
	// Talking or laughing together: the split layout, then back.
	if st.Together && len(showable) >= 2 {
		for _, sp := range together(in) {
			a, _ := snap(sp.From-0.15, centers, edges, 0.3)
			evs = append(evs, shotEvent{at: a, shot: layoutShot(fmt.Sprintf("Two people talk or laugh at once at %s: everyone on screen until it settles.", editspec.Clock(round1s(sp.From)))), pri: 3})
			back := sp.To + 0.5
			if who := floorAt(long, back); who != "" && shown(who) {
				b, _ := snap(back, centers, edges, 0.4)
				evs = append(evs, shotEvent{at: b, shot: editspec.Shot{Member: who, Rationale: fmt.Sprintf("Back to %s, who has the floor.", name(who))}, pri: 3})
			}
		}
	}
	// Listening reactions: a short reply or laugh while someone talks.
	if st.ReactAfter > 0 {
		for _, t := range ts {
			if !t.short || !shown(t.member) {
				continue
			}
			holder := floorAt(long, t.from)
			if holder == "" || holder == t.member || !shown(holder) {
				continue
			}
			a, _ := snap(t.from-0.3, centers, edges, 0.3)
			b, _ := snap(math.Max(t.to+1.0, a+st.ReactLen), centers, edges, 0.4)
			what := "reacts"
			if strings.TrimSpace(t.text) != "" {
				what = "says “" + strings.TrimSpace(t.text) + "”"
			}
			evs = append(evs, shotEvent{at: a, shot: editspec.Shot{Member: t.member, Rationale: fmt.Sprintf("%s %s while %s talks: a listening reaction.", name(t.member), what, name(holder))}, pri: 1})
			evs = append(evs, shotEvent{at: b, shot: editspec.Shot{Member: holder, Rationale: fmt.Sprintf("Back to %s.", name(holder))}, pri: 1})
		}
	}
	// The opening: everyone, before the first speaker.
	if st.Open > 0 && len(showable) >= 2 {
		evs = append(evs, shotEvent{at: in.from, shot: layoutShot("Open on everyone before the first person is shown alone."), pri: 4})
	}
	// B-roll during long turns.
	if st.BRoll && len(in.broll) > 0 {
		evs = append(evs, brollEvents(in, long, centers, edges, shown)...)
	}
	shots := resolveEvents(in, evs, long, centers, edges, shown, name)
	return shots
}

func round1s(t float64) float64 { return math.Round(t*10) / 10 }

// floorAt is who holds the floor at t (the long turn covering it, else the
// last one that ended before).
func floorAt(long []turn, t float64) string {
	who := ""
	for _, x := range long {
		if x.from <= t && t < x.to+0.3 {
			return x.member
		}
		if x.to <= t {
			who = x.member
		}
	}
	return who
}

// resolveEvents turns decisions into shots: in time order, the higher
// priority wins when two land closer than the minimum shot, shots stay at
// least MinShot long, and a picture held longer than MaxShot gets a
// listener's reaction at a sentence end.
func resolveEvents(in *cutInput, evs []shotEvent, long []turn, centers, edges []float64, shown func(string) bool, name func(string) string) []editspec.Shot {
	st := in.style
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].at < evs[j].at })
	var shots []editspec.Shot
	var pri []int
	same := func(a, b editspec.Shot) bool {
		return a.Member == b.Member && a.Layout == b.Layout && a.Card == b.Card && (a.BRoll == nil) == (b.BRoll == nil) && strings.Join(a.Members, ",") == strings.Join(b.Members, ",")
	}
	for _, e := range evs {
		at := math.Max(in.from, math.Min(in.to, e.at))
		sh := e.shot
		sh.At = round3(at)
		if n := len(shots); n > 0 {
			last := shots[n-1]
			if same(last, sh) {
				continue
			}
			if at-last.At < st.MinShot-1e-6 {
				if e.pri > pri[n-1] || (n == 1 && at-last.At < 0.05) {
					sh.At = last.At
					shots[n-1], pri[n-1] = sh, e.pri
					// Merge with the one before when it is now the same.
					if n >= 2 && same(shots[n-2], shots[n-1]) {
						shots, pri = shots[:n-1], pri[:n-1]
					}
					continue
				}
				// The shot before is held for its minimum length; this one starts
				// right after it (at the next word edge), if there is still room.
				later, _ := snapAfter(last.At+st.MinShot, edges, 0.4)
				if later >= in.to-st.MinShot*0.6 {
					continue
				}
				at = later
				sh.At = round3(later)
				if e.held != nil {
					sh.Rationale = e.held(later)
				} else {
					sh.Rationale = strings.TrimSuffix(sh.Rationale, ".") + "; held back so the shot before lasts " + secs(st.MinShot) + "."
				}
			}
		} else if at > in.from+0.01 {
			// Before the first decision: whoever talks first, or everyone.
			first := firstShown(long, shown)
			if first == "" {
				first = firstPicture(in, shown)
			}
			if first != "" {
				shots = append(shots, editspec.Shot{At: round3(in.from), Member: first, Rationale: fmt.Sprintf("%s is on screen first.", name(first))})
				pri = append(pri, 0)
				if same(shots[0], sh) {
					continue
				}
				if at-in.from < st.MinShot {
					shots[0] = sh
					shots[0].At = round3(in.from)
					pri[0] = e.pri
					continue
				}
			}
		}
		shots = append(shots, sh)
		pri = append(pri, e.pri)
	}
	if len(shots) == 0 {
		if p := firstPicture(in, shown); p != "" {
			shots = []editspec.Shot{{At: round3(in.from), Member: p, Rationale: fmt.Sprintf("%s is the only picture.", name(p))}}
		}
	}
	// The last shot must not be a flash at the end.
	if n := len(shots); n >= 2 && in.to-shots[n-1].At < st.MinShot*0.6 {
		shots = shots[:n-1]
	}
	// Nothing held too long: a listener at a sentence end (or the split layout).
	if st.MaxShot > 0 {
		shots = breakLong(in, shots, long, shown, name)
	}
	return shots
}

func firstShown(long []turn, shown func(string) bool) string {
	for _, t := range long {
		if shown(t.member) {
			return t.member
		}
	}
	return ""
}

func firstPicture(in *cutInput, shown func(string) bool) string {
	for _, p := range in.pin.PictureMembers() {
		if shown(p) {
			return p
		}
	}
	return ""
}

// breakLong adds a reaction inside any shot held longer than the style's
// MaxShot: the main listener, at the sentence end nearest the middle.
func breakLong(in *cutInput, shots []editspec.Shot, long []turn, shown func(string) bool, name func(string) string) []editspec.Shot {
	st := in.style
	var out []editspec.Shot
	for i, sh := range shots {
		out = append(out, sh)
		end := in.to
		if i+1 < len(shots) {
			end = shots[i+1].At
		}
		if end-sh.At <= st.MaxShot || sh.Member == "" {
			continue
		}
		listener := ""
		for _, p := range in.pin.PictureMembers() {
			if p != sh.Member && shown(p) {
				listener = p
				break
			}
		}
		if listener == "" {
			continue
		}
		var ends []float64
		for _, t := range long {
			if t.member == sh.Member {
				ends = append(ends, t.sentenceAt...)
			}
		}
		for at := sh.At + st.MaxShot*0.6; at < end-st.MinShot*2; at += st.MaxShot * 0.6 {
			t := at
			for _, e := range ends {
				if e > sh.At+st.MinShot && e < end-st.MinShot*2 && math.Abs(e-at) < math.Abs(t-at) {
					t = e
				}
			}
			react := st.ReactLen
			if react <= 0 {
				react = 2
			}
			out = append(out, editspec.Shot{At: round3(t + 0.1), Member: listener, Rationale: fmt.Sprintf("%s has been on screen for %s: %s listening, at a sentence end.", name(sh.Member), secs(t-sh.At), name(listener))})
			out = append(out, editspec.Shot{At: round3(t + 0.1 + math.Max(react, st.MinShot)), Member: sh.Member, Rationale: fmt.Sprintf("Back to %s.", name(sh.Member))})
		}
	}
	return out
}

// brollEvents places cutaways in long turns: 3–5 s, starting at least 3 s
// into the turn and ending 1.5 s before it ends, at most one every 10 s,
// matched to what is said by the footage's words.
func brollEvents(in *cutInput, long []turn, centers, edges []float64, shown func(string) bool) []shotEvent {
	var out []shotEvent
	used := map[string]float64{}
	next := 0
	for _, t := range long {
		if t.to-t.from < 8 || !shown(t.member) {
			continue
		}
		for at := t.from + 3; at+3 <= t.to-1.5; at += 10 {
			end := math.Min(at+4.5, t.to-1.5)
			if end-at < 3 {
				break
			}
			// The footage whose words match what is said around here, else the next one.
			said := map[string]bool{}
			for _, w := range t.words {
				if w.From >= at-4 && w.To <= end+2 {
					said[normWord(w.Text)] = true
				}
			}
			bi, bs := -1, 0
			for i, b := range in.broll {
				s := 0
				for w := range said {
					if len(w) > 3 && b.words[w] {
						s++
					}
				}
				if s > bs {
					bi, bs = i, s
				}
			}
			why := "a cutaway while they talk"
			if bi < 0 {
				bi = next % len(in.broll)
				next++
			} else {
				var hit []string
				for w := range said {
					if len(w) > 3 && in.broll[bi].words[w] {
						hit = append(hit, w)
					}
				}
				sort.Strings(hit)
				if len(hit) > 3 {
					hit = hit[:3]
				}
				why = "they talk about " + strings.Join(hit, ", ")
			}
			b := in.broll[bi]
			from := used[b.id]
			if b.duration > 0 && from+(end-at) > b.duration {
				from = 0
			}
			used[b.id] = from + (end - at)
			a, _ := snap(at, centers, edges, 0.4)
			e, _ := snap(end, centers, edges, 0.4)
			out = append(out, shotEvent{at: a, pri: 2, shot: editspec.Shot{BRoll: &editspec.ShotBRoll{AssetID: b.id, In: round3(from)}, Rationale: fmt.Sprintf("A cutaway to “%s”: %s.", b.title, why)}})
			out = append(out, shotEvent{at: e, pri: 2, shot: editspec.Shot{Member: t.member, Rationale: "Back to " + in.names[t.member] + "."}})
		}
	}
	return out
}

func normWord(w string) string {
	return strings.ToLower(strings.Trim(w, " .,!?;:\"'“”‘’()-"))
}

// questionCards turns an off-camera interviewer's questions into text cards:
// the clips skip each question, and a card clip shows its words for reading
// time with the interviewer's sound left out.
func questionCards(in *cutInput, ts []turn) []plannedClip {
	var out []plannedClip
	cur := in.from
	read := func(text string) float64 {
		n := len(strings.Fields(text))
		return math.Max(2, math.Min(6, 1+0.3*float64(n)))
	}
	prevEnd := in.from
	for _, t := range ts {
		if t.member != in.interviewer || t.short {
			if t.member != in.interviewer {
				prevEnd = math.Max(prevEnd, t.to)
			}
			continue
		}
		r := read(t.text)
		cs := math.Max(t.from, t.to-r)
		if t.to-cs < r {
			cs = math.Max(prevEnd+0.2, t.to-r)
		}
		if cs-cur >= in.style.MinShot {
			out = append(out, plannedClip{in: cur, out: cs})
		}
		text := t.text
		if len([]rune(text)) > 240 {
			text = string([]rune(text)[:239]) + "…"
		}
		out = append(out, plannedClip{in: cs, out: t.to + 0.15, muted: []string{in.interviewer}, why: "The question as a card, without the interviewer's voice.",
			shots: []editspec.Shot{{At: round3(cs), Card: text, Rationale: "The interviewer's question as a text card."}}})
		cur = t.to + 0.15
		prevEnd = t.to
	}
	if in.to-cur > 0.5 {
		out = append(out, plannedClip{in: cur, out: in.to})
	}
	return out
}
