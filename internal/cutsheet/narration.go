package cutsheet

// Narration and the mix in a cut sheet.
//
// The narration is the screenplay part of a sheet: `## Voiceover` lists the
// lines in order, each with its words and a start point in plain words
// ("0.5 s after hook starts", "after the previous line", "at interview-a 0:12.3
// in hook", "at 0:14.5"), with Delivery:, Take:, Why: and Evidence: lines under
// it, and an optional `Recording:` line for one continuous read. Settings that
// are about the narration as a whole (layout, speaking rate, fitting, the
// default pause, the lock) and the mix (bus levels, camera sound, ducking,
// loudness) are groups in the settings at the top. Every field has a friendly
// spelling; anything that cannot be written that way exactly travels in a {…}
// block, so the round trip stays lossless.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/editspec"
)

var (
	enumState      = tagEnum(editspec.Beat{}, "State")
	enumRateSource = tagEnum(editspec.VoiceoverSettings{}, "RateSource")
	enumFitPolicy  = tagEnum(editspec.VoiceoverSettings{}, "FitPolicy")
	enumTakeSource = tagEnum(editspec.TakeRef{}, "Source")

	// buses in mixer order, with the words the sheet uses for them.
	busNames = []struct{ json, word string }{
		{"source", "clip sound"}, {"voiceover", "narration"}, {"music", "music"}, {"sfx", "sound effects"},
	}
	busAliases = map[string]string{
		"clip sound": "source", "clips sound": "source", "source": "source",
		"narration": "voiceover", "voiceover": "voiceover", "voice over": "voiceover",
		"music":         "music",
		"sound effects": "sfx", "effects": "sfx", "sfx": "sfx",
	}
	takeSourceWord  = map[string]string{"scratch": "scratch", "tts": "generated", "recorded": "recorded", "imported": "imported"}
	takeSourceAlias = map[string]string{"scratch": "scratch", "generated": "tts", "tts": "tts", "recorded": "recorded", "imported": "imported"}
	voiceProvider   = map[string]string{"local": "scratch", "elevenlabs": "elevenlabs"}
	providerAlias   = map[string]string{"scratch": "local", "local": "local", "elevenlabs": "elevenlabs", "eleven": "elevenlabs"}

	recordingRe = regexp.MustCompile(`(?i)^recording\s*:\s?`)
	compactRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

const beatSettings = `a start point (when hook starts, 0.5 s after hook ends, after the previous line, at 0:12.4, at interview-a 0:12.3), voice scratch | voice elevenlabs "Name" id …, volume -2 dB, status draft, locked, or a {name: value} block`

// ---- printing: settings groups ----

func (p *printer) voiceoverSettings() {
	v := p.s.Voiceover
	if v == nil {
		return
	}
	f := fieldsOf(v)
	f.take("beats", "continuous") // written in ## Voiceover
	if v.Original != "" && originalFence(v.Original) != "" {
		f.take("original") // ### Original script in ## Voiceover
	}
	var lines []string
	switch v.Active {
	case editspec.VersionOriginal, editspec.VersionFitted:
		lines = append(lines, "script: "+v.Active)
		f.take("active")
	}
	switch v.Layout {
	case editspec.LayoutBeats:
		lines = append(lines, "layout: line by line")
		f.take("layout")
	case editspec.LayoutContinuous:
		lines = append(lines, "layout: one recording")
		f.take("layout")
	}
	if st := v.Settings; st != nil {
		if sl, ok := settingsWords(st); ok && len(sl) > 0 {
			lines = append(lines, sl...)
			f.take("settings")
		}
	}
	if v.Locked {
		lines = append(lines, "locked: true")
		f.take("locked")
	}
	if len(lines) == 0 && f.block() == "" {
		return
	}
	p.line("voiceover:")
	for _, l := range lines {
		p.line("  " + l)
	}
	p.leftoverKeys(f)
}

func settingsWords(st *editspec.VoiceoverSettings) ([]string, bool) {
	f := fieldsOf(st)
	var out []string
	if st.SpeakingRateWPM != 0 {
		out = append(out, "speaking rate: "+fmtNum(st.SpeakingRateWPM)+" words a minute")
		f.take("speakingRateWpm")
	}
	if st.RateSource != "" {
		if !contains(enumRateSource, st.RateSource) {
			return nil, false
		}
		out = append(out, "rate from: "+st.RateSource)
		f.take("rateSource")
	}
	if st.FitPolicy != "" {
		if !contains(enumFitPolicy, st.FitPolicy) {
			return nil, false
		}
		out = append(out, "fitting: "+st.FitPolicy)
		f.take("fitPolicy")
	}
	if st.Gap != 0 {
		out = append(out, "pause: "+fmtDur(st.Gap))
		f.take("gap")
	}
	if f.block() != "" {
		return nil, false
	}
	return out, true
}

func (p *printer) mixSettings() {
	m := p.s.Mix
	if m == nil {
		return
	}
	f := fieldsOf(m)
	var lines []string
	if m.MuteSource {
		lines = append(lines, "camera sound: off")
		f.take("muteSource")
	}
	for _, b := range busNames {
		bus := busOf(m, b.json)
		if bus == nil {
			continue
		}
		if w, ok := busWords(bus); ok {
			lines = append(lines, b.word+": "+w)
			f.take(b.json)
		}
	}
	if d := m.Ducking; d != nil {
		if dl, ok := duckingWords(d); ok {
			lines = append(lines, dl...)
			f.take("ducking")
		}
	}
	if l := m.Loudness; l != nil {
		if ll, ok := loudnessWords(l); ok {
			lines = append(lines, ll...)
			f.take("loudness")
		}
	}
	if m.LevelSpeech != nil {
		lines = append(lines, "even out voices: "+onOff(*m.LevelSpeech))
		f.take("levelSpeech")
	}
	if m.CutBlend != nil {
		lines = append(lines, "blend at cuts: "+fmtDur(*m.CutBlend))
		f.take("cutBlend")
	}
	if len(m.Voices) > 0 {
		if vl, ok := p.voicesWords(m.Voices); ok {
			lines = append(lines, vl...)
			f.take("voices")
		}
	}
	if m.Locked {
		lines = append(lines, "locked: true")
		f.take("locked")
	}
	if len(lines) == 0 && f.block() == "" {
		p.line("mix: {}")
		return
	}
	p.line("mix:")
	for _, l := range lines {
		p.line("  " + l)
	}
	p.leftoverKeys(f)
}

func busOf(m *editspec.Mix, name string) *editspec.Bus {
	switch name {
	case "source":
		return m.Source
	case "voiceover":
		return m.Voiceover
	case "music":
		return m.Music
	case "sfx":
		return m.SFX
	}
	return nil
}

func busWords(b *editspec.Bus) (string, bool) {
	f := fieldsOf(b)
	var parts []string
	if b.GainDB != nil {
		parts = append(parts, fmtNum(*b.GainDB)+" dB")
		f.take("gainDb")
	}
	if b.Mute {
		parts = append(parts, "muted")
		f.take("mute")
	}
	if b.Solo {
		parts = append(parts, "solo")
		f.take("solo")
	}
	if f.block() != "" {
		return "", false
	}
	if len(parts) == 0 {
		return "normal", true
	}
	return strings.Join(parts, ", "), true
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func duckingWords(d *editspec.Ducking) ([]string, bool) {
	f := fieldsOf(d)
	switch {
	case len(f.members) == 0:
		return []string{"ducking: {}"}, true
	case len(f.members) == 1 && d.Enabled != nil:
		return []string{"ducking: " + onOff(*d.Enabled)}, true
	}
	lines := []string{"ducking:"}
	if d.Enabled != nil {
		lines = append(lines, "  on: "+yamlBool(*d.Enabled))
		f.take("enabled")
	}
	add := func(v *float64, key, word, unit string) {
		if v != nil {
			lines = append(lines, "  "+word+": "+fmtNum(*v)+unit)
			f.take(key)
		}
	}
	add(d.MusicUnderVoiceoverDB, "musicUnderVoiceoverDb", "music under narration", " dB")
	add(d.SourceUnderVoiceoverDB, "sourceUnderVoiceoverDb", "clip sound under narration", " dB")
	add(d.MusicUnderDialogueDB, "musicUnderDialogueDb", "music under speech", " dB")
	add(d.Attack, "attack", "dip time", " s")
	add(d.Release, "release", "recovery", " s")
	if f.block() != "" {
		return nil, false
	}
	return lines, true
}

func loudnessWords(l *editspec.MasterLoudness) ([]string, bool) {
	f := fieldsOf(l)
	switch {
	case len(f.members) == 0:
		return []string{"loudness: {}"}, true
	case len(f.members) == 1 && l.TargetLUFS != nil:
		return []string{"loudness: " + fmtNum(*l.TargetLUFS) + " LUFS"}, true
	}
	lines := []string{"loudness:"}
	if l.TargetLUFS != nil {
		lines = append(lines, "  target: "+fmtNum(*l.TargetLUFS)+" LUFS")
		f.take("targetLufs")
	}
	if l.TruePeakDBTP != nil {
		lines = append(lines, "  true peak: "+fmtNum(*l.TruePeakDBTP)+" dBTP")
		f.take("truePeakDbtp")
	}
	if l.Normalize != nil {
		lines = append(lines, "  normalize: "+yamlBool(*l.Normalize))
		f.take("normalize")
	}
	if f.block() != "" {
		return nil, false
	}
	return lines, true
}

// ---- printing: ## Voiceover ----

func (p *printer) voiceover() {
	v := p.s.Voiceover
	if v == nil {
		return
	}
	p.line("")
	p.line("## Voiceover")
	if c := v.Continuous; c != nil {
		p.line("")
		p.line("Recording: " + p.recordingWords(*c))
	}
	var timing map[string]string
	if lt, ok := p.o.Info.(LineTimer); ok {
		timing = lt.LineTimes(p.s)
	}
	for _, b := range v.Beats {
		p.line("")
		p.line("- " + p.beatWords(b))
		if t := oneLine(timing[b.ID]); t != "" {
			p.line("  > " + t)
		}
		if b.Delivery != "" {
			p.line("  Delivery: " + freeText(b.Delivery))
		}
		if b.Take != nil {
			p.line("  Take: " + p.takeWords(*b.Take))
		}
		p.itemBlock("  ", b.Rationale, b.Evidence, "", true)
	}
	// The script exactly as the person gave it (D86), between fences.
	if fence := originalFence(v.Original); v.Original != "" && fence != "" {
		p.line("")
		p.line("### Original script")
		p.line("")
		p.line(fence + "text")
		p.b.WriteString(v.Original)
		p.b.WriteByte('\n')
		p.line(fence)
	}
}

func (p *printer) beatWords(b editspec.Beat) string {
	f := fieldsOf(b)
	f.take("text", "delivery", "rationale", "evidence", "take")
	head := ""
	if validClipID(b.ID) {
		head = b.ID + ": "
		f.take("id")
	}
	head += quote(b.Text)
	var attrs []string
	if ph, ok := p.anchorWords(b.Anchor); ok {
		attrs = append(attrs, ph)
		f.take("anchor")
	}
	if b.Voice != nil {
		if w, ok := voiceWords(*b.Voice); ok {
			attrs = append(attrs, w)
			f.take("voice")
		}
	}
	if b.GainDB != nil {
		attrs = append(attrs, "volume "+fmtNum(*b.GainDB)+" dB")
		f.take("gainDb")
	}
	if b.State != "" && contains(enumState, b.State) {
		attrs = append(attrs, "status "+b.State)
		f.take("state")
	}
	if b.Locked {
		attrs = append(attrs, "locked")
		f.take("locked")
	}
	if blk := f.block(); blk != "" {
		attrs = append(attrs, blk)
	}
	if len(attrs) == 0 {
		return head
	}
	return head + " · " + strings.Join(attrs, " · ")
}

// audioWords prints the shared part of a take and a recording: the audio
// range, how it was made, the cleaned-up version and which one plays.
func (p *printer) audioWords(f *fields, assetID string, in, out float64, source string, aiVoice bool, pr *editspec.ProcessedRef, useProcessed bool, mid []string) []string {
	var parts []string
	if assetID != "" {
		parts = append(parts, p.names.name(assetID)+" "+fmtClock(in)+" → "+fmtClock(out))
		f.take("assetId", "in", "out")
	}
	parts = append(parts, mid...)
	if w, ok := takeSourceWord[source]; ok {
		parts = append(parts, w)
		f.take("source")
	}
	if aiVoice {
		parts = append(parts, "AI voice")
		f.take("aiVoice")
	}
	if pr != nil && pr.AssetID != "" && fieldsOnly(pr, "assetId", "preset", "offset") {
		s := "cleaned version " + p.names.name(pr.AssetID) + " " + quote(pr.Preset)
		if pr.Offset != 0 {
			s += " shifted " + fmtDur(pr.Offset)
		}
		parts = append(parts, s)
		f.take("processed")
	}
	if useProcessed {
		parts = append(parts, "plays the cleaned version")
		f.take("useProcessed")
	}
	return parts
}

// fieldsOnly reports whether v has no JSON members other than keys.
func fieldsOnly(v any, keys ...string) bool {
	f := fieldsOf(v)
	f.take(keys...)
	return f.block() == ""
}

func (p *printer) takeWords(t editspec.TakeRef) string {
	f := fieldsOf(t)
	var mid []string
	if t.Speed != 0 {
		mid = append(mid, "speed "+fmtNum(t.Speed)+"×")
		f.take("speed")
	}
	parts := p.audioWords(f, t.AssetID, t.In, t.Out, t.Source, t.AIVoice, t.Processed, t.UseProcessed, mid)
	if t.TakeID != "" && compactRe.MatchString(t.TakeID) {
		parts = append(parts, "id "+t.TakeID)
		f.take("takeId")
	}
	if blk := f.block(); blk != "" {
		parts = append(parts, blk)
	}
	return strings.Join(parts, " · ")
}

func (p *printer) recordingWords(c editspec.ContinuousVO) string {
	f := fieldsOf(c)
	var mid []string
	if ph, ok := p.anchorWords(c.Anchor); ok {
		mid = append(mid, ph)
		f.take("anchor")
	}
	if c.GainDB != nil {
		mid = append(mid, "volume "+fmtNum(*c.GainDB)+" dB")
		f.take("gainDb")
	}
	parts := p.audioWords(f, c.AssetID, c.In, c.Out, c.Source, c.AIVoice, c.Processed, c.UseProcessed, mid)
	if blk := f.block(); blk != "" {
		parts = append(parts, blk)
	}
	return strings.Join(parts, " · ")
}

func voiceWords(v editspec.VoiceRef) (string, bool) {
	f := fieldsOf(v)
	pw, ok := voiceProvider[v.Provider]
	if !ok {
		return "", false
	}
	f.take("provider")
	s := "voice " + pw
	if v.Name != "" {
		s += " " + quote(v.Name)
		f.take("name")
	}
	if v.VoiceID != "" {
		if !compactRe.MatchString(v.VoiceID) {
			return "", false
		}
		s += " id " + v.VoiceID
		f.take("voiceId")
	}
	if v.ModelID != "" {
		if !compactRe.MatchString(v.ModelID) {
			return "", false
		}
		s += " model " + v.ModelID
		f.take("modelId")
	}
	if f.block() != "" {
		return "", false // voice settings: the whole voice goes in the block
	}
	return s, true
}

// anchorWords is a start point in words, when one of the friendly forms says
// exactly the same thing (checked by reading it back).
func (p *printer) anchorWords(a editspec.Anchor) (string, bool) {
	s, ok := anchorPhrase(a, p.names.name)
	if !ok {
		return "", false
	}
	ts, prob := tokenize([]rune(s), 1)
	if prob != nil {
		return "", false
	}
	got, matched, msg := readAnchor(ts, p.footageID)
	if !matched || msg != "" || !jsonEqual(got, a) {
		return "", false
	}
	return s, true
}

// footageID maps a short name (or an ast_ ID) back to its asset, the way the
// parser will.
func (p *printer) footageID(name string) (string, bool) {
	if assetIDRe.MatchString(name) {
		return name, true
	}
	for id, n := range p.names.byID {
		if n == name {
			return id, true
		}
	}
	return "", false
}

func secondsAfter(v float64, word string) string {
	if v < 0 {
		return fmtNum(-v) + " s before " + word
	}
	return fmtNum(v) + " s after " + word
}

func anchorPhrase(a editspec.Anchor, name func(string) string) (string, bool) {
	switch a.Kind {
	case editspec.AnchorClip:
		if a.ClipID == "" || a.AssetID != "" || a.At != nil || a.Quote != "" || a.Ref != "" || a.Gap != nil {
			return "", false
		}
		verb := "starts"
		switch a.Edge {
		case "":
		case "end":
			verb = "ends"
		default:
			return "", false
		}
		if a.Offset == 0 {
			return "when " + a.ClipID + " " + verb, true
		}
		return secondsAfter(a.Offset, a.ClipID+" "+verb), true
	case editspec.AnchorAfter:
		if a.ClipID != "" || a.AssetID != "" || a.At != nil || a.Quote != "" || a.Ref != "" || a.Edge != "" {
			return "", false
		}
		switch {
		case a.Gap == nil && a.Offset == 0:
			return "after the previous line", true
		case a.Gap != nil && a.Offset == 0 && *a.Gap == 0:
			return "right after the previous line", true
		case a.Gap != nil && a.Offset == 0 && *a.Gap > 0:
			return fmtNum(*a.Gap) + " s after the previous line", true
		case a.Gap == nil && a.Offset < 0:
			return fmtNum(-a.Offset) + " s before the previous line ends", true
		}
		return "", false
	case editspec.AnchorTime:
		if a.At == nil || a.Offset != 0 || a.ClipID != "" || a.AssetID != "" || a.Quote != "" || a.Ref != "" || a.Edge != "" || a.Gap != nil {
			return "", false
		}
		return "at " + fmtClock(*a.At), true
	case editspec.AnchorMoment:
		if a.AssetID == "" || a.At == nil || a.Edge != "" || a.Gap != nil {
			return "", false
		}
		s := name(a.AssetID) + " " + fmtClock(*a.At)
		if a.ClipID != "" {
			s += " in " + a.ClipID
		}
		if a.Quote != "" {
			s += " " + quote(a.Quote)
		}
		if a.Ref != "" {
			s += " ref " + quote(a.Ref)
		}
		if a.Offset == 0 {
			return "at " + s, true
		}
		return secondsAfter(a.Offset, s), true
	}
	return "", false
}

// ---- reading start points ----

// durationAt reads "0.5 s" or "0.5s" at ts[i] (a unit is required). It returns
// the value and the index after it.
func durationAt(ts []token, i int) (float64, int, bool) {
	if i >= len(ts) || ts[i].kind != tWord {
		return 0, i, false
	}
	if m := durRe.FindStringSubmatch(ts[i].text); m != nil && m[2] != "" {
		v, ok := parseNum(m[1])
		return v, i + 1, ok
	}
	if i+1 < len(ts) && ts[i+1].is("s", "sec", "secs", "second", "seconds") {
		v, ok := parseNum(ts[i].text)
		return v, i + 2, ok
	}
	return 0, i, false
}

// timeAt reads a clock time (0:12.5, 12.5s or 12.5 s) at ts[i].
func timeAt(ts []token, i int) (float64, int, bool) {
	if i >= len(ts) || ts[i].kind != tWord {
		return 0, i, false
	}
	if v, ok := parseClock(ts[i].text); ok {
		return v, i + 1, true
	}
	if i+1 < len(ts) && ts[i+1].is("s", "sec", "secs", "second", "seconds") {
		if v, ok := parseNum(ts[i].text); ok {
			return v, i + 2, true
		}
	}
	return 0, i, false
}

// previousLine matches "the previous line" (optionally "ends") from ts[i] to
// the end.
func previousLine(ts []token, i int, ends bool) bool {
	words := []string{}
	for _, t := range ts[i:] {
		if t.kind != tWord {
			return false
		}
		words = append(words, strings.ToLower(t.text))
	}
	s := strings.Join(words, " ")
	s = strings.TrimPrefix(s, "the ")
	want := "previous line"
	if ends {
		want += " ends"
	}
	return s == want || s == strings.Replace(want, "previous", "last", 1)
}

// readAnchor reads a start point in words. matched is false when the segment
// is not a start point at all (so it can be another setting); msg explains a
// start point that could not be read.
func readAnchor(seg []token, footage func(string) (string, bool)) (editspec.Anchor, bool, string) {
	var a editspec.Anchor
	n := len(seg)
	if n == 0 {
		return a, false, ""
	}
	is := func(i int, words ...string) bool { return i < n && seg[i].is(words...) }
	clipEdge := func(i int) (string, bool) {
		switch {
		case is(i, "starts", "start", "begins", "begin"):
			return "", true
		case is(i, "ends", "end"):
			return "end", true
		}
		return "", false
	}
	moment := func(i int) string {
		if i >= n || seg[i].kind != tWord {
			return "Say which footage and when, like at interview-a 0:12.3."
		}
		id, ok := footage(seg[i].text)
		if !ok {
			return fmt.Sprintf("There's no footage called %q in this sheet.", seg[i].text)
		}
		at, j, ok := timeAt(seg, i+1)
		if !ok {
			return "Say when in the footage, like at interview-a 0:12.3."
		}
		a.Kind, a.AssetID, a.At = editspec.AnchorMoment, id, &at
		if is(j, "in") && j+1 < n && seg[j+1].kind == tWord {
			a.ClipID = seg[j+1].text
			j += 2
		}
		if j < n && seg[j].kind == tString {
			a.Quote = seg[j].text
			j++
		}
		if is(j, "ref") && j+1 < n && seg[j+1].kind == tString {
			a.Ref = seg[j+1].text
			j += 2
		}
		if j != n {
			return fmt.Sprintf("Unexpected %q in the start point.", joinWords(seg[j:]))
		}
		return ""
	}
	switch {
	case is(0, "when"):
		if n != 3 || seg[1].kind != tWord {
			return a, true, "Write it as: when <shot> starts, or when <shot> ends."
		}
		edge, ok := clipEdge(2)
		if !ok {
			return a, true, "Write it as: when <shot> starts, or when <shot> ends."
		}
		a.Kind, a.ClipID, a.Edge = editspec.AnchorClip, seg[1].text, edge
		return a, true, ""
	case is(0, "right") && is(1, "after"):
		if !previousLine(seg, 2, false) {
			return a, true, "Write it as: right after the previous line."
		}
		zero := 0.0
		a.Kind, a.Gap = editspec.AnchorAfter, &zero
		return a, true, ""
	case is(0, "after"):
		if !previousLine(seg, 1, false) {
			return a, true, "Write it as: after the previous line (or 0.5 s after the previous line)."
		}
		a.Kind = editspec.AnchorAfter
		return a, true, ""
	case is(0, "at"):
		if t, j, ok := timeAt(seg, 1); ok && j == n {
			a.Kind, a.At = editspec.AnchorTime, &t
			return a, true, ""
		}
		msg := moment(1)
		return a, true, msg
	}
	d, i, ok := durationAt(seg, 0)
	if !ok || !is(i, "after", "before") {
		return a, false, ""
	}
	before := is(i, "before")
	i++
	if d < 0 {
		return a, true, "Use a positive number of seconds, with after or before."
	}
	sign := 1.0
	if before {
		sign = -1
	}
	switch {
	case !before && previousLine(seg, i, false):
		g := d
		a.Kind, a.Gap = editspec.AnchorAfter, &g
		return a, true, ""
	case before && previousLine(seg, i, true):
		a.Kind, a.Offset = editspec.AnchorAfter, -d
		return a, true, ""
	case i+2 == n && seg[i].kind == tWord:
		if edge, ok := clipEdge(i + 1); ok {
			a.Kind, a.ClipID, a.Edge, a.Offset = editspec.AnchorClip, seg[i].text, edge, sign*d
			return a, true, ""
		}
	}
	if msg := moment(i); msg != "" {
		return a, true, msg
	}
	a.Offset = sign * d
	return a, true, ""
}

// ---- parsing: settings groups ----

func readNumberUnit(v *yaml.Node, units ...string) (float64, bool) {
	s, ok := scalarText(v)
	if !ok {
		return 0, false
	}
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	us := append([]string(nil), units...)
	sort.SliceStable(us, func(i, j int) bool { return len(us[i]) > len(us[j]) })
	for _, u := range us {
		if strings.HasSuffix(low, strings.ToLower(u)) {
			s = strings.TrimSpace(s[:len(s)-len(u)])
			break
		}
	}
	return parseNum(s)
}

func (p *parser) settingNumber(k, v *yaml.Node, off int, what string, units ...string) (json.RawMessage, bool) {
	x, ok := readNumberUnit(v, units...)
	if !ok {
		hint := ""
		if len(units) > 0 {
			hint = "Like " + units[0] + "."
		}
		p.errAt(v.Line+off, v.Column, v.Column+max(1, len(v.Value)), "bad_value", fmt.Sprintf("%s should be a number%s.", what, unitWords(units)), hint)
		return nil, false
	}
	return jsonNum(x), true
}

func unitWords(units []string) string {
	if len(units) == 0 {
		return ""
	}
	return " in " + strings.TrimSpace(units[0])
}

// voiceoverGroup reads `voiceover:` in the settings: layout, the narration
// settings and the lock (the lines themselves are in ## Voiceover).
func (p *parser) voiceoverGroup(v *yaml.Node, off int) {
	p.voGroup = map[string]json.RawMessage{}
	if v.Kind != yaml.MappingNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_value", "voiceover is a group of settings, one per line.", "Like:  voiceover:\n    fitting: ask")
		return
	}
	names := jsonNames(editspec.Voiceover{})
	settings := map[string]json.RawMessage{}
	put := func(m map[string]json.RawMessage, k *yaml.Node, key string, raw json.RawMessage) {
		if _, dup := m[key]; dup {
			p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "duplicate", fmt.Sprintf("%s is set twice in voiceover.", k.Value), "Keep one of them.")
			return
		}
		m[key] = raw
	}
	for j := 0; j+1 < len(v.Content); j += 2 {
		k, val := v.Content[j], v.Content[j+1]
		switch normKey(k.Value) {
		case "layout":
			s, _ := scalarText(val)
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "one recording", "continuous", "one take":
				put(p.voGroup, k, "layout", json.RawMessage(`"continuous"`))
			case "line by line", "lines", "beats":
				put(p.voGroup, k, "layout", json.RawMessage(`"beats"`))
			default:
				p.errAt(val.Line+off, val.Column, val.Column+max(1, len(s)), "bad_value", fmt.Sprintf("%q isn't a narration layout.", s), "Use line by line (each line has its own audio) or one recording.")
			}
		case "speaking rate", "rate":
			if raw, ok := p.settingNumber(k, val, off, "The speaking rate", " words a minute", " words per minute", " wpm"); ok {
				put(settings, k, "speakingRateWpm", raw)
			}
		case "rate from":
			if raw, ok := readEnum(enumRateSource, "speaking rate source")(p, val, off); ok {
				put(settings, k, "rateSource", raw)
			}
		case "fitting", "fit":
			if raw, ok := readEnum(enumFitPolicy, "way to fit lines")(p, val, off); ok {
				put(settings, k, "fitPolicy", raw)
			}
		case "pause", "gap":
			if raw, ok := p.settingNumber(k, val, off, "The pause between lines", " s", "s", " seconds"); ok {
				put(settings, k, "gap", raw)
			}
		case "locked":
			if raw, ok := readBool(p, val, off); ok {
				put(p.voGroup, k, "locked", raw)
			}
		case "script", "script version":
			s, _ := scalarText(val)
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "original", "your own", "my own", "mine":
				put(p.voGroup, k, "active", json.RawMessage(`"original"`))
			case "fitted", "fitted by claude", "claude's", "claude":
				put(p.voGroup, k, "active", json.RawMessage(`"fitted"`))
			default:
				p.errAt(val.Line+off, val.Column, val.Column+max(1, len(s)), "bad_value", fmt.Sprintf("%q isn't a script version.", s), "Use original (your own script) or fitted (Claude's version fitted to the video).")
			}
		case "beats":
			p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "not_supported", "The narration lines go in the ## Voiceover section, not in the settings.", "")
		default:
			if contains(names, k.Value) {
				raw, err := nodeJSON(val)
				if err != nil {
					p.errAt(val.Line+off, val.Column, val.Column+1, "bad_value", err.Error(), "")
					continue
				}
				put(p.voGroup, k, k.Value, raw)
				continue
			}
			p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "unknown_setting", fmt.Sprintf("voiceover has no setting called %q.", k.Value), "voiceover settings are: layout, script, speaking rate, rate from, fitting, pause, locked.")
		}
	}
	if len(settings) > 0 {
		if _, dup := p.voGroup["settings"]; dup {
			p.errAt(v.Line+off, v.Column, v.Column+1, "duplicate", "The narration settings are given twice (in words and as settings:).", "Keep one.")
			return
		}
		b, _ := json.Marshal(settings)
		p.voGroup["settings"] = b
	}
}

