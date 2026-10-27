package cutsheet

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Conversations (D99) in a cut sheet: a section with each pinned conversation
// and its members, conversation clips naming one, and Shot: lines for what
// they show.
//
//	## Conversations
//
//	- call: cnv_01j… · quieter when not speaking by 18 dB · placed to the sample
//	  Member: maya "Maya" · maya-cam · starts at 0:00 · tag "Host"
//	  Member: sam "Sam" · sam-cam · starts at 0:12.345 · drift +20 ppm · crop top 6%, bottom 8% · focus on faces
//
//	## Clips
//
//	1. talk: conversation call 0:00 → 12:30 · without sound from maya
//	   Shot: 0:00 side by side maya, sam · why "Open on both of them."
//	   Shot: 0:15.8 sam · dissolve 0.3 s
//	   Shot: 1:02.5 card "What did you build first?" over sam
//	   Shot: 1:10 b-roll garage from 0:03.5

// layoutWords are the layouts in words (printer) and the words read (parser).
var layoutWords = map[string]string{
	editspec.LayoutSideBySide: "side by side", editspec.LayoutStacked: "stacked",
	editspec.LayoutPiP: "picture in picture", editspec.LayoutGrid: "grid",
}

var layoutAliases = map[string]string{
	"side by side": editspec.LayoutSideBySide, "side-by-side": editspec.LayoutSideBySide, "split": editspec.LayoutSideBySide,
	"stacked": editspec.LayoutStacked, "picture in picture": editspec.LayoutPiP, "pip": editspec.LayoutPiP,
	"grid": editspec.LayoutGrid,
}

// shotKeywords can't be written bare as a member's name in a Shot: line.
var shotKeywords = map[string]bool{"side": true, "stacked": true, "picture": true, "pip": true, "grid": true, "card": true,
	"cutaway": true, "b-roll": true, "broll": true, "member": true, "split": true, "side-by-side": true}

var useWords = map[string]string{editspec.UseBoth: "picture and sound", editspec.UseSound: "sound only", editspec.UsePicture: "picture only"}

// infoFacts lets editspec read the members' recordings through a sheet's Info.
type infoFacts struct{ info Info }

func (f infoFacts) Asset(id string) (editspec.AssetFacts, bool) {
	ft, ok := f.info.Footage(id)
	if !ok {
		return editspec.AssetFacts{}, false
	}
	prec := ""
	if segs := f.info.Transcript(id); segs != nil {
		prec = "segment"
		for _, s := range segs {
			if len(s.Words) > 0 {
				prec = "word"
				break
			}
		}
	}
	return editspec.AssetFacts{ID: id, Name: ft.Title, Kind: ft.Kind, State: "available", InProject: true, Duration: ft.Duration,
		HasVideo: ft.Kind == "video", HasAudio: ft.Kind != "image", TranscriptPrecision: prec}, true
}

func (f infoFacts) Transcript(id string) []editspec.TranscriptSegment { return f.info.Transcript(id) }

// convWords is a conversation's words on its clock and its length, from the
// pin and the sheet's Info (nil without Info).
func convWords(pin editspec.ConversationPin, out editspec.Output, info Info) ([]editspec.TranscriptSegment, float64) {
	if info == nil {
		return nil, 0
	}
	return editspec.PinWords(pin, out, infoFacts{info})
}

// transcript is what is said in a clip's source: footage's transcript, or a
// conversation's words on its clock.
func (p *printer) transcript(assetID string) []editspec.TranscriptSegment {
	if p.o.Info == nil {
		return nil
	}
	if name := editspec.ConvName(assetID); name != "" {
		pin, ok := p.s.Conversations[name]
		if !ok {
			return nil
		}
		ws, _ := convWords(pin, p.s.Output, p.o.Info)
		if ws == nil {
			ws = []editspec.TranscriptSegment{}
		}
		return ws
	}
	return p.o.Info.Transcript(assetID)
}

// ---- printing ----

func (p *printer) conversations() {
	if len(p.s.Conversations) == 0 {
		return
	}
	p.line("")
	p.line("## Conversations")
	for _, name := range sortedKeys(p.s.Conversations) {
		pin := p.s.Conversations[name]
		p.line("")
		p.line("- " + p.convLine(name, pin))
		f := fieldsOf(pin)
		_ = f
		for _, m := range pin.Members {
			p.line("  Member: " + p.memberLine(m))
		}
	}
}

