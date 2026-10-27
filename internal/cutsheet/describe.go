package cutsheet

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Describe lists, in plain words, what changed from cut a to cut b. Clips are
// called by their short names, as in the cut sheet. name gives a footage's
// short name (nil: the sheet's own naming of b).
func Describe(a, b *editspec.Spec, name func(assetID string) string) []string {
	a, b = editspec.Clone(a), editspec.Clone(b)
	editspec.Normalize(a)
	editspec.Normalize(b)
	if name == nil {
		na, nb := assignNames(a, nil), assignNames(b, nil)
		name = func(id string) string {
			if n, ok := nb.byID[id]; ok {
				return n
			}
			return na.name(id)
		}
	}
	d := &describer{name: name}
	first := len(a.Clips) == 0
	if first {
		d.add(fmt.Sprintf("A first cut: %s, %s long.", plural(len(b.Clips), "clip"), seconds(cutLength(b))))
	} else {
		d.duration(a, b)
	}
	d.clips(a, b)
	d.overlays(a, b)
	d.audio(a, b)
	d.captions(a.Captions, b.Captions)
	d.voiceover(a.Voiceover, b.Voiceover)
	d.mix(a.Mix, b.Mix)
	if !first {
		d.output(a.Output, b.Output)
	}
	if !jsonEqual(a.Brand, b.Brand) && !(first && b.Brand == nil) {
		if logosOnly(a.Brand, b.Brand) {
			d.add("The brand's logos changed.")
		} else {
			d.add("The brand colors or font changed.")
		}
	}
	d.resources(a, b)
	d.fixed(a, b)
	if first {
		return d.out
	}
	if a.Title != b.Title {
		if b.Title == "" {
			d.add("The title was removed.")
		} else {
			d.add(fmt.Sprintf("The title is now “%s”.", b.Title))
		}
	}
	if a.Notes != b.Notes {
		d.add("The notes about the whole cut changed.")
	}
	if len(d.out) == 0 {
		return []string{"No changes."}
	}
	return d.out
}

type describer struct {
	name func(string) string
	out  []string
}

func (d *describer) add(s string) { d.out = append(d.out, s) }

// resources says which pictures, logos and fonts the cut now uses (D82).
func (d *describer) resources(a, b *editspec.Spec) {
	names := map[string]bool{}
	for n := range a.Resources {
		names[n] = true
	}
	for n := range b.Resources {
		names[n] = true
	}
	var order []string
	for n := range names {
		order = append(order, n)
	}
	sort.Strings(order)
	for _, n := range order {
		p, had := a.Resources[n]
		q, has := b.Resources[n]
		switch {
		case !had && has:
			d.add(fmt.Sprintf("Uses the resource %s.", n))
		case had && !has:
			d.add(fmt.Sprintf("No longer uses the resource %s.", n))
		case p.ID != q.ID:
			d.add(fmt.Sprintf("%s now stands for another resource.", capital(n)))
		case p.SHA256 != q.SHA256:
			d.add(fmt.Sprintf("Uses another version of %s.", n))
		}
	}
}

// fixed says which footage now plays a fixed copy or its original (D152).
func (d *describer) fixed(a, b *editspec.Spec) {
	ids := map[string]bool{}
	for id := range a.Fixed {
		ids[id] = true
	}
	for id := range b.Fixed {
		ids[id] = true
	}
	var order []string
	for id := range ids {
		order = append(order, id)
	}
	sort.Strings(order)
	for _, id := range order {
		p, had := a.Fixed[id]
		q, has := b.Fixed[id]
		switch {
		case !had && has:
			d.add(fmt.Sprintf("%s plays its fixed copy.", capital(d.name(id))))
		case had && !has:
			d.add(fmt.Sprintf("%s plays its original again.", capital(d.name(id))))
		case p.SHA256 != q.SHA256:
			d.add(fmt.Sprintf("%s plays another fixed copy.", capital(d.name(id))))
		}
	}
}

// plural counts for people: "1 line", "3 lines".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	var xv, yv any
	_ = json.Unmarshal(x, &xv)
	_ = json.Unmarshal(y, &yv)
	xb, _ := json.Marshal(xv)
	yb, _ := json.Marshal(yv)
	return string(xb) == string(yb)
}

// seconds is a length for people: "14.2 s", "1 min 4.2 s".
func seconds(v float64) string {
	v = math.Round(v*10) / 10
	if v < 60 {
		return fmtNum(v) + " s"
	}
	m := int(v) / 60
	return fmt.Sprintf("%d min %s s", m, fmtNum(math.Round((v-float64(m*60))*10)/10))
}

func cutLength(s *editspec.Spec) float64 {
	_, total := editspec.Timeline(s)
	if s.Output.FPS == 0 {
		return 0
	}
	return float64(total) / float64(s.Output.FPS)
}