// mixGroup reads `mix:` in the settings.
func (p *parser) mixGroup(v *yaml.Node, off int) {
	p.mix = map[string]json.RawMessage{}
	if v.Kind != yaml.MappingNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_value", "mix is a group of settings, one per line (or {} for the defaults).", "Like:  mix:\n    music: -6 dB")
		return
	}
	names := jsonNames(editspec.Mix{})
	put := func(k *yaml.Node, key string, raw json.RawMessage) {
		if _, dup := p.mix[key]; dup {
			p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "duplicate", fmt.Sprintf("%s is set twice in mix.", k.Value), "Keep one of them.")
			return
		}
		p.mix[key] = raw
	}
	for j := 0; j+1 < len(v.Content); j += 2 {
		k, val := v.Content[j], v.Content[j+1]
		nk := normKey(k.Value)
		if bus, ok := busAliases[nk]; ok {
			if val.Kind == yaml.MappingNode {
				if raw, err := nodeJSON(val); err == nil {
					put(k, bus, raw)
				} else {
					p.errAt(val.Line+off, val.Column, val.Column+1, "bad_value", err.Error(), "")
				}
				continue
			}
			if raw, ok := p.busValue(val, off); ok {
				put(k, bus, raw)
			}
			continue
		}
		switch nk {
		case "camera sound", "clip audio":
			s, _ := scalarText(val)
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "off", "muted", "false", "no":
				put(k, "muteSource", json.RawMessage("true"))
			case "on", "true", "yes":
				put(k, "muteSource", json.RawMessage("false"))
			default:
				p.errAt(val.Line+off, val.Column, val.Column+max(1, len(s)), "bad_value", "camera sound is on or off.", "")
			}
		case "ducking":
			if raw, ok := p.duckingValue(val, off); ok {
				put(k, "ducking", raw)
			}
		case "loudness":
			if raw, ok := p.loudnessValue(val, off); ok {
				put(k, "loudness", raw)
			}
		case "even out voices", "level speech", "speech leveling", "level voices":
			if raw, ok := readBool(p, val, off); ok {
				put(k, "levelSpeech", raw)
			}
		case "blend at cuts", "cut blend", "blend at straight cuts":
			if raw, ok := p.settingNumber(k, val, off, "The blend at cuts", " s", "s", " seconds"); ok {
				put(k, "cutBlend", raw)
			}
		case "voices", "people", "speakers":
			if val.Kind == yaml.SequenceNode { // the data form ([{…}]) as written by older tools
				if raw, err := nodeJSON(val); err == nil {
					put(k, "voices", raw)
				} else {
					p.errAt(val.Line+off, val.Column, val.Column+1, "bad_value", err.Error(), "")
				}
				continue
			}
			p.voicesNode, p.voicesKey, p.voicesOff = val, k, off
		case "locked":
			if raw, ok := readBool(p, val, off); ok {
				put(k, "locked", raw)
			}
		default:
			if contains(names, k.Value) {
				raw, err := nodeJSON(val)
				if err != nil {
					p.errAt(val.Line+off, val.Column, val.Column+1, "bad_value", err.Error(), "")
					continue
				}
				put(k, k.Value, raw)
				continue
			}
			p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "unknown_setting", fmt.Sprintf("mix has no setting called %q.", k.Value), "mix settings are: camera sound, clip sound, narration, music, sound effects, ducking, loudness, even out voices, blend at cuts, voices, locked.")
		}
	}
}