func (p *printer) convLine(name string, pin editspec.ConversationPin) string {
	f := fieldsOf(pin)
	f.take("members")
	head := name + ":"
	if pin.ID != "" && !strings.ContainsAny(pin.ID, " ·|,\"{[") {
		head += " " + pin.ID
		f.take("id")
	}
	var attrs []string
	if g := pin.Gate; g != nil && onlyKeys(g, "mode", "depthDb") {
		switch {
		case g.Mode == editspec.GateSpeaking && g.DepthDB == nil:
			attrs = append(attrs, "quieter when not speaking")
			f.take("gate")
		case g.Mode == editspec.GateSpeaking:
			attrs = append(attrs, "quieter when not speaking by "+fmtNum(*g.DepthDB)+" dB")
			f.take("gate")
		case g.Mode == editspec.GateOff && g.DepthDB == nil:
			attrs = append(attrs, "sound always on")
			f.take("gate")
		}
	}
	switch pin.NameTags {
	case "on":
		attrs = append(attrs, "name tags")
		f.take("nameTags")
	case "off":
		attrs = append(attrs, "no name tags")
		f.take("nameTags")
	}
	if pin.SampleAligned {
		attrs = append(attrs, "placed to the sample")
		f.take("sampleAligned")
	}
	if blk := f.block(); blk != "" {
		attrs = append(attrs, blk)
	}
	if len(attrs) == 0 {
		return head
	}
	return head + " · " + strings.Join(attrs, " · ")
}

func (p *printer) memberLine(m editspec.ConversationMember) string {
	f := fieldsOf(m)
	var head []string
	if editspec.ConversationNamePattern.MatchString(m.ID) {
		head = append(head, m.ID)
		f.take("id")
	}
	head = append(head, quote(m.Name))
	f.take("name")
	parts := []string{strings.Join(head, " ")}
	if m.AssetID != "" {
		parts = append(parts, p.names.name(m.AssetID))
		f.take("assetId")
	}
	if w, ok := useWords[m.Use]; ok {
		parts = append(parts, w)
		f.take("use")
	}
	parts = append(parts, "starts at "+fmtClock(m.Start))
	f.take("start")
	if m.Drift != 0 {
		parts = append(parts, "drift "+signed(m.Drift)+" ppm")
		f.take("drift")
	}
	if m.PersonID != "" && !strings.ContainsAny(m.PersonID, " ·|,\"{[") {
		parts = append(parts, "person "+m.PersonID)
		f.take("personId")
	}
	if m.Tag != "" {
		parts = append(parts, "tag "+quote(m.Tag))
		f.take("tag")
	}
	if c := m.Crop; c != nil && (onlyKeys(c, "left", "top", "right", "bottom") || (*c == editspec.Crop{})) {
		var sides []string
		for _, kv := range []struct {
			k string
			v float64
		}{{"left", c.Left}, {"top", c.Top}, {"right", c.Right}, {"bottom", c.Bottom}} {
			if kv.v != 0 {
				sides = append(sides, kv.k+" "+fmtPercent(kv.v))
			}
		}
		if len(sides) == 0 {
			parts = append(parts, "crop none")
		} else {
			parts = append(parts, "crop "+strings.Join(sides, ", "))
		}
		f.take("crop")
	}
	if fr := m.Framing; fr != nil && onlyKeys(fr, "mode", "auto", "focusX", "focusY", "zoom") && (fr.Mode == "" || fr.Mode == "fill" || fr.Mode == "fit") &&
		(fr.Auto == "" || fr.Auto == editspec.FramingFaces) {
		parts = append(parts, framingWords(fr)...)
		f.take("framing")
	}
	if l := m.Look; l != nil && onlyKeys(l, "exposure", "contrast", "saturation", "warmth", "tint") {
		var ws []string
		for _, kv := range []struct {
			k     string
			v     float64
			ratio bool
		}{{"exposure", l.Exposure, true}, {"contrast", l.Contrast, true}, {"saturation", l.Saturation, true}, {"warmth", l.Warmth, false}, {"tint", l.Tint, false}} {
			if kv.v == 0 {
				continue
			}
			if kv.ratio {
				ws = append(ws, kv.k+" "+fmtPercent(kv.v))
			} else {
				ws = append(ws, kv.k+" "+signedPercent(kv.v))
			}
		}
		parts = append(parts, "look "+strings.Join(ws, ", "))
		f.take("look")
	}
	if m.Muted {
		parts = append(parts, "muted")
		f.take("muted")
	}
	if blk := f.block(); blk != "" {
		parts = append(parts, blk)
	}
	return strings.Join(parts, " · ")
}