func (d *describer) duration(a, b *editspec.Spec) {
	la, lb := cutLength(a), cutLength(b)
	delta := lb - la
	switch {
	case math.Abs(delta) < 0.05:
	case delta < 0:
		d.add(fmt.Sprintf("The video gets %s shorter: %s → %s.", seconds(-delta), seconds(la), seconds(lb)))
	default:
		d.add(fmt.Sprintf("The video gets %s longer: %s → %s.", seconds(delta), seconds(la), seconds(lb)))
	}
}

func (d *describer) clipText(c editspec.Clip) string {
	return fmt.Sprintf("%s %s → %s", d.name(c.AssetID), fmtClock(c.In), fmtClock(c.Out))
}

func transitionText(t *editspec.Transition) string {
	if t == nil {
		return "a straight cut"
	}
	w := TransitionWords(t.Type)
	if t.Direction != "" && moves(t.Type) && isDirection(t.Direction) {
		w += " " + t.Direction
	}
	s := "a " + w
	if t.Duration > 0 && t.Type != editspec.TransitionCut {
		s = fmt.Sprintf("a %s (%s)", w, fmtDur(t.Duration))
	}
	for _, cl := range TransitionClauses {
		if phrase, ok := cl.Print(t, CutContext{}); ok {
			s += ", " + phrase
		}
	}
	return s
}

func (d *describer) clips(a, b *editspec.Spec) {
	ac, bc := map[string]editspec.Clip{}, map[string]editspec.Clip{}
	var aIDs, bIDs []string
	for _, c := range a.Clips {
		ac[c.ID] = c
		aIDs = append(aIDs, c.ID)
	}
	for _, c := range b.Clips {
		bc[c.ID] = c
		bIDs = append(bIDs, c.ID)
	}
	for _, c := range a.Clips {
		if _, ok := bc[c.ID]; !ok {
			d.add(fmt.Sprintf("Removed the clip %s (%s).", c.ID, d.clipText(c)))
		}
	}
	// A clip cut in two: one sentence, and its first part is compared with what
	// the split made (so only other changes are listed).
	splits := editspec.SplitPairs(a, b)
	for _, c := range b.Clips {
		if orig, ok := splits[c.ID]; ok {
			d.add(fmt.Sprintf("Split %s in two at %s; the second part is %s.", orig, fmtClock(c.In), c.ID))
			left, right := editspec.SplitParts(ac[orig], c.In, c.ID)
			ac[orig], ac[c.ID] = left, right
			for k, id := range aIDs {
				if id == orig {
					aIDs = append(aIDs[:k+1], append([]string{c.ID}, aIDs[k+1:]...)...)
					break
				}
			}
		}
	}
	for i, c := range b.Clips {
		if _, ok := ac[c.ID]; !ok {
			d.add(fmt.Sprintf("Added clip %d, %s: %s.", i+1, c.ID, d.clipText(c)))
		}
	}
	var common, commonB []string
	for _, id := range aIDs {
		if _, ok := bc[id]; ok {
			common = append(common, id)
		}
	}
	for _, id := range bIDs {
		if _, ok := ac[id]; ok {
			commonB = append(commonB, id)
		}
	}
	keep := lcsSet(common, commonB)
	for i, id := range bIDs {
		if _, ok := ac[id]; ok && !keep[id] {
			d.add(fmt.Sprintf("Moved %s to position %d.", id, i+1))
		}
	}
	for _, c := range b.Clips {
		old, ok := ac[c.ID]
		if !ok || jsonEqual(old, c) {
			continue
		}
		d.clipChanges(old, c)
	}
}