// busValue reads "-6 dB", "muted", "solo", "-6 dB, muted" or "normal".
func (p *parser) busValue(v *yaml.Node, off int) (json.RawMessage, bool) {
	s, ok := scalarText(v)
	bad := func() (json.RawMessage, bool) {
		p.errAt(v.Line+off, v.Column, v.Column+max(1, len(v.Value)), "bad_value", fmt.Sprintf("%q isn't a level MetaRush can read.", v.Value), "A level is in decibels, like -6 dB (0 dB is unchanged); add , muted or , solo, or write normal.")
		return nil, false
	}
	if !ok {
		return bad()
	}
	m := map[string]json.RawMessage{}
	for _, part := range strings.Split(s, ",") {
		w := strings.ToLower(strings.TrimSpace(part))
		switch {
		case w == "normal" && strings.TrimSpace(s) == part:
		case w == "muted" || w == "mute" || w == "off":
			if _, dup := m["mute"]; dup {
				return bad()
			}
			m["mute"] = json.RawMessage("true")
		case w == "solo":
			if _, dup := m["solo"]; dup {
				return bad()
			}
			m["solo"] = json.RawMessage("true")
		default:
			x, ok := parseNum(strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(w, "db"), " ")))
			if !ok || !strings.HasSuffix(w, "db") {
				return bad()
			}
			if _, dup := m["gainDb"]; dup {
				return bad()
			}
			m["gainDb"] = jsonNum(x)
		}
	}
	b, _ := json.Marshal(m)
	return b, true
}