// framingWords are a framing's settings in words (as on a clip line).
func framingWords(fr *editspec.Framing) []string {
	var out []string
	if fr.Mode != "" {
		out = append(out, fr.Mode)
	}
	if fr.Auto == editspec.FramingFaces {
		out = append(out, "focus on faces")
	}
	switch {
	case fr.FocusX != nil && fr.FocusY != nil:
		out = append(out, "focus "+fmtPercent(*fr.FocusX)+" across, "+fmtPercent(*fr.FocusY)+" down")
	case fr.FocusX != nil:
		out = append(out, "focus "+fmtPercent(*fr.FocusX)+" across")
	case fr.FocusY != nil:
		out = append(out, "focus "+fmtPercent(*fr.FocusY)+" down")
	}
	if fr.Zoom != 0 {
		out = append(out, "zoom "+fmtNum(fr.Zoom)+"×")
	}
	return out
}

func signed(v float64) string {
	if v > 0 {
		return "+" + fmtNum(v)
	}
	return fmtNum(v)
}

func signedPercent(v float64) string {
	if v > 0 {
		return "+" + fmtPercent(v)
	}
	return fmtPercent(v)
}

// convSourceWords is how a conversation clip names its source.
func convSourceWords(assetID string) (string, bool) {
	name := editspec.ConvName(assetID)
	if name == "" || !editspec.ConversationNamePattern.MatchString(name) {
		return "", false
	}
	return "conversation " + name, true
}

// mutedWords spells a clip's muted members ("without sound from maya, sam").
func mutedWords(ids []string) (string, bool) {
	if len(ids) == 0 {
		return "", false
	}
	for _, id := range ids {
		if !editspec.ConversationNamePattern.MatchString(id) {
			return "", false
		}
	}
	return "without sound from " + strings.Join(ids, ", "), true
}

// shotLines are a conversation clip's Shot: lines.
func (p *printer) shotLines(c editspec.Clip) []string {
	var out []string
	for _, sh := range c.Shots {
		out = append(out, "Shot: "+p.shotLine(sh))
	}
	return out
}

func memberWord(id string) string {
	if shotKeywords[id] {
		return "member " + id
	}
	return id
}

func validIDs(ids []string) bool {
	for _, id := range ids {
		if !editspec.ConversationNamePattern.MatchString(id) {
			return false
		}
	}
	return true
}

func (p *printer) shotLine(sh editspec.Shot) string {
	f := fieldsOf(sh)
	f.take("at")
	what := ""
	switch sh.Kind() {
	case "member":
		if editspec.ConversationNamePattern.MatchString(sh.Member) {
			what = memberWord(sh.Member)
			f.take("member")
		}
	case "layout":
		if w, ok := layoutWords[sh.Layout]; ok && validIDs(sh.Members) {
			what = w
			if len(sh.Members) > 0 {
				what += " " + strings.Join(sh.Members, ", ")
			}
			f.take("layout", "members")
		}
	case "card":
		if validIDs(sh.Members) {
			what = "card " + quote(sh.Card)
			if len(sh.Members) > 0 {
				what += " over " + strings.Join(sh.Members, ", ")
			}
			f.take("card", "members")
		}
	case "broll":
		if b := sh.BRoll; onlyKeys(b, "assetId", "in") && b.AssetID != "" {
			what = "cutaway " + p.names.name(b.AssetID) + " from " + fmtClock(b.In)
			f.take("broll")
		}
	}
	l := fmtClock(sh.At)
	if what != "" {
		l += " " + what
	}
	var attrs []string
	if sh.Dissolve != 0 {
		attrs = append(attrs, "dissolve "+fmtDur(sh.Dissolve))
		f.take("dissolve")
	}
	if sh.Rationale != "" {
		attrs = append(attrs, "why "+quote(sh.Rationale))
		f.take("rationale")
	}
	if blk := f.block(); blk != "" {
		attrs = append(attrs, blk)
	}
	if len(attrs) > 0 {
		l += " · " + strings.Join(attrs, " · ")
	}
	return l
}

// ---- parsing ----

