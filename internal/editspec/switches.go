package editspec

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Call recordings (D160–D162, docs/call-recordings.md): the footage's own
// picture switches found to the frame, who is on screen between them, the
// names a call app printed in the picture, split-second flashes of the other
// picture at a clip's edges and the fix that trims at the switch, and the
// built-in name bar that covers the printed names.

// PictureSwitch is one switch of a footage's picture (a cut inside it):
// At is the first frame of the new picture, Last the last of the old one, in
// seconds of the footage.
type PictureSwitch struct {
	At   float64 `json:"at"`
	Last float64 `json:"last"`
}

// FootageShot is the footage between two switches and who is on screen.
type FootageShot struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
	// Person is who is on screen ("" when nobody is known); By says how
	// MetaRush knows: face (a face a person named), printed (the name the call
	// app printed, matched to People or as written), looks (a face recognized
	// by looks, not confirmed), voice (who talks: unsure), member (the
	// conversation member shown alone, D182).
	Person  string `json:"person,omitempty"`
	By      string `json:"by,omitempty" jsonschema:"enum=face,enum=printed,enum=looks,enum=voice,enum=member"`
	Printed string `json:"printed,omitempty" jsonschema:"description=The name printed in the picture, as read."`
	// Cover is where this shot's printed name is, in fractions of the clip's
	// picture, when it differs from the footage's names area (a nested cut's
	// clips are framed differently, D182).
	Cover *CoverBox `json:"cover,omitempty"`
	// member is the conversation member shown alone (D182; not sent);
	// noNames says this stretch shows no printed name although its clip's
	// other footage does (a nested cut's b-roll): the name bar hides there.
	member  string
	noNames bool
}

// PrintedNames is where a call app printed people's names in the picture.
type PrintedNames struct {
	Corner string   `json:"corner" jsonschema:"enum=bottom-left,enum=bottom-right,enum=top-left,enum=top-right"`
	Box    CoverBox `json:"box" jsonschema:"description=The area to cover\\, in fractions of the footage's picture."`
	Shots  int      `json:"shots" jsonschema:"description=How many shots show a printed name there."`
}

// FootageShots is what MetaRush knows about a footage's picture switches.
type FootageShots struct {
	Switches []PictureSwitch `json:"switches"`
	// Precision is how far the original's switch can be from At (seconds):
	// 0 when found on the original's own frames.
	Precision float64       `json:"precision"`
	Shots     []FootageShot `json:"shots"`
	Names     *PrintedNames `json:"names,omitempty"`
}

// ShotAt is the shot at footage time t (nil when there is none).
func (f *FootageShots) ShotAt(t float64) *FootageShot {
	if f == nil {
		return nil
	}
	for i := range f.Shots {
		if t >= f.Shots[i].From-1e-6 && t < f.Shots[i].To-1e-6 {
			return &f.Shots[i]
		}
	}
	if n := len(f.Shots); n > 0 && t >= f.Shots[n-1].From {
		return &f.Shots[n-1]
	}
	return nil
}

// SwitchFacts is optionally implemented by Facts: a footage's picture
// switches, who is on screen and its printed names (ok false when MetaRush
// hasn't looked at the footage that way).
type SwitchFacts interface {
	Footage(assetID string) (*FootageShots, bool)
}

func switchFacts(f Facts, id string) (*FootageShots, bool) {
	if f == nil || IsConversation(id) {
		return nil, false // a conversation clip's depend on its shots (clipFootage)
	}
	if nf, ok := f.(*nestFacts); ok {
		return nf.Footage(id)
	}
	if IsNested(id) {
		return nil, false
	}
	if sf, ok := f.(SwitchFacts); ok {
		return sf.Footage(id)
	}
	return nil, false
}