var duckingKeys = map[string]struct {
	json, unit string
}{
	"music under narration":      {"musicUnderVoiceoverDb", "dB"},
	"clip sound under narration": {"sourceUnderVoiceoverDb", "dB"},
	"music under speech":         {"musicUnderDialogueDb", "dB"},
	"dip time":                   {"attack", "s"},
	"recovery":                   {"release", "s"},
}

func (p *parser) duckingValue(v *yaml.Node, off int) (json.RawMessage, bool) {
	if v.Kind == yaml.ScalarNode {
		s, _ := scalarText(v)
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "on", "true", "yes":
			return json.RawMessage(`{"enabled":true}`), true
		case "off", "false", "no":
			return json.RawMessage(`{"enabled":false}`), true
		}
		p.errAt(v.Line+off, v.Column, v.Column+max(1, len(v.Value)), "bad_value", "ducking is on, off, or a group of settings.", "")
		return nil, false
	}
	if v.Kind != yaml.MappingNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_value", "ducking is on, off, or a group of settings.", "")
		return nil, false
	}
	names := jsonNames(editspec.Ducking{})
	m := map[string]json.RawMessage{}
	for j := 0; j+1 < len(v.Content); j += 2 {
		k, val := v.Content[j], v.Content[j+1]
		nk := normKey(k.Value)
		key, raw, ok := "", json.RawMessage(nil), false
		switch {
		case nk == "on" || nk == "enabled":
			key = "enabled"
			raw, ok = readBool(p, val, off)
		case duckingKeys[nk].json != "":
			d := duckingKeys[nk]
			key = d.json
			raw, ok = p.settingNumber(k, val, off, k.Value, " "+d.unit, d.unit)
		case contains(names, k.Value):
			key = k.Value
			var err error
			if raw, err = nodeJSON(val); err == nil {
				ok = true
			} else {
				p.errAt(val.Line+off, val.Column, val.Column+1, "bad_value", err.Error(), "")
			}
		default:
			p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "unknown_setting", fmt.Sprintf("ducking has no setting called %q.", k.Value), "ducking settings are: on, music under narration, clip sound under narration, music under speech, dip time, recovery.")
		}
		if !ok {
			continue
		}
		if _, dup := m[key]; dup {
			p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "duplicate", fmt.Sprintf("%s is set twice in ducking.", k.Value), "")
			continue
		}
		m[key] = raw
	}
	b, _ := json.Marshal(m)
	return b, true
}