// convItem is a ## Conversations bullet being read: the pin's fields and its
// members (each a sub-item decoded into a member).
func (p *parser) convLine(rest []rune, line, col int) *item {
	it := newItem("conversation", line)
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		it.broken = true
		return it
	}
	segs := splitSeps(ts)
	head := segs[0]
	if len(head) == 0 || head[0].kind != tWord || !strings.HasSuffix(head[0].text, ":") {
		p.errAt(line, col, col+len(rest), "missing_name", "Each conversation starts with its short name, like - call: cnv_… .", "The name is what clips use: 1. talk: conversation call 0:00 → 12:30.")
		it.broken = true
		return it
	}
	name := strings.TrimSuffix(head[0].text, ":")
	if !editspec.ConversationNamePattern.MatchString(name) {
		p.errAt(line, head[0].col, head[0].end-1, "bad_name", fmt.Sprintf("%q can't be a conversation's name.", name), "Names start with a lowercase letter and use letters, digits, - and _ (like call or interview-2).")
		it.broken = true
		return it
	}
	it.id = name
	if len(head) >= 2 {
		if head[1].kind != tWord || len(head) > 2 {
			c0, c1 := spanOf(head[1:])
			p.errAt(line, c0, c1, "unexpected", fmt.Sprintf("Unexpected %q after the conversation's name.", joinWords(head[1:])), "Write the conversation's ID (cnv_…) after its name, then its settings after ·.")
		} else {
			p.set(it, "id", json.RawMessage(quote(head[1].text)), Span{line, head[1].col, head[1].end}, "The conversation")
		}
	}
	for _, seg := range segs[1:] {
		p.convAttr(it, seg, line)
	}
	return it
}

func (p *parser) convAttr(it *item, seg []token, line int) {
	if len(seg) == 0 {
		return
	}
	c0, c1 := spanOf(seg)
	sp := Span{line, c0, c1}
	words := strings.ToLower(joinWords(seg))
	switch {
	case len(seg) == 1 && seg[0].kind == tBlock:
		p.mergeBlock(it, seg[0], line)
	case words == "quieter when not speaking":
		p.set(it, "gate", json.RawMessage(`{"mode":"speaking"}`), sp, "The gate")
	case strings.HasPrefix(words, "quieter when not speaking by "):
		rest := seg[5:]
		if len(rest) == 2 && rest[1].is("db") {
			rest = rest[:1]
		}
		v, ok := 0.0, false
		if len(rest) == 1 {
			v, ok = numberWithSuffix(rest[0], "dB", "db")
		}
		if !ok {
			p.errAt(line, c0, c1, "bad_value", "How much quieter a track gets while its person isn't speaking, like quieter when not speaking by 18 dB.", "")
			return
		}
		p.set(it, "gate", json.RawMessage(`{"mode":"speaking","depthDb":`+fmtNum(v)+`}`), sp, "The gate")
	case words == "sound always on":
		p.set(it, "gate", json.RawMessage(`{"mode":"off"}`), sp, "The gate")
	case words == "name tags":
		p.set(it, "nameTags", json.RawMessage(`"on"`), sp, "Name tags")
	case words == "no name tags":
		p.set(it, "nameTags", json.RawMessage(`"off"`), sp, "Name tags")
	case words == "placed to the sample":
		p.set(it, "sampleAligned", json.RawMessage("true"), sp, "Placing to the sample")
	default:
		p.errAt(line, c0, c1, "unknown_setting", fmt.Sprintf("MetaRush doesn't know %q for a conversation.", joinWords(seg)), "Conversation settings: quieter when not speaking (by 18 dB), sound always on, name tags, no name tags, placed to the sample, or a {name: value} block.")
	}
}

// memberLine reads a Member: line under a conversation.
func (p *parser) memberLine(it *item, rest []rune, line, col int) {
	sub := newItem("member", line)
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		return
	}
	segs := splitSeps(ts)
	head := segs[0]
	k := 0
	if k < len(head) && head[k].kind == tWord {
		if !editspec.ConversationNamePattern.MatchString(head[k].text) {
			p.errAt(line, head[k].col, head[k].end, "bad_name", fmt.Sprintf("%q can't be a member's short name.", head[k].text), "Short names start with a lowercase letter and use letters, digits, - and _ (like maya).")
			return
		}
		sub.id = head[k].text
		p.set(sub, "id", json.RawMessage(quote(head[k].text)), Span{line, head[k].col, head[k].end}, "The short name")
		k++
	}
	if k < len(head) && head[k].kind == tString {
		p.set(sub, "name", json.RawMessage(quote(head[k].text)), Span{line, head[k].col, head[k].end}, "The name")
		k++
	}
	if k != len(head) || len(head) == 0 {
		p.errAt(line, col, col+len(rest), "bad_member", "A member is its short name and the person's name in quotes, then the recording.", `Like: Member: maya "Maya" · maya-cam · starts at 0:00`)
		return
	}
	next := 1
	if len(segs) > 1 && len(segs[1]) == 1 && segs[1][0].kind == tWord && !strings.EqualFold(segs[1][0].text, "muted") {
		if id, ok := p.resolveFootage(segs[1][0], line); ok {
			p.set(sub, "assetId", json.RawMessage(quote(id)), Span{line, segs[1][0].col, segs[1][0].end}, "The recording")
		}
		next = 2
	}
	for _, seg := range segs[next:] {
		p.memberAttr(sub, seg, line)
	}
	it.subs = append(it.subs, sub)
}