// Footage implements SwitchFacts: a conversation clip's depend on its shots
// (convClipFootage); everything else is the facts' own.
func (cf *convFacts) Footage(id string) (*FootageShots, bool) {
	if IsConversation(id) {
		return nil, false
	}
	return switchFacts(cf.base, id)
}

// Footage implements SwitchFacts: a nested cut's on its own timeline (D182),
// the outer cut's own footage from the facts it wraps.
func (f *nestFacts) Footage(id string) (*FootageShots, bool) {
	if IsNested(id) {
		return f.cuts[id].footage()
	}
	return switchFacts(f.base, id)
}

// FlashMax is the longest sliver of the other picture at a clip's edge that
// counts as a flash (seconds of footage).
const FlashMax = 0.75

// EdgeFlash is a split-second of another picture at a clip's start or end.
type EdgeFlash struct {
	ClipID string        `json:"clipId"`
	Index  int           `json:"index"`
	Edge   string        `json:"edge" jsonschema:"enum=start,enum=end"`
	Switch PictureSwitch `json:"switch"`
	// Seconds of the footage the flash lasts; Frames of it the cut shows.
	Seconds float64 `json:"seconds"`
	Frames  int     `json:"frames"`
	// Flash is who the flash shows, Main who the clip shows otherwise ("" unknown).
	Flash string `json:"flash,omitempty"`
	Main  string `json:"main,omitempty"`
	// Words: the stretch a trim takes out has words in it.
	Words bool `json:"words,omitempty"`
	// Precision of the switch (seconds).
	Precision float64 `json:"precision"`
}

// sourceFPS is a footage's frame rate (30 when unknown).
func sourceFPS(facts Facts, id string) float64 {
	if a, ok := facts.Asset(id); ok && a.FPS > 0 {
		return a.FPS
	}
	return 30
}

// StartAfter is the in-point on the clip's frame grid whose first frame shows
// footage time t or later: the smallest k/fps ≥ t (milliseconds kept).
func StartAfter(fps float64, t float64) float64 {
	k := math.Ceil(t*fps - 1e-6)
	return round3(k / fps)
}

// EndBefore is the out-point for clip c whose last frame shows footage at
// least margin before t, on the clip's frame grid (milliseconds kept). It
// returns c.In when no frame fits.
func EndBefore(c Clip, fps float64, t, margin float64) float64 {
	rate := speedOf(c)
	trim := math.Round(c.In * fps)
	n := math.Floor(((t-margin)*fps-trim)/rate+1e-9) + 1
	if n < 1 {
		return c.In
	}
	out := round3(c.In + n*rate/fps)
	// The rounding to milliseconds must give back n frames (Timeline).
	for k := 0; k < 3 && math.Round((out-c.In)/rate*fps) > n; k++ {
		out = round3(out - 0.001)
	}
	return out
}

// framesShowing counts the clip's frames whose footage time is in [from, to).
func framesShowing(pc PlanClip, fps float64, from, to float64) int {
	n := 0
	for k := 0; k < pc.DurationInFrames; k++ {
		t := sampleAt(pc, fps, k)
		if t >= from-1e-9 && t < to-1e-9 {
			n++
		}
	}
	return n
}

// EdgeFlashes finds split-second slivers of another picture at the start and
// end of the cut's clips (D161), from the footage's frame-accurate switches.
func EdgeFlashes(s *Spec, facts Facts) []EdgeFlash {
	if facts == nil {
		return nil
	}
	s = Clone(s)
	Normalize(s)
	return edgeFlashes(s, wrapFacts(s, facts))
}