func (p *parser) loudnessValue(v *yaml.Node, off int) (json.RawMessage, bool) {
	if v.Kind == yaml.ScalarNode {
		raw, ok := p.settingNumber(v, v, off, "The loudness target", " LUFS", "LUFS")
		if !ok {
			return nil, false
		}
		return json.RawMessage(`{"targetLufs":` + string(raw) + `}`), true
	}
	if v.Kind != yaml.MappingNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_value", "loudness is a target like -14 LUFS, or a group of settings.", "")
		return nil, false
	}
	names := jsonNames(editspec.MasterLoudness{})
	m := map[string]json.RawMessage{}
	for j := 0; j+1 < len(v.Content); j += 2 {
		k, val := v.Content[j], v.Content[j+1]
		key, raw, ok := "", json.RawMessage(nil), false
		switch nk := normKey(k.Value); {
		case nk == "target":
			key = "targetLufs"
			raw, ok = p.settingNumber(k, val, off, "The loudness target", " LUFS", "LUFS")
		case nk == "true peak" || nk == "peak":
			key = "truePeakDbtp"
			raw, ok = p.settingNumber(k, val, off, "The true peak", " dBTP", "dBTP", " dB", "dB")
		case nk == "normalize" || nk == "normalise":
			key = "normalize"
			raw, ok = readBool(p, val, off)
		case contains(names, k.Value):
			key = k.Value
			var err error
			if raw, err = nodeJSON(val); err == nil {
				ok = true
			} else {
				p.errAt(val.Line+off, val.Column, val.Column+1, "bad_value", err.Error(), "")
			}
		default:
			p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "unknown_setting", fmt.Sprintf("loudness has no setting called %q.", k.Value), "loudness settings are: target, true peak, normalize.")
		}
		if !ok {
			continue
		}
		if _, dup := m[key]; dup {
			p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "duplicate", fmt.Sprintf("%s is set twice in loudness.", k.Value), "")
			continue
		}
		m[key] = raw
	}
	b, _ := json.Marshal(m)
	return b, true
}