func (p *parser) memberAttr(it *item, seg []token, line int) {
	if len(seg) == 0 {
		return
	}
	c0, c1 := spanOf(seg)
	sp := Span{line, c0, c1}
	words := strings.ToLower(joinWords(seg))
	for use, w := range useWords {
		if words == w {
			p.set(it, "use", json.RawMessage(quote(use)), sp, "What the recording gives")
			return
		}
	}
	t := seg[0]
	switch {
	case len(seg) == 1 && t.kind == tBlock:
		p.mergeBlock(it, t, line)
	case len(seg) == 1 && t.is("muted"):
		p.set(it, "muted", json.RawMessage("true"), sp, "muted")
	case t.is("starts"):
		k := 1
		if len(seg) > 1 && seg[1].is("at") {
			k = 2
		}
		if len(seg) != k+1 {
			p.errAt(line, c0, c1, "bad_time", "Where the recording starts on the conversation's clock, like starts at 0:12.345.", "")
			return
		}
		v, ok := parseClock(seg[k].text)
		if !ok {
			v, ok = parseNum(seg[k].text)
		}
		if !ok {
			p.errAt(line, seg[k].col, seg[k].end, "bad_time", "Where the recording starts on the conversation's clock, like starts at 0:12.345.", "")
			return
		}
		p.set(it, "start", jsonNum(v), sp, "The start")
	case t.is("drift"):
		rest := seg[1:]
		if len(rest) == 2 && rest[1].is("ppm") {
			rest = rest[:1]
		}
		v, ok := 0.0, false
		if len(rest) == 1 {
			v, ok = numberWithSuffix(rest[0], "ppm")
		}
		if !ok {
			p.errAt(line, c0, c1, "bad_value", "Clock drift in parts per million, like drift +20 ppm.", "")
			return
		}
		p.set(it, "drift", jsonNum(v), sp, "The drift")
	case t.is("person") && len(seg) == 2 && seg[1].kind == tWord:
		p.set(it, "personId", json.RawMessage(quote(seg[1].text)), sp, "The person")
	case t.is("tag") && len(seg) == 2 && seg[1].kind == tString:
		p.set(it, "tag", json.RawMessage(quote(seg[1].text)), sp, "The name tag")
	case t.is("crop"):
		p.cropAttr(it, seg, line)
	case t.is("look"):
		p.lookAttr(it, seg, line)
	case len(seg) == 1 && t.is("fill", "fit"):
		p.framingSet(it, "mode", json.RawMessage(quote(strings.ToLower(t.text))), sp, "fill or fit")
	case t.is("focus"):
		p.focus(it, seg, line)
	case t.is("zoom") && len(seg) >= 2:
		v, ok := numberWithSuffix(seg[1], "×", "x")
		if !ok || len(seg) > 3 || (len(seg) == 3 && !seg[2].is("×", "x")) {
			p.errAt(line, seg[1].col, seg[len(seg)-1].end, "bad_value", "Zoom is a number like 1.2× (1× shows the whole width).", "")
			return
		}
		p.framingSet(it, "zoom", jsonNum(v), sp, "Zoom")
	default:
		p.errAt(line, c0, c1, "unknown_setting", fmt.Sprintf("MetaRush doesn't know %q for a member.", joinWords(seg)), "Member settings: picture and sound, sound only, picture only, starts at 0:00, drift +20 ppm, person per_…, tag \"…\", crop top 6%, look exposure 104%, fill or fit, focus on faces, focus 40% across, zoom 1.2×, muted, or a {name: value} block.")
	}
}