func (d *describer) clipChanges(o, c editspec.Clip) {
	id := c.ID
	changed := changedKeys(o, c)
	has := func(k string) bool { return changed[k] }
	if has("assetId") {
		d.add(fmt.Sprintf("%s now uses %s instead of %s.", id, d.name(c.AssetID), d.name(o.AssetID)))
	}
	lenOf := func(x editspec.Clip) float64 {
		sp := x.Speed
		if sp == 0 {
			sp = 1
		}
		return (x.Out - x.In) / sp
	}
	delta := lenOf(c) - lenOf(o)
	longer := ""
	switch {
	case math.Abs(delta) < 0.005:
	case delta < 0:
		longer = fmt.Sprintf(", %s shorter", seconds(-delta))
	default:
		longer = fmt.Sprintf(", %s longer", seconds(delta))
	}
	switch {
	case has("in") && has("out"):
		d.add(fmt.Sprintf("%s now runs %s → %s (was %s → %s%s).", id, fmtClock(c.In), fmtClock(c.Out), fmtClock(o.In), fmtClock(o.Out), longer))
	case has("in"):
		d.add(fmt.Sprintf("%s now starts at %s instead of %s%s.", id, fmtClock(c.In), fmtClock(o.In), longer))
	case has("out"):
		d.add(fmt.Sprintf("%s now ends at %s instead of %s%s.", id, fmtClock(c.Out), fmtClock(o.Out), longer))
	}
	if has("speed") {
		d.add(fmt.Sprintf("%s now plays at %s× speed (was %s×).", id, fmtNum(nz(c.Speed)), fmtNum(nz(o.Speed))))
	}
	if has("volume") {
		if c.Volume == nil {
			d.add(fmt.Sprintf("%s is back to its normal volume.", id))
		} else {
			d.add(fmt.Sprintf("%s's volume is now %s.", id, fmtPercent(*c.Volume)))
		}
	}
	if has("muted") {
		if c.Muted {
			d.add(fmt.Sprintf("%s is now muted.", id))
		} else {
			d.add(fmt.Sprintf("%s's sound is back on.", id))
		}
	}
	if has("framing") {
		d.add(fmt.Sprintf("%s's framing changed.", id))
	}
	if has("transitionIn") {
		d.add(fmt.Sprintf("%s now comes in with %s (was %s).", id, transitionText(c.Transition), transitionText(o.Transition)))
	}
	if has("fadeOut") {
		if c.FadeOut == 0 {
			d.add(fmt.Sprintf("%s no longer fades out.", id))
		} else {
			d.add(fmt.Sprintf("%s now fades out over %s.", id, fmtDur(c.FadeOut)))
		}
	}
	if has("label") {
		d.add(fmt.Sprintf("%s is now labeled “%s”.", id, c.Label))
	}
	if has("rationale") {
		d.add(fmt.Sprintf("The reason (Why) for %s changed.", id))
	}
	if has("evidence") {
		d.add(fmt.Sprintf("The evidence for %s changed.", id))
	}
	if has("notes") {
		d.add(fmt.Sprintf("The notes on %s changed.", id))
	}
	if has("locked") {
		if c.Locked {
			d.add(fmt.Sprintf("%s is now locked.", id))
		} else {
			d.add(fmt.Sprintf("%s is now unlocked.", id))
		}
	}
	var other []string
	for k := range changed {
		switch k {
		case "assetId", "in", "out", "speed", "volume", "muted", "framing", "transitionIn", "fadeOut", "label", "rationale", "evidence", "notes", "locked":
		default:
			other = append(other, k)
		}
	}
	if len(other) > 0 {
		sort.Strings(other)
		d.add(fmt.Sprintf("%s changed (%s).", id, strings.Join(other, ", ")))
	}
}

func nz(v float64) float64 {
	if v == 0 {
		return 1
	}
	return v
}

func changedKeys(a, b any) map[string]bool {
	var am, bm map[string]json.RawMessage
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	_ = json.Unmarshal(ab, &am)
	_ = json.Unmarshal(bb, &bm)
	out := map[string]bool{}
	for k, v := range am {
		if w, ok := bm[k]; !ok || !jsonEqual(v, w) {
			out[k] = true
		}
	}
	for k := range bm {
		if _, ok := am[k]; !ok {
			out[k] = true
		}
	}
	return out
}

func lcsSet(a, b []string) map[string]bool {
	w := map[string]int{}
	for _, x := range a {
		w[x] = 1
	}
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	keep := map[string]bool{}
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a[i] == b[j]:
			keep[a[i]] = true
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return keep
}