// edgeFlashes is EdgeFlashes for a normalized spec and wrapped facts.
func edgeFlashes(s *Spec, facts Facts) []EdgeFlash {
	if facts == nil {
		return nil
	}
	clips, _ := Timeline(s)
	fps := specFPS(s)
	var out []EdgeFlash
	for i, c := range s.Clips {
		fs, ok := clipFootage(s, i, facts)
		if !ok || fs == nil || len(fs.Switches) == 0 {
			continue
		}
		pc := clips[i]
		tx := facts.Transcript(c.AssetID)
		margin := math.Max(fs.Precision, 0.5/sourceFPS(facts, c.AssetID)) + 0.001
		var inside []PictureSwitch
		for _, sw := range fs.Switches {
			if sw.At > c.In+1e-3 && sw.At < c.Out-1e-3 {
				inside = append(inside, sw)
			}
		}
		if len(inside) == 0 {
			continue
		}
		person := func(t float64) string {
			if sh := fs.ShotAt(t); sh != nil {
				return sh.Person
			}
			return ""
		}
		same := func(a, b string) bool { return a != "" && strings.EqualFold(a, b) }
		if sw := inside[0]; sw.At-c.In <= FlashMax {
			next := c.Out
			if len(inside) > 1 {
				next = inside[1].At
			}
			main := next - sw.At
			frames := framesShowing(pc, fps, c.In, sw.At)
			if frames > 0 && main >= 2*(sw.At-c.In) && main >= 0.5 {
				f := EdgeFlash{ClipID: c.ID, Index: i, Edge: "start", Switch: sw, Seconds: round3(sw.At - c.In), Frames: frames,
					Flash: person(c.In + 1e-3), Main: person(sw.At + 1e-3), Precision: fs.Precision}
				f.Words = wordsIn(tx, c.In, StartAfter(fps, sw.At))
				if !same(f.Flash, f.Main) {
					out = append(out, f)
				}
			}
		}
		if sw := inside[len(inside)-1]; c.Out-sw.At <= FlashMax && (len(inside) > 1 || sw.At-c.In > FlashMax) {
			prev := c.In
			if len(inside) > 1 {
				prev = inside[len(inside)-2].At
			}
			main := sw.At - prev
			frames := framesShowing(pc, fps, sw.At-margin+0.001, c.Out+1)
			if frames > 0 && main >= 2*(c.Out-sw.At) && main >= 0.5 {
				f := EdgeFlash{ClipID: c.ID, Index: i, Edge: "end", Switch: sw, Seconds: round3(c.Out - sw.At), Frames: frames,
					Flash: person(sw.At + 1e-3), Main: person(sw.At - 0.05), Precision: fs.Precision}
				f.Words = wordsIn(tx, EndBefore(c, fps, sw.At, margin), c.Out)
				if !same(f.Flash, f.Main) {
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// wordsIn reports a word said (in part) between from and to of the footage.
func wordsIn(segs []TranscriptSegment, from, to float64) bool {
	for _, sg := range segs {
		for _, w := range sg.Words {
			if w.From < to && w.To > from {
				return true
			}
		}
		if len(sg.Words) == 0 && sg.From < to && sg.To > from && strings.TrimSpace(sg.Text) != "" {
			return true
		}
	}
	return false
}

// EdgeFlashFix is the change that removes a flash (D161): the clip trimmed to
// the exact switch on its frame grid, and when that takes words out, its sound
// starting that much earlier (a J-cut), or for a flash at its end its sound
// carrying on over the next clip (an L-cut), so nothing said is lost. Locked
// clips get no ops.
func EdgeFlashFix(s *Spec, f EdgeFlash, facts Facts) []Op {
	if f.Index < 0 || f.Index >= len(s.Clips) || s.Clips[f.Index].ID != f.ClipID || s.Clips[f.Index].Locked {
		return nil
	}
	c := s.Clips[f.Index]
	fps := specFPS(s)
	var ops []Op
	if f.Edge == "start" {
		to := StartAfter(fps, f.Switch.At)
		ops = append(ops, Op{Op: "trimClip", ID: c.ID, Edge: "in", To: &to})
		if lead := round3(to - c.In); f.Words && f.Index > 0 && lead > 0 && soundUnset(c.Transition) {
			tr := Transition{Type: TransitionCut}
			if c.Transition != nil {
				tr = *c.Transition
			}
			tr.Audio = &SoundTransition{Mode: SoundLead, Duration: math.Min(3, lead)}
			ops = append(ops, Op{Op: "setTransition", ID: c.ID, Transition: SetTransitionValue(&tr)})
		}
		return ops
	}
	margin := math.Max(f.Precision, 0.5/sourceFPS(facts, c.AssetID)) + 0.001
	to := EndBefore(c, fps, f.Switch.At, margin)
	if to <= c.In {
		return nil
	}
	ops = append(ops, Op{Op: "trimClip", ID: c.ID, Edge: "end", To: &to})
	if carry := round3(c.Out - to); f.Words && f.Index+1 < len(s.Clips) && carry > 0 {
		next := s.Clips[f.Index+1]
		if !next.Locked && soundUnset(next.Transition) {
			tr := Transition{Type: TransitionCut}
			if next.Transition != nil {
				tr = *next.Transition
			}
			tr.Audio = &SoundTransition{Mode: SoundCarry, Duration: math.Min(3, carry)}
			ops = append(ops, Op{Op: "setTransition", ID: next.ID, Transition: SetTransitionValue(&tr)})
		}
	}
	return ops
}

// soundUnset: the cut doesn't say how its sound changes yet.
func soundUnset(t *Transition) bool { return t == nil || t.Audio == nil }

// FlashWords says a flash in a person's words: "opens with a split-second of
// Maya (0.2 s) before Lena appears".
func FlashWords(f EdgeFlash) string {
	who := "another shot"
	if f.Flash != "" {
		who = f.Flash
	}
	long := "a split-second"
	if f.Seconds >= 0.5 {
		long = "under a second"
	}
	if f.Edge == "start" {
		if f.Main != "" {
			return fmt.Sprintf("opens with %s of %s (%s) before %s appears", long, who, seconds(f.Seconds), f.Main)
		}
		return fmt.Sprintf("opens with %s of %s (%s) before its picture switches", long, who, seconds(f.Seconds))
	}
	if f.Main != "" {
		return fmt.Sprintf("ends with %s of %s (%s) after %s", long, who, seconds(f.Seconds), f.Main)
	}
	return fmt.Sprintf("ends with %s of %s (%s) after its picture switches", long, who, seconds(f.Seconds))
}

// callIssues are the edit check's warnings about call footage (D161, D162):
// flashes at clip edges, and names the call app printed that no name bar covers.
func callIssues(s *Spec, facts Facts) []Issue {
	var out []Issue
	for _, f := range edgeFlashes(s, facts) {
		fix := "start the clip at the switch"
		if f.Edge == "end" {
			fix = "end the clip just before the switch"
		}
		if f.Words {
			fix += ", with the sound running on so no words are lost"
		}
		out = append(out, warnI("edge_flash", fmt.Sprintf("/clips/%d", f.Index), "%s %s; %s (Trim the flashes does it).", upperFirst(clipWords(s.Clips[f.Index])), FlashWords(f), fix))
	}
	if n, corner := printedNameClips(s, facts); n > 0 && !hasNameBar(s) {
		out = append(out, warnI("printed_names", "/overlays", "Names printed by the call app show at the %s in %s; cover them with your own name bar (Cover the printed names).", strings.ReplaceAll(corner, "-", " "), pluralWords(n, "clip", "clips")))
	}
	return out
}

// printedNameClips counts the clips whose footage shows printed names, and
// the corner most of them use.
func printedNameClips(s *Spec, facts Facts) (int, string) {
	n := 0
	corners := map[string]int{}
	for i := range s.Clips {
		if fs, ok := clipFootage(s, i, facts); ok && fs != nil && fs.Names != nil {
			n++
			corners[fs.Names.Corner]++
		}
	}
	best := ""
	for k, v := range corners {
		if best == "" || v > corners[best] || (v == corners[best] && k < best) {
			best = k
		}
	}
	return n, best
}

func hasNameBar(s *Spec) bool {
	for _, o := range s.Overlays {
		if o.Template == TplNameBar {
			return true
		}
	}
	return false
}

// NameBarOptions say how the built-in name bar is written (cut.names).
type NameBarOptions struct {
	ID         string // the graphic's name (default "names")
	FirstNames bool   // only first names
	Style      string // band | tab
	KeepTitles bool   // keep the person-name lower thirds
	// Height (full | half) and Animate (fade | slide | never): the house
	// style's name bar (D199).
	Height  string
	Animate string
}

// NameBarPlan is the change that covers the printed names with the built-in
// name bar (D162): the graphic (one for the whole video, changing at every
// picture switch of call footage, hidden over other footage) and the
// person-name lower thirds it replaces.
type NameBarPlan struct {
	Overlay  Overlay  `json:"overlay"`
	Replaces bool     `json:"replaces" jsonschema:"description=A name bar of that name is already in the cut: it is replaced."`
	Removed  []string `json:"removed" jsonschema:"description=Person-name titles it takes out."`
	Clips    int      `json:"clips" jsonschema:"description=Clips of call footage it covers."`
	Changes  int      `json:"changes"`
	Unknown  int      `json:"unknown" jsonschema:"description=Shots of call footage where nobody is known (the bar shows no name)."`
	People   []string `json:"people"`
}

// firstName is the first word of a name ("Lena O." → "Lena").
func firstName(n string) string {
	f := strings.Fields(n)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimRight(f[0], ",.;:")
}

// coverIn maps a footage area (fractions of its picture) into the output
// picture through a clip's crop (the renderer's placement, CropRect), clamped
// to the picture. ok is false when none of it is in the picture.
func coverIn(pc PlanClip, out Output, b CoverBox) (CoverBox, bool) {
	sw, sh := float64(pc.SourceWidth), float64(pc.SourceHeight)
	W, H := float64(out.Width), float64(out.Height)
	if sw <= 0 || sh <= 0 || W <= 0 || H <= 0 {
		return b, true
	}
	left, top, w, h := CropRect(pc.Framing, sw, sh, W, H, 0)
	x0, y0 := (left+b.X*w)/W, (top+b.Y*h)/H
	x1, y1 := (left+(b.X+b.W)*w)/W, (top+(b.Y+b.H)*h)/H
	clamp := func(v float64) float64 { return math.Max(0, math.Min(1, v)) }
	x0, y0, x1, y1 = clamp(x0), clamp(y0), clamp(x1), clamp(y1)
	if x1-x0 <= 0.001 || y1-y0 <= 0.001 {
		return CoverBox{}, false
	}
	return CoverBox{X: round3(x0), Y: round3(y0), W: round3(x1 - x0), H: round3(y1 - y0)}, true
}

// BuildNameBar writes the built-in name bar for a cut (D162) from the
// footage's switches, who is on screen and where the names are printed. ok is
// false when no clip's footage shows printed names.
func BuildNameBar(s *Spec, facts Facts, o NameBarOptions) (NameBarPlan, bool) {
	s = Clone(s)
	Normalize(s)
	facts = wrapFacts(s, facts)
	id := o.ID
	if id == "" {
		id = "names"
	}
	clips, _ := Timeline(s)
	plan := Resolve(Clone(s), facts, "", "", "") // placement (crop) per clip
	var changes []OverlayChange
	people := map[string]bool{}
	seenFootage := map[string]bool{}
	var first *CoverBox
	callClips := 0
	unknown := 0
	name := func(n string) string {
		if o.FirstNames {
			return firstName(n)
		}
		return n
	}
	fps := specFPS(s)
	for i, c := range s.Clips {
		fs, ok := clipFootage(s, i, facts)
		if !ok || fs == nil || fs.Names == nil {
			continue
		}
		callClips++
		pc := clips[i]
		if i < len(plan.Clips) {
			pc = plan.Clips[i]
		}
		conv := IsConversation(c.AssetID)
		cover, inPicture := coverIn(pc, s.Output, fs.Names.Box)
		if conv {
			inPicture = false // each shot's own tile (convShotCover)
		}
		if first == nil && inPicture {
			cp := cover
			first = &cp
		}
		if seenFootage[c.AssetID] && !conv {
			continue // one set of changes per footage covers every clip of it
		}
		seenFootage[c.AssetID] = true
		for _, sh := range fs.Shots {
			shotCover, shotIn := cover, inPicture
			switch {
			case conv:
				// A conversation: the recording shown alone, through its tile;
				// in a layout each tile's own name tag covers its name (D182).
				shotCover, shotIn = convShotCover(pc, c, sh.From, facts, s.Output, fps)
			case sh.Cover != nil:
				shotCover, shotIn = coverIn(pc, s.Output, *sh.Cover)
			}
			if sh.noNames || ((conv || sh.Cover != nil) && !shotIn) {
				b, _ := json.Marshal(map[string]any{"hide": true})
				changes = append(changes, OverlayChange{Footage: c.AssetID, At: round3(math.Max(sh.From, c.In)), Params: b})
				continue
			}
			if first == nil && shotIn {
				cp := shotCover
				first = &cp
			}
			fields := map[string]any{"hide": false}
			if sh.Person != "" {
				fields["name"] = name(sh.Person)
				people[name(sh.Person)] = true
			} else {
				fields["name"] = ""
				unknown++
			}
			if shotIn && first != nil && (shotCover != *first) {
				fields["cover"] = shotCover
			}
			b, _ := json.Marshal(fields)
			at := sh.From
			if conv {
				at = math.Max(sh.From, c.In)
			}
			changes = append(changes, OverlayChange{Footage: c.AssetID, At: round3(at), Params: b})
		}
	}
	if callClips == 0 {
		return NameBarPlan{}, false
	}
	base := map[string]any{"hide": true}
	if first != nil {
		base["cover"] = *first
	}
	if o.FirstNames {
		base["firstNames"] = true // a conversation's tile tags follow it (D182)
	}
	if o.Style != "" && o.Style != "band" {
		base["style"] = o.Style
	}
	// The house style's height and animation (D199).
	if o.Height != "" && o.Height != NameBarFull {
		base["height"] = o.Height
	}
	if o.Animate != "" && o.Animate != NameBarFade {
		base["animate"] = o.Animate
	}
	bb, _ := json.Marshal(base)
	ov := Overlay{ID: id, Template: TplNameBar, Span: &OverlaySpan{}, Params: bb, Changes: trimChanges(changes, s)}
	out := NameBarPlan{Overlay: ov, Clips: callClips, Changes: len(ov.Changes), Unknown: unknown}
	for _, x := range s.Overlays {
		if x.ID == id {
			out.Replaces = true
		}
		if !o.KeepTitles && personTitle(x, people) {
			out.Removed = append(out.Removed, x.ID)
		}
	}
	for p := range people {
		out.People = append(out.People, p)
	}
	sortStrings(out.People)
	return out, true
}

// trimChanges keeps the changes a cut can show: for each footage, those inside
// its clips' ranges plus the last one before each clip's start (the shot the
// clip starts in).
func trimChanges(cs []OverlayChange, s *Spec) []OverlayChange {
	byFootage := map[string][]Clip{}
	for _, c := range s.Clips {
		byFootage[c.AssetID] = append(byFootage[c.AssetID], c)
	}
	keep := make([]bool, len(cs))
	for i, ch := range cs {
		for _, c := range byFootage[ch.Footage] {
			if ch.At >= c.In-1e-6 && ch.At < c.Out {
				keep[i] = true
			}
		}
	}
	// The last change at or before each clip's start.
	for _, clipsOf := range byFootage {
		for _, c := range clipsOf {
			best := -1
			for i, ch := range cs {
				if clipsOf[0].AssetID == ch.Footage && ch.At <= c.In+1e-6 && (best < 0 || ch.At >= cs[best].At) {
					best = i
				}
			}
			if best >= 0 {
				keep[best] = true
			}
		}
	}
	var out []OverlayChange
	for i, ch := range cs {
		if keep[i] {
			out = append(out, ch)
		}
	}
	// Drop a change that repeats the fields already showing for that footage.
	var dedup []OverlayChange
	last := map[string]string{}
	for _, ch := range out {
		if last[ch.Footage] == string(ch.Params) {
			continue
		}
		last[ch.Footage] = string(ch.Params)
		dedup = append(dedup, ch)
	}
	return dedup
}

// personTitle reports a person-name lower third (one the name bar replaces).
func personTitle(o Overlay, people map[string]bool) bool {
	if o.Template != TplLowerThirdClean && o.Template != TplLowerThirdCinematic {
		return false
	}
	var p LowerThirdParams
	if json.Unmarshal(o.Params, &p) != nil || p.Name == "" {
		return false
	}
	if len(people) == 0 {
		return true
	}
	for n := range people {
		if strings.EqualFold(firstName(n), firstName(p.Name)) {
			return true
		}
	}
	return false
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// NameMismatch is where a cut's name bar names someone else than who is on
// screen (D162): the quality check's name_bar_mismatch.
type NameMismatch struct {
	At       float64 `json:"at" jsonschema:"description=Timeline seconds."`
	To       float64 `json:"to"`
	Shows    string  `json:"shows"`
	OnScreen string  `json:"onScreen"`
	ClipID   string  `json:"clipId"`
}

// NameBarMismatches compares a resolved name bar with who is on screen.
func NameBarMismatches(s *Spec, plan *RenderPlan, facts Facts, minSec float64) []NameMismatch {
	var out []NameMismatch
	if plan == nil || facts == nil {
		return nil
	}
	s = Clone(s)
	Normalize(s)
	facts = wrapFacts(s, facts)
	fps := float64(plan.FPS)
	for _, po := range plan.Overlays {
		if po.Template != TplNameBar {
			continue
		}
		segs := po.Segments
		if len(segs) == 0 {
			segs = []PlanSegment{{From: 0, Params: po.Params}}
		}
		for i, pc := range plan.Clips {
			if i >= len(s.Clips) || s.Clips[i].AssetID != pc.AssetID {
				continue
			}
			fs, ok := clipFootage(s, i, facts)
			if !ok || fs == nil || fs.Names == nil {
				continue
			}
			lo, hi := max(pc.From, po.From), min(pc.From+pc.DurationInFrames, po.From+po.DurationInFrames)
			for _, sh := range fs.Shots {
				if sh.Person == "" {
					continue
				}
				// The frames of this clip that show this shot.
				f0 := pc.From + firstFrameAtOrAfter(pc, fps, sh.From)
				f1 := pc.From + firstFrameAtOrAfter(pc, fps, sh.To)
				f0, f1 = max(f0, lo), min(f1, hi)
				if f1-f0 < int(minSec*fps) {
					continue
				}
				mid := (f0+f1)/2 - po.From
				var fields NameBarParams
				for _, sg := range segs {
					if sg.From <= mid {
						_ = json.Unmarshal(sg.Params, &fields)
					}
				}
				if fields.Hide || fields.Name == "" {
					continue
				}
				if !strings.EqualFold(firstName(fields.Name), firstName(sh.Person)) {
					out = append(out, NameMismatch{At: round3(float64(f0) / fps), To: round3(float64(f1) / fps), Shows: fields.Name, OnScreen: sh.Person, ClipID: plan.Clips[i].ID})
				}
			}
		}
	}
	return out
}