// cropAttr reads "crop none" or "crop left 6%, top 4%, …".
func (p *parser) cropAttr(it *item, seg []token, line int) {
	c0, c1 := spanOf(seg)
	sp := Span{line, c0, c1}
	ts := seg[1:]
	if len(ts) == 1 && ts[0].is("none") {
		p.set(it, "crop", json.RawMessage("{}"), sp, "The crop")
		return
	}
	m := map[string]json.RawMessage{}
	k := 0
	for k < len(ts) {
		if ts[k].kind == tComma {
			k++
			continue
		}
		if k+1 < len(ts) && ts[k].is("left", "top", "right", "bottom") {
			if v, ok := parsePercent(ts[k+1].text); ok {
				side := strings.ToLower(ts[k].text)
				if _, dup := m[side]; dup {
					break
				}
				m[side] = jsonNum(v)
				k += 2
				continue
			}
		}
		break
	}
	if k != len(ts) || len(m) == 0 {
		p.errAt(line, c0, c1, "bad_value", "A crop says how much of each edge is removed, like crop top 6%, bottom 8% (or crop none).", "")
		return
	}
	b, _ := json.Marshal(m)
	p.set(it, "crop", b, sp, "The crop")
}

// lookAttr reads "look exposure 106%, contrast 98%, warmth -3%".
func (p *parser) lookAttr(it *item, seg []token, line int) {
	c0, c1 := spanOf(seg)
	sp := Span{line, c0, c1}
	ts := seg[1:]
	m := map[string]json.RawMessage{}
	k := 0
	for k < len(ts) {
		if ts[k].kind == tComma {
			k++
			continue
		}
		if k+1 < len(ts) && ts[k].is("exposure", "contrast", "saturation", "warmth", "tint") {
			if v, ok := parsePercent(strings.TrimPrefix(ts[k+1].text, "+")); ok {
				key := strings.ToLower(ts[k].text)
				if _, dup := m[key]; dup {
					break
				}
				m[key] = jsonNum(v)
				k += 2
				continue
			}
		}
		break
	}
	if k != len(ts) || len(m) == 0 {
		p.errAt(line, c0, c1, "bad_value", "A look is exposure, contrast and saturation as percentages (100% unchanged) and warmth and tint as changes, like look exposure 106%, warmth -3%.", "")
		return
	}
	b, _ := json.Marshal(m)
	p.set(it, "look", b, sp, "The look")
}