func shortVersion(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func templateName(id string) string {
	return map[string]string{
		editspec.TplLowerThirdClean: "name title", editspec.TplLowerThirdCinematic: "cinematic name title", editspec.TplQuoteKinetic: "quote",
		editspec.TplEndCard: "end card", editspec.TplDataCard: "number card", editspec.TplTitleCard: "title card", editspec.TplLogoCorner: "corner logo",
		editspec.TplSlideTitle: "title slide", editspec.TplSlideBullets: "bullet slide", editspec.TplSlideCode: "code slide",
		editspec.TplSlideArchitecture: "diagram slide", editspec.TplSlideScreenshot: "screenshot slide", editspec.TplSlideQuote: "quote slide",
		editspec.TplNameBar: "name bar", editspec.TplEndScreen: "end screen",
	}[id]
}

func (d *describer) overlays(a, b *editspec.Spec) {
	ao, bo := map[string]editspec.Overlay{}, map[string]editspec.Overlay{}
	for _, o := range a.Overlays {
		ao[o.ID] = o
	}
	for _, o := range b.Overlays {
		bo[o.ID] = o
	}
	what := func(o editspec.Overlay) string {
		if o.Motion != nil {
			return "the motion graphic " + o.ID + " (" + o.Motion.Name + ")"
		}
		if n := templateName(o.Template); n != "" {
			return "the " + n + " " + o.ID
		}
		if o.Role != "" && editspec.GraphicRole(o.Role) {
			return "the " + editspec.RoleWords(o.Role) + " " + o.ID
		}
		return "the graphic " + o.ID
	}
	for _, o := range a.Overlays {
		if _, ok := bo[o.ID]; !ok {
			d.add(fmt.Sprintf("Removed %s.", what(o)))
		}
	}
	for _, o := range b.Overlays {
		old, ok := ao[o.ID]
		if !ok {
			if o.Span != nil {
				d.add(fmt.Sprintf("Added %s for %s.", what(o), spanSentence(o.Span)))
			} else {
				d.add(fmt.Sprintf("Added %s at %s for %s.", what(o), fmtClock(o.At), fmtDur(o.Duration)))
			}
			if n := len(o.Changes); n > 0 {
				d.add(fmt.Sprintf("%s changes %s while it is on screen.", capital(what(o)), timesWords(n)))
			}
			continue
		}
		ch := changedKeys(old, o)
		switch {
		case ch["motion"] && o.Motion != nil && old.Motion != nil && old.Motion.Name == o.Motion.Name:
			d.add(fmt.Sprintf("%s now uses another version of %s (%s, was %s).", o.ID, o.Motion.Name, shortVersion(o.Motion.Hash), shortVersion(old.Motion.Hash)))
		case ch["motion"] && o.Motion != nil:
			d.add(fmt.Sprintf("%s is now the motion graphic %s.", o.ID, o.Motion.Name))
		case ch["template"] && o.Template != "":
			d.add(fmt.Sprintf("%s is now a %s.", o.ID, templateName(o.Template)))
		}
		switch {
		case ch["span"] && o.Span != nil:
			d.add(fmt.Sprintf("%s now lasts %s.", capital(what(o)), spanSentence(o.Span)))
		case ch["span"]:
			d.add(fmt.Sprintf("%s now shows at %s for %s instead of %s.", capital(what(o)), fmtClock(o.At), fmtDur(o.Duration), spanSentence(old.Span)))
		case (ch["at"] || ch["duration"]) && o.Span == nil:
			d.add(fmt.Sprintf("%s now shows at %s for %s (was at %s for %s).", capital(what(o)), fmtClock(o.At), fmtDur(o.Duration), fmtClock(old.At), fmtDur(old.Duration)))
		case ch["at"] || ch["duration"]:
			d.add(fmt.Sprintf("%s starts or ends at another moment of its span.", capital(what(o))))
		}
		if ch["changes"] {
			if n := len(o.Changes); n > 0 {
				d.add(fmt.Sprintf("%s now changes %s while it is on screen (was %s).", capital(what(o)), timesWords(n), timesWords(len(old.Changes))))
			} else {
				d.add(fmt.Sprintf("%s no longer changes while it is on screen.", capital(what(o))))
			}
		}
		if ch["params"] {
			d.add(fmt.Sprintf("The text of %s changed.", what(o)))
		}
		if ch["role"] {
			if o.Role != "" {
				d.add(fmt.Sprintf("%s is now the layout pack's %s.", capital(what(o)), editspec.RoleWords(o.Role)))
			} else {
				d.add(fmt.Sprintf("%s no longer plays a part in the layout pack.", capital(what(o))))
			}
		}
		for _, k := range []string{"rationale", "evidence", "notes", "locked"} {
			if ch[k] {
				d.add(fmt.Sprintf("%s: %s changed.", capital(what(o)), map[string]string{"rationale": "the reason (Why)", "evidence": "the evidence", "notes": "the notes", "locked": "the lock"}[k]))
			}
		}
	}
	if len(d.orderChanged(idsOf(a.Overlays), idsOf(b.Overlays))) > 0 {
		d.add("The graphics are listed in a different order.")
	}
}

func idsOf[T editspec.Overlay | editspec.AudioTrack](xs []T) []string {
	var out []string
	for _, x := range xs {
		switch v := any(x).(type) {
		case editspec.Overlay:
			out = append(out, v.ID)
		case editspec.AudioTrack:
			out = append(out, v.ID)
		}
	}
	return out
}

func (d *describer) orderChanged(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var ca []string
	for _, x := range a {
		if in[x] {
			ca = append(ca, x)
		}
	}
	inA := map[string]bool{}
	for _, x := range a {
		inA[x] = true
	}
	var cb []string
	for _, x := range b {
		if inA[x] {
			cb = append(cb, x)
		}
	}
	keep := lcsSet(ca, cb)
	var moved []string
	for _, x := range cb {
		if !keep[x] {
			moved = append(moved, x)
		}
	}
	return moved
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (d *describer) audio(a, b *editspec.Spec) {
	aa, ba := map[string]editspec.AudioTrack{}, map[string]editspec.AudioTrack{}
	for _, t := range a.Audio {
		aa[t.ID] = t
	}
	for _, t := range b.Audio {
		ba[t.ID] = t
	}
	for _, t := range a.Audio {
		if _, ok := ba[t.ID]; !ok {
			d.add(fmt.Sprintf("Removed the sound %s.", t.ID))
		}
	}
	for _, t := range b.Audio {
		old, ok := aa[t.ID]
		role := roleWord[t.Role]
		if role == "" {
			role = t.Role
		}
		if !ok {
			d.add(fmt.Sprintf("Added the sound %s (%s, %s %s → %s) starting at %s.", t.ID, role, d.name(t.AssetID), fmtClock(t.In), fmtClock(t.Out), fmtClock(t.At)))
			continue
		}
		ch := changedKeys(old, t)
		if ch["assetId"] || ch["in"] || ch["out"] {
			d.add(fmt.Sprintf("The sound %s now uses %s %s → %s.", t.ID, d.name(t.AssetID), fmtClock(t.In), fmtClock(t.Out)))
		}
		if ch["at"] {
			d.add(fmt.Sprintf("The sound %s now starts at %s (was %s).", t.ID, fmtClock(t.At), fmtClock(old.At)))
		}
		if ch["gainDb"] {
			if t.GainDB == nil {
				d.add(fmt.Sprintf("The sound %s is back to its normal volume.", t.ID))
			} else {
				d.add(fmt.Sprintf("The sound %s is now at %s dB.", t.ID, fmtNum(*t.GainDB)))
			}
		}
		if ch["role"] {
			d.add(fmt.Sprintf("The sound %s is now %s.", t.ID, role))
		}
		if ch["fadeIn"] || ch["fadeOut"] {
			d.add(fmt.Sprintf("The fades on %s changed.", t.ID))
		}
		if ch["duckUnderDialogue"] || ch["duckDb"] {
			if t.DuckUnderDialogue {
				d.add(fmt.Sprintf("The sound %s gets quieter under speech.", t.ID))
			} else {
				d.add(fmt.Sprintf("The sound %s no longer gets quieter under speech.", t.ID))
			}
		}
		if ch["rights"] {
			d.add(fmt.Sprintf("The rights for %s changed.", t.ID))
		}
		for _, k := range []string{"rationale", "notes", "locked"} {
			if ch[k] {
				d.add(fmt.Sprintf("The sound %s: %s changed.", t.ID, map[string]string{"rationale": "the reason (Why)", "notes": "the notes", "locked": "the lock"}[k]))
			}
		}
	}
	if len(d.orderChanged(idsOf(a.Audio), idsOf(b.Audio))) > 0 {
		d.add("The sounds are listed in a different order.")
	}
}

// logosOnly reports two brands that differ only in their logos (D198).
func logosOnly(a, b *editspec.Brand) bool {
	if a == nil || b == nil {
		return false
	}
	x, y := *a, *b
	x.Logo, x.LogoDark, x.LogoLight = "", "", ""
	y.Logo, y.LogoDark, y.LogoLight = "", "", ""
	return x == y
}

// sizeWord is a caption size in words (absent: medium).
func sizeWord(s string) string {
	if s == "" {
		return editspec.CaptionMedium
	}
	return s
}

func (d *describer) captions(a, b *editspec.Captions) {
	switch {
	case jsonEqual(a, b):
		return
	case a == nil:
		if b.Enabled {
			d.add("Captions are now on.")
		} else {
			d.add("Caption settings were added (captions are off).")
		}
		return
	case b == nil:
		d.add("Caption settings were removed (no captions).")
		return
	}
	if a.Enabled != b.Enabled {
		if b.Enabled {
			d.add("Captions are now on.")
		} else {
			d.add("Captions are now off.")
		}
	}
	ch := changedKeys(a, b)
	if ch["style"] {
		d.add(fmt.Sprintf("Caption style: %s → %s.", a.Style, b.Style))
	}
	if ch["position"] {
		d.add(fmt.Sprintf("Captions move from %s to %s.", a.Position, b.Position))
	}
	if ch["size"] {
		d.add(fmt.Sprintf("Captions are %s now (were %s).", sizeWord(b.Size), sizeWord(a.Size)))
	}
	if ch["maxWordsPerLine"] {
		d.add(fmt.Sprintf("Captions show up to %d words per line (was %d).", b.MaxWordsPerLine, a.MaxWordsPerLine))
	}
	if ch["uppercase"] {
		if b.Uppercase {
			d.add("Captions are now in capitals.")
		} else {
			d.add("Captions are no longer in capitals.")
		}
	}
	if ch["showSpeakers"] {
		if b.ShowSpeakers {
			d.add("Captions now show who is speaking.")
		} else {
			d.add("Captions no longer show who is speaking.")
		}
	}
	if ch["source"] {
		from := map[string]string{"transcript": "the transcripts", "dialogue": "what people say on camera", "voiceover": "the narration",
			"both": "the narration and what people say on camera", "manual": "the caption lines in the sheet"}[b.Source]
		if from == "" {
			from = "the transcripts"
		}
		d.add(fmt.Sprintf("Caption text now comes from %s.", from))
	}
	if ch["segments"] {
		d.add("The caption lines changed.")
	}
	if ch["emphasis"] {
		d.add("The emphasized caption words changed.")
	}
	if ch["locked"] {
		if b.Locked {
			d.add("Captions are now locked.")
		} else {
			d.add("Captions are now unlocked.")
		}
	}
}

func (d *describer) output(a, b editspec.Output) {
	if a.Width != b.Width || a.Height != b.Height || a.Preset != b.Preset {
		shape := shapeWord[b.Preset]
		if shape == "" {
			shape = "custom size"
		}
		d.add(fmt.Sprintf("The video is now %s (%d × %d), was %d × %d.", shape, b.Width, b.Height, a.Width, a.Height))
	}
	if a.FPS != b.FPS {
		d.add(fmt.Sprintf("Frame rate: %d → %d.", a.FPS, b.FPS))
	}
	if a.Platform != b.Platform {
		d.add(fmt.Sprintf("Made for %s now (was %s).", b.Platform, a.Platform))
	}
	if a.Background != b.Background {
		d.add(fmt.Sprintf("Background color: %s → %s.", a.Background, b.Background))
	}
	if a.Framing != b.Framing {
		switch b.Framing {
		case editspec.FramingFaces:
			d.add("Crops now keep faces in frame.")
		case editspec.FramingFollow:
			d.add("Crops now follow the subject as it moves.")
		default:
			d.add("Crops now keep the middle of the picture.")
		}
	}
}

func excerpt(s string) string {
	s = oneLine(s)
	if r := []rune(s); len(r) > 60 {
		return string(r[:59]) + "…"
	}
	return s
}

func (d *describer) voiceover(a, b *editspec.Voiceover) {
	switch {
	case jsonEqual(a, b):
		return
	case a == nil:
		d.add(fmt.Sprintf("Narration added: %s.", plural(len(b.Beats), "line")))
		if b.Layout == editspec.LayoutContinuous {
			d.add("The narration plays as one recording.")
		}
		d.narrationSettings(nil, b.Settings)
		return
	case b == nil:
		d.add("The narration was removed.")
		return
	}
	old := map[string]editspec.Beat{}
	for _, x := range a.Beats {
		old[x.ID] = x
	}
	inB := map[string]bool{}
	for _, x := range b.Beats {
		inB[x.ID] = true
		o, ok := old[x.ID]
		if !ok {
			d.add(fmt.Sprintf("New narration line %s: “%s”.", x.ID, excerpt(x.Text)))
			continue
		}
		said := false
		if o.Text != x.Text {
			d.add(fmt.Sprintf("Narration line %s now says “%s”.", x.ID, excerpt(x.Text)))
			said = true
		}
		if !jsonEqual(o.Anchor, x.Anchor) {
			if ph, ok := anchorPhrase(x.Anchor, d.name); ok {
				d.add(fmt.Sprintf("Narration line %s now starts %s.", x.ID, ph))
			} else {
				d.add(fmt.Sprintf("Narration line %s starts at a different point.", x.ID))
			}
			said = true
		}
		if !jsonEqual(o.Take, x.Take) {
			d.add(fmt.Sprintf("Narration line %s has different audio.", x.ID))
			said = true
		}
		if !jsonEqual(o.Voice, x.Voice) {
			d.add(fmt.Sprintf("Narration line %s uses a different voice.", x.ID))
			said = true
		}
		if o.Locked != x.Locked {
			if x.Locked {
				d.add(fmt.Sprintf("Narration line %s is now locked.", x.ID))
			} else {
				d.add(fmt.Sprintf("Narration line %s is now unlocked.", x.ID))
			}
			said = true
		}
		ox, xx := o, x
		ox.Text, xx.Text, ox.Anchor, xx.Anchor, ox.Take, xx.Take, ox.Voice, xx.Voice, ox.Locked, xx.Locked = "", "", editspec.Anchor{}, editspec.Anchor{}, nil, nil, nil, nil, false, false
		if !jsonEqual(ox, xx) {
			if said {
				d.add(fmt.Sprintf("Other details of narration line %s changed.", x.ID))
			} else {
				d.add(fmt.Sprintf("Narration line %s changed (delivery, volume, status or notes).", x.ID))
			}
		}
	}
	for _, x := range a.Beats {
		if !inB[x.ID] {
			d.add(fmt.Sprintf("Narration line %s was removed.", x.ID))
		}
	}
	var ao, bo []string
	for _, x := range a.Beats {
		if inB[x.ID] {
			ao = append(ao, x.ID)
		}
	}
	for _, x := range b.Beats {
		if _, ok := old[x.ID]; ok {
			bo = append(bo, x.ID)
		}
	}
	if strings.Join(ao, "\x00") != strings.Join(bo, "\x00") {
		d.add("The narration lines are in a different order.")
	}
	if a.Layout != b.Layout {
		if b.Layout == editspec.LayoutContinuous {
			d.add("The narration now plays as one recording.")
		} else {
			d.add("The narration now plays line by line.")
		}
	}
	if !jsonEqual(a.Continuous, b.Continuous) {
		d.add("The narration recording changed.")
	}
	d.narrationSettings(a.Settings, b.Settings)
	if a.Locked != b.Locked {
		if b.Locked {
			d.add("The narration is now locked.")
		} else {
			d.add("The narration is now unlocked.")
		}
	}
}

// fitWords says what happens to a narration line that runs long, per fit policy.
var fitWords = map[string]string{
	editspec.FitAuto:    "MetaRush now fits it automatically",
	editspec.FitAsk:     "MetaRush now asks you first",
	editspec.FitShorten: "it now gets rewritten shorter",
	"":                  "MetaRush is back to its usual handling",
}

func (d *describer) narrationSettings(a, b *editspec.VoiceoverSettings) {
	if jsonEqual(a, b) {
		return
	}
	var x, y editspec.VoiceoverSettings
	if a != nil {
		x = *a
	}
	if b != nil {
		y = *b
	}
	said := false
	if x.SpeakingRateWPM != y.SpeakingRateWPM {
		if y.SpeakingRateWPM == 0 {
			d.add("The narration's speaking rate is back to the default.")
		} else {
			d.add(fmt.Sprintf("The narration's speaking rate is now %s words a minute.", fmtNum(y.SpeakingRateWPM)))
		}
		said = true
	}
	if x.FitPolicy != y.FitPolicy {
		if w, ok := fitWords[y.FitPolicy]; ok {
			d.add("When a narration line runs long, " + w + ".")
			said = true
		}
	}
	if x.Gap != y.Gap {
		if y.Gap == 0 {
			d.add("The pause between narration lines is back to the default.")
		} else {
			d.add(fmt.Sprintf("The pause between narration lines is now %s.", fmtDur(y.Gap)))
		}
		said = true
	}
	if !said {
		d.add("The narration settings changed.")
	}
}

// levelWords says a bus level the way people hear it: "normal", "6 dB
// quieter", "muted" (the voice of `metarush mix show`).
func levelWords(b *editspec.Bus) string {
	if b == nil {
		return "normal"
	}
	var parts []string
	switch g := b.GainDB; {
	case g == nil || math.Abs(*g) < 0.05:
		if !b.Mute {
			parts = append(parts, "normal")
		}
	case *g < 0:
		parts = append(parts, fmtNum(-*g)+" dB quieter")
	default:
		parts = append(parts, fmtNum(*g)+" dB louder")
	}
	if b.Mute {
		parts = append(parts, "muted")
	}
	if b.Solo {
		parts = append(parts, "the only sound playing")
	}
	return strings.Join(parts, ", ")
}

// dips says how much quieter something gets while a voice plays.
func dips(v *float64) string {
	if v == nil {
		return "the usual amount"
	}
	return fmtNum(math.Abs(*v)) + " dB"
}

// loudnessGoal says what a loudness target means for people.
func loudnessGoal(v float64) string {
	switch {
	case v > -13:
		return "louder than most platforms play videos"
	case v >= -15:
		return "about as loud as YouTube and the social apps expect"
	case v > -18:
		return "a little quieter, about right for most other places"
	case v > -21:
		return "quieter than most platforms play videos"
	}
	return "at TV broadcast loudness, which is quiet for online videos"
}

func (d *describer) mix(a, b *editspec.Mix) {
	switch {
	case jsonEqual(a, b):
		return
	case b == nil:
		d.add("The mix settings were removed (back to the usual levels).")
		return
	}
	added, before := a == nil, len(d.out)
	if added {
		a = &editspec.Mix{}
	}
	if a.MuteSource != b.MuteSource {
		if b.MuteSource {
			d.add("The camera sound is now off.")
		} else {
			d.add("The camera sound is back on.")
		}
	}
	for _, n := range busNames {
		ab, bb := busOf(a, n.json), busOf(b, n.json)
		if !jsonEqual(ab, bb) {
			d.add(fmt.Sprintf("%s: now %s (was %s).", capitalize(n.word), levelWords(bb), levelWords(ab)))
		}
	}
	if !jsonEqual(a.Ducking, b.Ducking) {
		n := len(d.out)
		var x, y editspec.Ducking
		if a.Ducking != nil {
			x = *a.Ducking
		}
		if b.Ducking != nil {
			y = *b.Ducking
		}
		off := func(v *bool) bool { return v != nil && !*v }
		switch {
		case off(y.Enabled) && !off(x.Enabled):
			d.add("Music and clip sound no longer get quieter while someone speaks.")
		case off(x.Enabled) && !off(y.Enabled):
			d.add("Music and clip sound get quieter while someone speaks again.")
		}
		if !jsonEqual(x.MusicUnderVoiceoverDB, y.MusicUnderVoiceoverDB) {
			d.add(fmt.Sprintf("Music now gets %s quieter while the narration plays (was %s).", dips(y.MusicUnderVoiceoverDB), dips(x.MusicUnderVoiceoverDB)))
		}
		if !jsonEqual(x.SourceUnderVoiceoverDB, y.SourceUnderVoiceoverDB) {
			d.add(fmt.Sprintf("The clips' own sound now gets %s quieter while the narration plays (was %s).", dips(y.SourceUnderVoiceoverDB), dips(x.SourceUnderVoiceoverDB)))
		}
		if !jsonEqual(x.MusicUnderDialogueDB, y.MusicUnderDialogueDB) {
			d.add(fmt.Sprintf("Music now gets %s quieter while someone talks on camera (was %s).", dips(y.MusicUnderDialogueDB), dips(x.MusicUnderDialogueDB)))
		}
		if !jsonEqual(x.Attack, y.Attack) || !jsonEqual(x.Release, y.Release) {
			d.add("The music now fades down and back up around speech at a different speed.")
		}
		if len(d.out) == n {
			d.add("How the music and clip sound get quieter under speech changed.")
		}
	}
	if !jsonEqual(a.Loudness, b.Loudness) {
		n := len(d.out)
		var x, y editspec.MasterLoudness
		if a.Loudness != nil {
			x = *a.Loudness
		}
		if b.Loudness != nil {
			y = *b.Loudness
		}
		if !jsonEqual(x.TargetLUFS, y.TargetLUFS) {
			if y.TargetLUFS != nil {
				d.add(fmt.Sprintf("The whole video is now made %s.", loudnessGoal(*y.TargetLUFS)))
			} else {
				d.add("The whole video's loudness is back to the usual for its platform.")
			}
		}
		if !jsonEqual(x.TruePeakDBTP, y.TruePeakDBTP) {
			switch {
			case y.TruePeakDBTP == nil:
				d.add("The limit on the loudest moments is back to the usual.")
			case x.TruePeakDBTP == nil || *y.TruePeakDBTP < *x.TruePeakDBTP:
				d.add("The loudest moments now stay further from distorting.")
			default:
				d.add("The loudest moments may now come closer to distorting.")
			}
		}
		if !jsonEqual(x.Normalize, y.Normalize) {
			switch {
			case y.Normalize == nil:
				d.add("Whether the whole video is turned up or down to its loudness is back to the usual.")
			case *y.Normalize:
				d.add("The whole video is now turned up or down to reach that loudness.")
			default:
				d.add("The whole video is no longer turned up or down to reach a loudness.")
			}
		}
		if len(d.out) == n {
			d.add("The overall loudness settings changed.")
		}
	}
	if !jsonEqual(a.LevelSpeech, b.LevelSpeech) {
		if b.LevelSpeech != nil && *b.LevelSpeech {
			d.add("Voices are now evened out from clip to clip, so nobody sounds much louder or quieter than the others.")
		} else {
			d.add("Voices are no longer evened out from clip to clip.")
		}
	}
	if !jsonEqual(a.CutBlend, b.CutBlend) {
		if b.CutBlend != nil && *b.CutBlend > 0 {
			d.add(fmt.Sprintf("At straight cuts the sound now blends over %s, so it never clicks.", fmtDur(*b.CutBlend)))
		} else {
			d.add("At straight cuts the sound now switches at once.")
		}
	}
	d.said(a.Said, b.Said)
	if a.Locked != b.Locked {
		if b.Locked {
			d.add("The mix is now locked.")
		} else {
			d.add("The mix is now unlocked.")
		}
	}
	if added && len(d.out) == before {
		d.add("Mix settings were added.")
	}
}

// said says which words changed in a speaker's own voice (D308): the new
// words with what was said, and the changes taken back.
func (d *describer) said(a, b []editspec.SaidSound) {
	had := map[string]editspec.SaidSound{}
	for _, c := range a {
		had[c.ID] = c
	}
	has := map[string]bool{}
	for _, c := range b {
		has[c.ID] = true
		if o, ok := had[c.ID]; ok && jsonEqual(o, c) {
			continue
		}
		who := c.Who
		if who == "" {
			who = "The speaker"
		}
		d.add(fmt.Sprintf("%s now says “%s” in place of “%s”, in their own voice copy (marked as changed with AI).", who, c.Words, c.Before))
	}
	for _, c := range a {
		if !has[c.ID] {
			d.add(fmt.Sprintf("“%s” is heard again as it was said, in place of “%s”.", c.Before, c.Words))
		}
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// spanSentence says how long a spanning graphic lasts (D163).
func spanSentence(sp *editspec.OverlaySpan) string {
	switch {
	case sp == nil:
		return "a set time"
	case sp.From == "" && sp.To == "":
		return "the whole video"
	case sp.From == "":
		return "the start of the video to the end of " + sp.To
	case sp.To == "":
		return "the start of " + sp.From + " to the end of the video"
	}
	return "the start of " + sp.From + " to the end of " + sp.To
}

func timesWords(n int) string {
	switch n {
	case 0:
		return "never"
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}