// ---- parsing: ## Voiceover ----

// beatLine reads `- name: "words" · start point · settings`.
func (p *parser) beatLine(rest []rune, line, col int) *item {
	it := newItem("beat", line)
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		it.broken = true
		return it
	}
	segs := splitSeps(ts)
	head := segs[0]
	i := p.idToken(it, head, line)
	if it.id == "" {
		it.auto = true
	}
	if i >= len(head) || head[i].kind != tString {
		c := col
		if i < len(head) {
			c = head[i].col
		}
		p.errAt(line, c, c+1, "missing_text", "Each narration line needs its words in quotes, like - intro: \"When I started…\" · when hook starts.", "")
		it.broken = true
		return it
	}
	p.set(it, "text", json.RawMessage(quote(head[i].text)), Span{line, head[i].col, head[i].end}, "The words")
	if extra := head[i+1:]; len(extra) > 0 {
		c0, c1 := spanOf(extra)
		p.errAt(line, c0, c1, "unexpected", fmt.Sprintf("Unexpected %q after the words.", joinWords(extra)), "Separate the start point and settings with ·.")
	}
	for _, seg := range segs[1:] {
		p.beatAttr(it, seg, line)
	}
	if _, ok := it.m["anchor"]; !ok {
		it.m["anchor"] = json.RawMessage(`{"kind":"after"}`) // a line without a start point follows the previous one
	}
	return it
}