// shotLine reads a Shot: line under a conversation clip.
func (p *parser) shotLine(it *item, rest []rune, line, col int) {
	sub := newItem("shot", line)
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		return
	}
	segs := splitSeps(ts)
	head := segs[0]
	if len(head) == 0 || head[0].kind != tWord {
		p.errAt(line, col, col+len(rest), "bad_shot", "A shot starts with when it starts on the conversation's clock, then what it shows.", `Like: Shot: 0:15.8 sam · why "Sam answers."`)
		return
	}
	at, ok := parseClock(head[0].text)
	if !ok || at < 0 {
		p.errAt(line, head[0].col, head[0].end, "bad_time", "When the shot starts on the conversation's clock, like 0:15.8.", "")
		return
	}
	p.set(sub, "at", jsonNum(at), Span{line, head[0].col, head[0].end}, "The shot's start")
	what := head[1:]
	sp := func(ts []token) Span { a, b := spanOf(ts); return Span{line, a, b} }
	names := func(ts []token) ([]string, bool) {
		var out []string
		for k, t := range ts {
			if k%2 == 1 {
				if t.kind != tComma {
					return nil, false
				}
				continue
			}
			if t.kind != tWord || !editspec.ConversationNamePattern.MatchString(t.text) {
				return nil, false
			}
			out = append(out, t.text)
		}
		return out, len(ts)%2 == 1
	}
	list := func(ids []string) json.RawMessage { b, _ := json.Marshal(ids); return b }
	switch {
	case len(what) == 0:
	case what[0].is("member") && len(what) == 2 && what[1].kind == tWord:
		p.set(sub, "member", json.RawMessage(quote(what[1].text)), sp(what), "Who is shown")
	case what[0].is("card"):
		if len(what) < 2 || what[1].kind != tString {
			p.errAt(line, what[0].col, what[len(what)-1].end, "bad_shot", "A card shows text: card \"What did you build first?\" (over sam).", "")
			return
		}
		p.set(sub, "card", json.RawMessage(quote(what[1].text)), sp(what[:2]), "The card")
		if len(what) > 2 {
			ids, ok := []string(nil), false
			if what[2].is("over") {
				ids, ok = names(what[3:])
			}
			if !ok {
				p.errAt(line, what[2].col, what[len(what)-1].end, "bad_shot", "After the card's text: over <who is seen behind it>.", "")
				return
			}
			p.set(sub, "members", list(ids), sp(what[2:]), "Who is behind the card")
		}
	case what[0].is("cutaway", "b-roll", "broll"):
		if len(what) != 4 || what[1].kind != tWord || !what[2].is("from") {
			p.errAt(line, what[0].col, what[len(what)-1].end, "bad_shot", "A cutaway names the footage and where in it to start: cutaway garage from 0:03.5.", "")
			return
		}
		id, ok := p.resolveFootage(what[1], line)
		if !ok {
			return
		}
		in, ok := parseClock(what[3].text)
		if !ok {
			p.errAt(line, what[3].col, what[3].end, "bad_time", "Where in the footage the cutaway starts, like from 0:03.5.", "")
			return
		}
		b, _ := json.Marshal(map[string]any{"assetId": id, "in": in})
		p.set(sub, "broll", b, sp(what), "The cutaway")
	default:
		// A layout (its words, then who) or one member.
		matched := false
		for n := min(3, len(what)); n >= 1 && !matched; n-- {
			if lay, ok := layoutAliases[strings.ToLower(joinWords(what[:n]))]; ok {
				matched = true
				p.set(sub, "layout", json.RawMessage(quote(lay)), sp(what[:n]), "The layout")
				if len(what) > n {
					ids, ok := names(what[n:])
					if !ok {
						p.errAt(line, what[n].col, what[len(what)-1].end, "bad_shot", "After a layout: who is in it, in order (side by side maya, sam).", "")
						return
					}
					p.set(sub, "members", list(ids), sp(what[n:]), "Who is in the layout")
				}
			}
		}
		if matched {
			break
		}
		if len(what) == 1 && what[0].kind == tWord && editspec.ConversationNamePattern.MatchString(what[0].text) {
			p.set(sub, "member", json.RawMessage(quote(what[0].text)), sp(what), "Who is shown")
			break
		}
		p.errAt(line, what[0].col, what[len(what)-1].end, "bad_shot", fmt.Sprintf("MetaRush doesn't know what %q shows.", joinWords(what)), "A shot shows one person (sam), a layout (side by side, stacked, picture in picture, grid, with who), a card \"…\" or a cutaway <footage> from 0:03.")
		return
	}
	for _, seg := range segs[1:] {
		if len(seg) == 0 {
			continue
		}
		c0, c1 := spanOf(seg)
		s := Span{line, c0, c1}
		switch {
		case len(seg) == 1 && seg[0].kind == tBlock:
			p.mergeBlock(sub, seg[0], line)
		case seg[0].is("dissolve"):
			v, ok := readDuration(seg[1:])
			if !ok {
				p.errAt(line, c0, c1, "bad_value", "How long the switch blends, like dissolve 0.3 s.", "")
				return
			}
			p.set(sub, "dissolve", jsonNum(v), s, "The dissolve")
		case seg[0].is("why") && len(seg) == 2 && seg[1].kind == tString:
			p.set(sub, "rationale", json.RawMessage(quote(seg[1].text)), s, "Why")
		default:
			p.errAt(line, c0, c1, "unknown_setting", fmt.Sprintf("MetaRush doesn't know %q for a shot.", joinWords(seg)), "Shot settings: dissolve 0.3 s, why \"…\", or a {name: value} block.")
			return
		}
	}
	var sh editspec.Shot
	if !p.decodeItem(sub, &sh) {
		return
	}
	b, _ := json.Marshal(sh)
	it.shots = append(it.shots, b)
}

// convSource reads `conversation <name>` at head[i] of a clip line.
func (p *parser) convSource(head []token, i int) (string, bool) {
	if i+1 >= len(head) || !head[i].is("conversation") || head[i+1].kind != tWord || looksLikeTime(head[i+1].text) {
		return "", false
	}
	name := head[i+1].text
	if !editspec.ConversationNamePattern.MatchString(name) {
		return "", false
	}
	return editspec.ConvKey(name), true
}

// mutedAttr reads "without sound from maya, sam" on a clip line.
func (p *parser) mutedAttr(it *item, seg []token, line int) bool {
	if len(seg) < 4 || !seg[0].is("without") || !seg[1].is("sound") || !seg[2].is("from") {
		return false
	}
	c0, c1 := spanOf(seg)
	var ids []string
	for k, t := range seg[3:] {
		if k%2 == 1 {
			if t.kind != tComma {
				p.errAt(line, c0, c1, "bad_value", "Name the members whose sound this clip leaves out: without sound from maya, sam.", "")
				return true
			}
			continue
		}
		if t.kind != tWord || !editspec.ConversationNamePattern.MatchString(t.text) {
			p.errAt(line, t.col, t.end, "bad_value", "Name the members whose sound this clip leaves out by their short names: without sound from maya.", "")
			return true
		}
		ids = append(ids, t.text)
	}
	if len(seg[3:])%2 == 0 {
		p.errAt(line, c0, c1, "bad_value", "Name the members whose sound this clip leaves out: without sound from maya, sam.", "")
		return true
	}
	b, _ := json.Marshal(ids)
	p.set(it, "mutedMembers", b, Span{line, c0, c1}, "The members left out")
	return true
}