func (p *parser) anchorAttr(it *item, seg []token, line int) bool {
	a, matched, msg := readAnchor(seg, func(name string) (string, bool) {
		if assetIDRe.MatchString(name) {
			return name, true
		}
		if ref, ok := p.footage[name]; ok {
			return ref.AssetID, true
		}
		return "", false
	})
	if !matched {
		return false
	}
	c0, c1 := spanOf(seg)
	if msg != "" {
		p.errAt(line, c0, c1, "bad_start", msg, "Start points: when hook starts, 0.5 s after hook ends, after the previous line, at 0:12.4, at interview-a 0:12.3 in hook.")
		return true
	}
	b, _ := json.Marshal(a)
	p.set(it, "anchor", b, Span{line, c0, c1}, "The start point")
	return true
}

func (p *parser) beatAttr(it *item, seg []token, line int) {
	if len(seg) == 0 {
		return
	}
	c0, c1 := spanOf(seg)
	sp := Span{line, c0, c1}
	t := seg[0]
	switch {
	case len(seg) == 1 && t.kind == tBlock:
		p.mergeBlock(it, t, line)
	case len(seg) == 1 && t.is("locked"):
		p.set(it, "locked", json.RawMessage("true"), sp, "locked")
	case t.is("volume") && len(seg) >= 2:
		if v, ok := dbValue(seg[1:]); ok {
			p.set(it, "gainDb", jsonNum(v), sp, "Volume")
			return
		}
		p.errAt(line, c0, c1, "bad_value", "A line's volume is in decibels, like volume -2 dB (0 dB is unchanged).", "")
	case t.is("status") && len(seg) == 2:
		s := strings.ToLower(seg[1].text)
		if !contains(enumState, s) {
			p.errAt(line, seg[1].col, seg[1].end, "bad_value", fmt.Sprintf("%q isn't a status MetaRush knows.", seg[1].text), "Use "+orList(enumState)+".")
			return
		}
		p.set(it, "state", json.RawMessage(quote(s)), sp, "The status")
	case t.is("voice"):
		if raw, ok := p.voiceValue(seg, line); ok {
			p.set(it, "voice", raw, sp, "The voice")
		}
	default:
		if p.anchorAttr(it, seg, line) {
			return
		}
		p.errAt(line, c0, c1, "unknown_setting", fmt.Sprintf("MetaRush doesn't know %q for a narration line.", joinWords(seg)), "Narration line settings: "+beatSettings+".")
	}
}

func dbValue(ts []token) (float64, bool) {
	if len(ts) == 2 && ts[1].is("db") {
		ts = ts[:1]
	}
	if len(ts) != 1 {
		return 0, false
	}
	return numberWithSuffix(ts[0], "dB", "db")
}

// voiceValue reads `voice scratch`, `voice scratch "Samantha"` or
// `voice elevenlabs "Rachel" id … model …`.
func (p *parser) voiceValue(seg []token, line int) (json.RawMessage, bool) {
	c0, c1 := spanOf(seg)
	bad := func(msg string) (json.RawMessage, bool) {
		p.errAt(line, c0, c1, "bad_value", msg, `Like: voice scratch, or voice elevenlabs "Rachel" id <voice id>.`)
		return nil, false
	}
	if len(seg) < 2 || seg[1].kind != tWord {
		return bad("Say which voice: scratch (the free placeholder) or elevenlabs.")
	}
	prov, ok := providerAlias[strings.ToLower(seg[1].text)]
	if !ok {
		return bad(fmt.Sprintf("%q isn't a voice provider MetaRush knows.", seg[1].text))
	}
	m := map[string]string{"provider": prov}
	keys := []string{"provider"}
	for j := 2; j < len(seg); {
		t := seg[j]
		key := ""
		switch {
		case t.kind == tString:
			key = "name"
		case t.is("id") && j+1 < len(seg) && seg[j+1].kind == tWord:
			key = "voiceId"
			j++
		case t.is("model") && j+1 < len(seg) && seg[j+1].kind == tWord:
			key = "modelId"
			j++
		default:
			return bad(fmt.Sprintf("Unexpected %q in the voice.", t.text))
		}
		if _, dup := m[key]; dup {
			return bad(key + " is given twice.")
		}
		m[key] = seg[j].text
		keys = append(keys, key)
		j++
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, quote(k)+":"+quote(m[k]))
	}
	return json.RawMessage("{" + strings.Join(parts, ",") + "}"), true
}

// audioRefLine reads the audio part shared by Take: and Recording: lines into
// a JSON object: `<footage> <in> → <out> · scratch | generated | recorded |
// imported · AI voice · cleaned version <footage> "<preset>" [shifted 0.1 s] ·
// plays the cleaned version`, plus `speed 1.04× · id tk_…` for takes and a
// start point and `volume -2 dB` for recordings.
func (p *parser) audioRefLine(rest []rune, line, col int, recording bool) (map[string]json.RawMessage, map[string]Span, bool) {
	it := newItem("audioref", line)
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		return nil, nil, false
	}
	segs := splitSeps(ts)
	what := "the take"
	if recording {
		what = "the recording"
	}
	start := 0
	if len(segs[0]) > 0 && !(len(segs[0]) == 1 && segs[0][0].kind == tBlock) {
		head := segs[0]
		assetID, ok := p.resolveFootage(head[0], line)
		if !ok {
			return nil, nil, false
		}
		p.set(it, "assetId", json.RawMessage(quote(assetID)), Span{line, head[0].col, head[0].end}, "The audio")
		a, b, used, ok := p.readRange(head[1:], line, false, what)
		if !ok {
			return nil, nil, false
		}
		if extra := head[1+used:]; len(extra) > 0 {
			c0, c1 := spanOf(extra)
			p.errAt(line, c0, c1, "unexpected", fmt.Sprintf("Unexpected %q after the times.", joinWords(extra)), "Separate settings with ·.")
		}
		p.set(it, "in", jsonNum(a.time), a.sp, "The start")
		p.set(it, "out", jsonNum(b.time), b.sp, "The end")
		start = 1
	}
	for _, seg := range segs[start:] {
		if len(seg) == 0 {
			continue
		}
		c0, c1 := spanOf(seg)
		sp := Span{line, c0, c1}
		words := strings.ToLower(joinWords(seg))
		t := seg[0]
		switch {
		case len(seg) == 1 && t.kind == tBlock:
			p.mergeBlock(it, t, line)
		case takeSourceAlias[words] != "" && len(seg) == 1:
			p.set(it, "source", json.RawMessage(quote(takeSourceAlias[words])), sp, "How it was made")
		case words == "ai voice":
			p.set(it, "aiVoice", json.RawMessage("true"), sp, "AI voice")
		case words == "plays the cleaned version" || words == "plays cleaned version":
			p.set(it, "useProcessed", json.RawMessage("true"), sp, "Which version plays")
		case t.is("cleaned") && len(seg) >= 4 && seg[1].is("version") && seg[3].kind == tString:
			id, ok := p.resolveFootage(seg[2], line)
			if !ok {
				continue
			}
			pr := `{"assetId":` + quote(id) + `,"preset":` + quote(seg[3].text)
			if len(seg) > 4 {
				d, j, ok := durationAt(seg, 5)
				if !seg[4].is("shifted") || !ok || j != len(seg) {
					p.errAt(line, c0, c1, "bad_value", "Write the cleaned version as: cleaned version <footage> \"<preset>\" (shifted 0.1 s).", "")
					continue
				}
				pr += `,"offset":` + string(jsonNum(d))
			}
			p.set(it, "processed", json.RawMessage(pr+"}"), sp, "The cleaned version")
		case !recording && t.is("speed") && len(seg) >= 2:
			ts := seg[1:]
			if len(ts) == 2 && ts[1].is("×", "x", "times") {
				ts = ts[:1]
			}
			v, ok := numberWithSuffix(ts[0], "×", "x")
			if !ok || len(ts) != 1 {
				p.errAt(line, c0, c1, "bad_value", "Speed is a number like 1.04×.", "")
				continue
			}
			p.set(it, "speed", jsonNum(v), sp, "Speed")
		case !recording && t.is("id") && len(seg) == 2 && seg[1].kind == tWord:
			p.set(it, "takeId", json.RawMessage(quote(seg[1].text)), sp, "The take ID")
		case recording && t.is("volume") && len(seg) >= 2:
			v, ok := dbValue(seg[1:])
			if !ok {
				p.errAt(line, c0, c1, "bad_value", "The recording's volume is in decibels, like volume -2 dB.", "")
				continue
			}
			p.set(it, "gainDb", jsonNum(v), sp, "Volume")
		default:
			if recording && p.anchorAttr(it, seg, line) {
				continue
			}
			p.errAt(line, c0, c1, "unknown_setting", fmt.Sprintf("MetaRush doesn't know %q for %s.", joinWords(seg), what), "Like: narration-01 0:00 → 0:02.4 · recorded · cleaned version narration-01-clean \"voice\" · plays the cleaned version.")
		}
	}
	return it.m, it.spans, true
}

func (p *parser) takeLine(beat *item, rest []rune, line, col, labelCol int) {
	if beat.kind != "beat" {
		p.errAt(line, labelCol, col, "not_supported", "Only narration lines have a Take: line.", "")
		return
	}
	m, _, ok := p.audioRefLine(rest, line, col, false)
	if !ok {
		return
	}
	b, _ := json.Marshal(m)
	p.set(beat, "take", b, Span{line, col, col + len(rest)}, "The take")
}

// recordingLine reads `Recording: …`, the one continuous read of the narration.
func (p *parser) recordingLine(rest []rune, line, col int) *item {
	it := newItem("recording", line)
	m, spans, ok := p.audioRefLine(rest, line, col, true)
	if !ok {
		it.broken = true
		return it
	}
	it.m, it.spans = m, spans
	if _, ok := it.m["anchor"]; !ok {
		p.errAt(line, col, col+len(rest), "missing_start", "Say where the recording starts, like · when hook starts or · at 0:00.", "")
		it.broken = true
	}
	return it
}

// assembleVoiceover builds the narration from the settings group, the lines
// and the recording.
func (p *parser) assembleVoiceover(spec *editspec.Spec) {
	if p.voGroup == nil && !p.voSection {
		return
	}
	vm := map[string]json.RawMessage{}
	for k, v := range p.voGroup {
		vm[k] = v
	}
	var beats []string
	for _, it := range p.beats {
		if it.broken {
			continue
		}
		var b editspec.Beat
		if p.decodeItem(it, &b) {
			bb, _ := json.Marshal(b)
			beats = append(beats, string(bb))
			p.sm.Beats[b.ID] = &ItemPos{Line: it.line, Fields: it.spans}
		}
	}
	vm["beats"] = json.RawMessage("[" + strings.Join(beats, ",") + "]")
	if p.original != nil {
		if _, dup := vm["original"]; dup {
			p.errAt(p.originalLine, 1, 4, "duplicate", "The original script is given twice (under ### Original script and in the settings).", "Keep the ### Original script block.")
		} else {
			b, _ := json.Marshal(*p.original)
			vm["original"] = b
		}
	}
	for i, it := range p.recordings {
		if i > 0 {
			p.errAt(it.line, 1, 2, "duplicate", "There are two Recording: lines; the narration has one continuous recording at most.", "Keep one.")
			continue
		}
		if it.broken {
			continue
		}
		var c editspec.ContinuousVO
		if !p.decodeItem(it, &c) {
			continue
		}
		if _, dup := vm["continuous"]; dup {
			p.errAt(it.line, 1, 2, "duplicate", "The recording is given twice (a Recording: line and in the settings).", "Keep the Recording: line.")
			continue
		}
		cb, _ := json.Marshal(c)
		vm["continuous"] = cb
	}
	sp := p.sm.Keys["voiceover"]
	var v editspec.Voiceover
	if p.decodeInto(vm, &v, sp, "the voiceover") {
		spec.Voiceover = &v
	}
}

func (p *parser) assembleMix(spec *editspec.Spec) {
	if p.mix == nil {
		return
	}
	if p.voicesNode != nil {
		if raw, ok := p.voicesValue(p.voicesNode, p.voicesOff); ok {
			if _, dup := p.mix["voices"]; dup {
				k := p.voicesKey
				p.errAt(k.Line+p.voicesOff, k.Column, k.Column+len(k.Value), "duplicate", "voices is set twice in mix.", "Keep one of them.")
			} else {
				p.mix["voices"] = raw
			}
		}
		p.voicesNode = nil
	}
	var m editspec.Mix
	if p.decodeInto(p.mix, &m, p.sm.Keys["mix"], "mix") {
		spec.Mix = &m
	}
}