// convTranscript is a conversation clip's words for quoted words: the pin
// read so far (its section comes before ## Clips) through the sheet's Info.
func (p *parser) convTranscript(assetID string) ([]editspec.TranscriptSegment, float64, bool) {
	name := editspec.ConvName(assetID)
	if name == "" {
		return nil, 0, false
	}
	for _, it := range p.convs {
		if it.id != name || it.broken {
			continue
		}
		pin, ok := p.pinOf(it, false)
		if !ok {
			return nil, 0, true
		}
		var out editspec.Output
		if p.output != nil {
			b, _ := json.Marshal(p.output)
			_ = json.Unmarshal(b, &out)
		}
		ws, end := convWords(pin, out, p.o.Info)
		return ws, end, true
	}
	return nil, 0, true
}

// pinOf decodes a ## Conversations item (reporting problems when report).
func (p *parser) pinOf(it *item, report bool) (editspec.ConversationPin, bool) {
	var members []string
	for _, sub := range it.subs {
		var m editspec.ConversationMember
		if report {
			if !p.decodeItem(sub, &m) {
				return editspec.ConversationPin{}, false
			}
		} else {
			cp := *sub
			cp.m = map[string]json.RawMessage{}
			for k, v := range sub.m {
				cp.m[k] = v
			}
			if len(sub.framing) > 0 {
				b, _ := json.Marshal(sub.framing)
				cp.m["framing"] = b
			}
			b, _ := json.Marshal(cp.m)
			if strictDecode(b, &m) != nil {
				return editspec.ConversationPin{}, false
			}
		}
		b, _ := json.Marshal(m)
		members = append(members, string(b))
	}
	m := map[string]json.RawMessage{}
	for k, v := range it.m {
		m[k] = v
	}
	if _, dup := m["members"]; dup && len(members) > 0 {
		if report {
			p.errAt(it.line, 1, 2, "duplicate", "The members are given twice (as Member: lines and in the {…} block).", "Keep the Member: lines.")
		}
		return editspec.ConversationPin{}, false
	}
	if len(members) > 0 || m["members"] == nil {
		m["members"] = json.RawMessage("[" + strings.Join(members, ",") + "]")
	}
	b, _ := json.Marshal(m)
	var pin editspec.ConversationPin
	if err := strictDecode(b, &pin); err != nil {
		if report {
			p.errAt(it.line, 1, 2, "bad_value", friendlyDecode(err, "conversation "+it.id), "")
		}
		return editspec.ConversationPin{}, false
	}
	return pin, true
}

// assembleConversations puts the ## Conversations section into the spec.
func (p *parser) assembleConversations(spec *editspec.Spec) {
	seen := map[string]int{}
	for _, it := range p.convs {
		if it.broken {
			continue
		}
		if prev, dup := seen[it.id]; dup {
			p.errAt(it.line, 1, 2, "duplicate", fmt.Sprintf("There are two conversations called %s (the first is on line %d).", it.id, prev), "Give each its own name.")
			continue
		}
		seen[it.id] = it.line
		pin, ok := p.pinOf(it, true)
		if !ok {
			continue
		}
		if spec.Conversations == nil {
			spec.Conversations = map[string]editspec.ConversationPin{}
		}
		spec.Conversations[it.id] = pin
	}
	if p.convSection > 0 && spec.Conversations == nil {
		spec.Conversations = map[string]editspec.ConversationPin{}
	}
}

// sortedConvNames lists a spec's conversation names.
func sortedConvNames(s *editspec.Spec) []string {
	names := make([]string, 0, len(s.Conversations))
	for n := range s.Conversations {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// withoutMembers drops the words of a clip's muted members (they aren't heard).
func withoutMembers(segs []editspec.TranscriptSegment, pin editspec.ConversationPin, muted []string) []editspec.TranscriptSegment {
	names := map[string]bool{}
	for _, id := range muted {
		if m, ok := pin.Member(id); ok {
			names[m.Name] = true
		}
	}
	out := make([]editspec.TranscriptSegment, 0, len(segs))
	for _, s := range segs {
		if !names[s.Speaker] {
			out = append(out, s)
		}
	}
	return out
}
