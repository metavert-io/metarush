package recipes

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// Assembling a run's cut (D205): Go builds the fixed sections from the recipe
// and locks them; a chosen section gets stand-in clips for Claude to replace
// through an ordinary cut sheet (or, when its footage is what the recipe was
// saved with, the moments chosen then). A run again (rebind) starts from the
// cut's newest version and changes only what the changed inputs reach, so the
// edits people made since stay.

// madeClip is a clip the run made for a section (a Remotion composition drawn
// with the run's words, a generated shot from the run's words).
type madeClip = models.RecipeMadeClip

// assemblyIn is what a run's cut is made from.
type assemblyIn struct {
	Doc    *models.RecipeDoc
	Values map[string]*models.RecipeRunInput
	// Base is the cut's newest version when the run makes the cut again
	// (rebind); nil builds it from the recipe.
	Base *editspec.Spec
	// Changed are the inputs that differ from the run this one repeats
	// (rebind) or from the recipe's defaults (build).
	Changed map[string]bool
	// Made are clips the run made, by the recipe's clip names.
	Made map[string]madeClip
	// Voice is the voice the narration's voice input names.
	Voice *editspec.VoiceRef
	// DefaultVoice is the voice the recipe was saved with (the lines read in
	// it follow the voice input).
	DefaultVoice string
	// Run names the run in stand-ins' notes ("run 4 of “Event recap”").
	Run string
	// Moments are the footage's moments by clip (footage.moments), from which
	// chosen sections get their first picks (D215); none: stand-ins.
	Moments map[string][]ops.Moment
}

// chosenClips are what a chosen section starts with for Claude: first picks
// from its footage's moments (D215), else stand-ins spread over the footage.
func chosenClips(sec models.RecipeSection, in assemblyIn, taken map[string]bool) ([]editspec.Clip, []models.RecipeStandIn) {
	v := in.Values[sec.From]
	if picks := firstPicks(sec, v, in.Moments); len(picks) > 0 {
		return pickClips(sec, picks, in.Run, taken)
	}
	clips := standIns(sec, v, in.Run, taken)
	recs := make([]models.RecipeStandIn, 0, len(clips))
	for _, cl := range clips {
		recs = append(recs, models.RecipeStandIn{Section: sec.Name, ClipID: cl.ID, AssetID: cl.AssetID, In: cl.In, Out: cl.Out})
	}
	return clips, recs
}

// assembly is a run's cut, before it is saved.
type assembly struct {
	Spec     *editspec.Spec
	StandIns []models.RecipeStandIn
	// Notes are things it couldn't do, plainly ("the graphic “end” isn't in
	// the cut any more, so its headline stayed").
	Notes []string
}

// assemble makes a run's cut.
func assemble(in assemblyIn) *assembly {
	if in.Base != nil {
		return rebind(in)
	}
	return build(in)
}

// build makes a run's first cut from the recipe.
func build(in assemblyIn) *assembly {
	d := in.Doc
	out := &assembly{}
	c := d.Cut
	s := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Title: c.Title, Output: c.Output, Audio: cloneAudio(c.Audio), Captions: cloneCaptions(c.Captions),
		Overlays: cloneOverlays(c.Overlays), Brand: cloneBrand(c.Brand), Notes: c.Notes, Voiceover: cloneVoiceover(c.Voiceover), Mix: cloneMix(c.Mix),
		Resources: map[string]editspec.ResourcePin{}, Conversations: map[string]editspec.ConversationPin{}, Fixed: map[string]editspec.FixedPin{}}
	for k, v := range c.Resources {
		s.Resources[k] = v
	}
	for k, v := range c.Conversations {
		s.Conversations[k] = v
	}
	for k, v := range c.Fixed {
		s.Fixed[k] = v
	}
	remap := map[string]string{}  // a saved clip's name → the clip in its place now
	remapped := map[string]bool{} // saved clips a stand-in replaced
	taken := map[string]bool{}
	for _, sec := range d.Sections {
		for _, cl := range sec.Clips {
			taken[cl.ID] = true
		}
	}
	for _, sec := range d.Sections {
		switch sec.Kind {
		case models.RecipeFixed:
			for _, cl := range sec.Clips {
				cl = cloneClip(cl)
				cl.Locked = true
				bindClip(in, &cl, out)
				s.Clips = append(s.Clips, cl)
			}
		case models.RecipeChosen:
			if !in.Changed[sec.From] && len(sec.Clips) > 0 {
				// The same footage the recipe was saved with: the moments
				// chosen then (people and Claude can still change them).
				for _, cl := range sec.Clips {
					s.Clips = append(s.Clips, cloneClip(cl))
				}
				continue
			}
			ins, recs := chosenClips(sec, in, taken)
			s.Clips = append(s.Clips, ins...)
			out.StandIns = append(out.StandIns, recs...)
			for k, cl := range sec.Clips {
				j := k
				if j >= len(ins) {
					j = len(ins) - 1
				}
				if j >= 0 {
					remap[cl.ID] = ins[j].ID
					remapped[cl.ID] = true
				}
			}
		}
	}
	fixedClip := map[string]bool{}
	for _, sec := range d.Sections {
		if sec.Kind == models.RecipeFixed {
			for _, cl := range sec.Clips {
				fixedClip[cl.ID] = true
			}
		}
	}
	// Graphics follow their clips (their spans were set when the recipe was
	// saved); those inside fixed sections are locked.
	for i := range s.Overlays {
		o := &s.Overlays[i]
		if o.Span != nil {
			if n, ok := remap[o.Span.From]; ok {
				o.Span.From = n
			}
			if n, ok := remap[o.Span.To]; ok {
				o.Span.To = n
			}
			if fixedClip[o.Span.From] && fixedClip[o.Span.To] {
				o.Locked = true
			}
		}
	}
	remapBeats(s, d, remap, remapped)
	bindParts(in, s, out, false)
	followFootage(s, in)
	pruneFixed(s)
	out.Spec = s
	return out
}

// followFootage gives the new footage a run brings the clearer voice the
// recipe set for the recording it replaces (D295): a setting for a whole
// recording (mix.voices with its assetId) — the recipe's default footage, or the
// clips of a section that chooses from it or is bound to it — moves to each clip
// the run gives that input, so the run's sound step makes their voices clearer
// the same way (priced first when in the cloud). Settings for a person by name
// follow them already.
func followFootage(s *editspec.Spec, in assemblyIn) {
	if s.Mix == nil || len(s.Mix.Voices) == 0 {
		return
	}
	d := in.Doc
	moves := map[string][]string{} // a recording the recipe had → the clips the run gives in its place
	for _, x := range d.Inputs {
		v := in.Values[x.Name]
		if x.Kind != models.RecipeFootage || v == nil || len(v.Items) == 0 || !in.Changed[x.Name] {
			continue
		}
		var news []string
		for _, it := range v.Items {
			if it.ID != "" {
				news = append(news, it.ID)
			}
		}
		olds := append([]string{}, x.Default.Items...)
		for _, sec := range d.Sections {
			for _, c := range sec.Clips {
				if sec.Kind == models.RecipeChosen && sec.From == x.Name {
					olds = append(olds, c.AssetID)
				}
				for _, b := range bindingsOf(d, x.Name) {
					if b.Kind == models.BindClip && b.Where == c.ID {
						olds = append(olds, c.AssetID)
					}
				}
			}
		}
		for _, o := range olds {
			if strings.HasPrefix(o, "ast_") && !containsString(news, o) {
				moves[o] = news
			}
		}
	}
	if len(moves) == 0 {
		return
	}
	var voices []editspec.VoiceSound
	have := map[string]bool{}
	for _, vs := range s.Mix.Voices {
		if vs.AssetID != "" {
			have[vs.AssetID] = true
		}
	}
	for _, vs := range s.Mix.Voices {
		news, ok := moves[vs.AssetID]
		if vs.AssetID == "" || !ok {
			voices = append(voices, vs)
			continue
		}
		for _, id := range news {
			if have[id] {
				continue // a setting of its own wins
			}
			have[id] = true
			cp := cloneJSON(vs)
			cp.AssetID = id
			voices = append(voices, cp)
		}
	}
	s.Mix.Voices = voices
}

// rebind makes a run's cut again from its newest version: only what the
// changed inputs reach changes.
func rebind(in assemblyIn) *assembly {
	d := in.Doc
	out := &assembly{}
	s := editspec.Clone(in.Base)
	if s.Resources == nil {
		s.Resources = map[string]editspec.ResourcePin{}
	}
	// Footage bound to clips, and clips the run made again.
	for i := range s.Clips {
		cl := &s.Clips[i]
		if rc, _ := clipByID(d, cl.ID); rc != nil {
			bindClip(in, cl, out)
		}
	}
	// Chosen sections whose footage changed get stand-ins again.
	of := sectionsOfClips(d, s)
	taken := map[string]bool{}
	for _, cl := range s.Clips {
		taken[cl.ID] = true
	}
	for _, sec := range d.Sections {
		if sec.Kind != models.RecipeChosen || !in.Changed[sec.From] {
			continue
		}
		var keep []editspec.Clip
		first := -1
		for i, cl := range s.Clips {
			if of[cl.ID] == sec.Name {
				if first < 0 {
					first = len(keep)
				}
				delete(taken, cl.ID)
				continue
			}
			keep = append(keep, s.Clips[i])
		}
		ins, recs := chosenClips(sec, in, taken)
		if first < 0 {
			first = insertPoint(d, keep, sec.Name)
		}
		merged := append([]editspec.Clip{}, keep[:first]...)
		merged = append(merged, ins...)
		merged = append(merged, keep[first:]...)
		s.Clips = merged
		out.StandIns = append(out.StandIns, recs...)
		// Graphics and lines that followed the old moments follow the stand-ins.
		have := map[string]bool{}
		for _, cl := range s.Clips {
			have[cl.ID] = true
		}
		for i := range s.Overlays {
			o := &s.Overlays[i]
			if o.Span == nil || len(ins) == 0 {
				continue
			}
			if o.Span.From != "" && !have[o.Span.From] {
				o.Span.From = ins[0].ID
			}
			if o.Span.To != "" && !have[o.Span.To] {
				o.Span.To = ins[len(ins)-1].ID
			}
		}
		if s.Voiceover != nil && len(ins) > 0 {
			for i := range s.Voiceover.Beats {
				a := &s.Voiceover.Beats[i].Anchor
				if a.ClipID != "" && !have[a.ClipID] {
					a.Kind, a.ClipID, a.AssetID, a.At, a.Quote, a.Ref = editspec.AnchorClip, ins[0].ID, "", nil, "", ""
				}
			}
		}
	}
	bindParts(in, s, out, true)
	pruneFixed(s)
	out.Spec = s
	return out
}

// bindClip gives a clip the footage its input names, or the clip the run made.
func bindClip(in assemblyIn, cl *editspec.Clip, out *assembly) {
	if m, ok := in.Made[cl.ID]; ok {
		cl.AssetID = m.AssetID
		switch {
		case m.Whole || cl.In >= m.Duration:
			cl.In, cl.Out = 0, round3(m.Duration)
		case cl.Out > m.Duration:
			cl.Out = round3(m.Duration)
		}
		return
	}
	for _, b := range in.Doc.Bindings {
		p, ok := ParseBinding(b.To)
		if !ok || p.Kind != models.BindClip || p.Where != cl.ID {
			continue
		}
		v := in.Values[b.Input]
		if v == nil || (!in.Changed[b.Input] && in.Base != nil) {
			continue
		}
		k := 0
		for _, other := range bindingsOf(in.Doc, b.Input) {
			if other.Kind == models.BindClip && other.Where == cl.ID {
				break
			}
			if other.Kind == models.BindClip {
				k++
			}
		}
		if len(v.Items) == 0 {
			continue
		}
		item := v.Items[k%len(v.Items)]
		if item.ID == cl.AssetID {
			continue
		}
		length := cl.Out - cl.In
		cl.AssetID, cl.In = item.ID, 0
		cl.Out = round3(math.Min(length, item.Duration))
		if cl.Out <= 0 {
			cl.Out = round3(item.Duration)
		}
		cl.Shots, cl.MutedMembers = nil, nil
	}
}

// pictureSlots gives each pictures binding its place among its input's
// pictures, in the order of the bindings: a graphic's picture (resource:) and
// a Remotion clip's picture setting (pictures:) alike take the input's next
// picture.
func pictureSlots(d *models.RecipeDoc) map[int]int {
	out := map[int]int{}
	next := map[string]int{}
	for i, b := range d.Bindings {
		p, ok := ParseBinding(b.To)
		if !ok || (p.Kind != models.BindResource && p.Kind != models.BindPictures) {
			continue
		}
		out[i] = next[b.Input]
		next[b.Input]++
	}
	return out
}

// bindParts puts words, pictures and the voice where their inputs go
// (pictures of clips made from code are drawn by the run's code step).
func bindParts(in assemblyIn, s *editspec.Spec, out *assembly, onlyChanged bool) {
	d := in.Doc
	slots := pictureSlots(d)
	for i, b := range d.Bindings {
		p, ok := ParseBinding(b.To)
		v := in.Values[b.Input]
		if !ok || v == nil {
			continue
		}
		if onlyChanged && !in.Changed[b.Input] {
			continue
		}
		switch p.Kind {
		case models.BindParam:
			o := findOverlay(s, p.Where)
			if o == nil {
				out.Notes = append(out.Notes, fmt.Sprintf("%s isn't in the cut any more, so its %s stayed as it was.", upperFirst(graphicWords(overlayByID(d, p.Where))), fieldName(p.Field)))
				continue
			}
			o.Params = setParam(o.Params, p.Field, v.Text)
		case models.BindResource:
			k := slots[i]
			if len(v.Items) == 0 {
				continue
			}
			item := v.Items[k%len(v.Items)]
			if item.ID == "" || item.SHA256 == "" {
				continue
			}
			s.Resources[p.Where] = editspec.ResourcePin{ID: item.ID, SHA256: item.SHA256}
		case models.BindTitle:
			s.Title = shorten(v.Text, 200)
		case models.BindLine:
			if s.Voiceover == nil {
				continue
			}
			for i := range s.Voiceover.Beats {
				bt := &s.Voiceover.Beats[i]
				if bt.ID == p.Where && strings.TrimSpace(bt.Text) != strings.TrimSpace(v.Text) && strings.TrimSpace(v.Text) != "" {
					bt.Text = strings.TrimSpace(v.Text)
					bt.Take, bt.State = nil, "draft"
				}
			}
		case models.BindVoice:
			if s.Voiceover == nil || in.Voice == nil {
				continue
			}
			for i := range s.Voiceover.Beats {
				bt := &s.Voiceover.Beats[i]
				own := ""
				if bt.Voice != nil {
					own = bt.Voice.VoiceID
				}
				if own != "" && own != in.DefaultVoice && own != in.Voice.VoiceID {
					continue // a line with a voice of its own keeps it (D193)
				}
				if own == in.Voice.VoiceID || (own == "" && in.Voice.VoiceID == in.DefaultVoice) {
					continue
				}
				vr := *in.Voice
				if bt.Voice != nil {
					vr.ModelID, vr.Settings = bt.Voice.ModelID, bt.Voice.Settings
				}
				bt.Voice = &vr
				bt.Take, bt.State = nil, "draft"
			}
		}
	}
}

// standIns are the clips a chosen section starts with for Claude to
// replace: its smallest count of moments, spread over its footage, as long
// as the section should be in all.
func standIns(sec models.RecipeSection, v *models.RecipeRunInput, run string, taken map[string]bool) []editspec.Clip {
	if v == nil || len(v.Items) == 0 {
		return nil
	}
	n := len(sec.Clips)
	if sec.Count != nil && sec.Count.Min > 0 {
		n = int(math.Ceil(sec.Count.Min))
	}
	if n < 1 {
		n = 1
	}
	total := sectionSeconds(sec)
	if total <= 0 {
		total = 4 * float64(n)
	}
	each := math.Max(1, total/float64(n))
	perItem := map[int]int{}
	for i := 0; i < n; i++ {
		perItem[i%len(v.Items)]++
	}
	seen := map[int]int{}
	var out []editspec.Clip
	for i := 0; i < n; i++ {
		k := i % len(v.Items)
		item := v.Items[k]
		j := seen[k]
		seen[k]++
		m := perItem[k]
		dur := item.Duration
		length := math.Min(each, dur)
		start := dur*float64(j+1)/float64(m+1) - length/2
		start = math.Max(0, math.Min(start, dur-length))
		id := fmt.Sprintf("%s-%d", sec.Name, i+1)
		for x := 2; taken[id]; x++ {
			id = fmt.Sprintf("%s-%d-%d", sec.Name, i+1, x)
		}
		taken[id] = true
		purpose := strings.TrimSpace(sec.Purpose)
		if purpose == "" {
			purpose = "a moment for this part of the video"
		}
		note := fmt.Sprintf("A stand-in: MetaRush put this clip here for Claude to replace with a moment that fits the section “%s” (%s) — %s. Made for %s.",
			sec.Name, sectionNeeds(sec), purpose, run)
		out = append(out, editspec.Clip{ID: id, AssetID: item.ID, In: round3(start), Out: round3(start + length), Label: shorten("To choose: "+purpose, 120), Notes: shorten(note, 4000)})
	}
	return out
}

// sectionNeeds says what a chosen section wants ("3–5 moments, 20–30 s in all").
func sectionNeeds(sec models.RecipeSection) string {
	var parts []string
	if c := sec.Count; c != nil && c.Max > 0 {
		if c.Min > 0 && c.Min != c.Max {
			parts = append(parts, fmt.Sprintf("%g–%g moments", c.Min, c.Max))
		} else {
			parts = append(parts, plural(int(c.Max), "moment"))
		}
	} else if len(sec.Clips) > 0 {
		parts = append(parts, "about "+plural(len(sec.Clips), "moment"))
	}
	if l := sec.Length; l != nil && l.Max > 0 {
		if l.Min > 0 && l.Min != l.Max {
			parts = append(parts, secondsRange(l.Min, l.Max)+" in all")
		} else {
			parts = append(parts, seconds(l.Max)+" in all")
		}
	}
	if len(parts) == 0 {
		return "any number of moments"
	}
	return strings.Join(parts, ", ")
}

// sectionsOfClips says which of the recipe's sections each clip of a cut is
// in: a fixed section's clips by name; the others belong to the chosen
// section between the fixed ones around them (by name when several are).
func sectionsOfClips(d *models.RecipeDoc, s *editspec.Spec) map[string]string {
	fixed := map[string]int{}
	for i, sec := range d.Sections {
		if sec.Kind == models.RecipeFixed {
			for _, cl := range sec.Clips {
				fixed[cl.ID] = i
			}
		}
	}
	out := map[string]string{}
	last := -1
	for _, cl := range s.Clips {
		if i, ok := fixed[cl.ID]; ok {
			out[cl.ID] = d.Sections[i].Name
			last = i
			continue
		}
		// The chosen sections after the last fixed one seen, before the next fixed one.
		var cands []string
		for i := last + 1; i < len(d.Sections); i++ {
			if d.Sections[i].Kind == models.RecipeFixed {
				break
			}
			cands = append(cands, d.Sections[i].Name)
		}
		switch {
		case len(cands) == 0:
			continue
		case len(cands) == 1:
			out[cl.ID] = cands[0]
		default:
			out[cl.ID] = cands[0]
			for _, c := range cands {
				if strings.HasPrefix(cl.ID, c+"-") {
					out[cl.ID] = c
				}
			}
		}
	}
	return out
}

// insertPoint is where a chosen section's clips go when the cut has none of
// them: after the clips of the fixed section before it.
func insertPoint(d *models.RecipeDoc, clips []editspec.Clip, section string) int {
	before := map[string]bool{}
	for _, sec := range d.Sections {
		if sec.Name == section {
			break
		}
		for _, cl := range sec.Clips {
			before[cl.ID] = true
		}
	}
	at := 0
	for i, cl := range clips {
		if before[cl.ID] {
			at = i + 1
		}
	}
	return at
}

// remapBeats points narration lines that followed a chosen section's old
// moments at the stand-ins in their place.
func remapBeats(s *editspec.Spec, d *models.RecipeDoc, remap map[string]string, remapped map[string]bool) {
	if s.Voiceover == nil || len(remap) == 0 {
		return
	}
	for i := range s.Voiceover.Beats {
		a := &s.Voiceover.Beats[i].Anchor
		switch a.Kind {
		case editspec.AnchorClip:
			if n, ok := remap[a.ClipID]; ok {
				a.ClipID = n
			}
		case editspec.AnchorMoment:
			if a.ClipID != "" && remapped[a.ClipID] {
				a.Kind, a.ClipID, a.AssetID, a.At, a.Quote, a.Ref, a.Offset = editspec.AnchorClip, remap[a.ClipID], "", nil, "", "", 0
				continue
			}
			for _, sec := range d.Sections {
				for _, cl := range sec.Clips {
					if remapped[cl.ID] && cl.AssetID == a.AssetID && a.At != nil && *a.At >= cl.In && *a.At < cl.Out {
						a.Kind, a.ClipID, a.AssetID, a.At, a.Quote, a.Ref, a.Offset = editspec.AnchorClip, remap[cl.ID], "", nil, "", "", 0
					}
				}
			}
		}
	}
}

// pruneFixed drops fixed-copy pins and clearer copies of footage the cut no
// longer plays.
func pruneFixed(s *editspec.Spec) {
	used := map[string]bool{}
	for _, cl := range s.Clips {
		used[cl.AssetID] = true
	}
	for _, t := range s.Audio {
		used[t.AssetID] = true
	}
	if v := s.Voiceover; v != nil {
		for _, b := range v.Beats {
			if b.Take != nil {
				used[b.Take.AssetID] = true
			}
		}
		if v.Continuous != nil {
			used[v.Continuous.AssetID] = true
		}
	}
	for _, c := range s.Conversations {
		for _, m := range c.Members {
			used[m.AssetID] = true
		}
	}
	if s.Mix != nil && len(s.Mix.Enhanced) > 0 {
		var keep []editspec.EnhancedSound
		for _, e := range s.Mix.Enhanced {
			if used[e.AssetID] {
				keep = append(keep, e)
			}
		}
		s.Mix.Enhanced = keep
	}
	if len(s.Fixed) == 0 {
		s.Fixed = nil
		if len(s.Resources) == 0 {
			s.Resources = nil
		}
		if len(s.Conversations) == 0 {
			s.Conversations = nil
		}
		return
	}
	for id := range s.Fixed {
		if !used[id] {
			delete(s.Fixed, id)
		}
	}
	if len(s.Fixed) == 0 {
		s.Fixed = nil
	}
	if len(s.Resources) == 0 {
		s.Resources = nil
	}
	if len(s.Conversations) == 0 {
		s.Conversations = nil
	}
}

// findOverlay finds a graphic of a cut.
func findOverlay(s *editspec.Spec, id string) *editspec.Overlay {
	for i := range s.Overlays {
		if s.Overlays[i].ID == id {
			return &s.Overlays[i]
		}
	}
	return nil
}

// setParam sets one field of a graphic's settings.
func setParam(params json.RawMessage, field, text string) json.RawMessage {
	m := map[string]any{}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &m)
	}
	m[field] = text
	b, _ := json.Marshal(m)
	return b
}

// paramText is one field of a graphic's settings as words ("" none).
func paramText(params json.RawMessage, field string) string {
	m := map[string]any{}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &m)
	}
	s, _ := m[field].(string)
	return s
}

func fieldName(f string) string {
	if w := fieldWords[f]; w != "" {
		return w
	}
	return f
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func cloneClip(c editspec.Clip) editspec.Clip {
	var out editspec.Clip
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, &out)
	return out
}

func cloneJSON[T any](v T) T {
	var out T
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	return out
}

func cloneAudio(xs []editspec.AudioTrack) []editspec.AudioTrack {
	if xs == nil {
		return nil
	}
	return cloneJSON(xs)
}

func cloneOverlays(xs []editspec.Overlay) []editspec.Overlay {
	if xs == nil {
		return nil
	}
	return cloneJSON(xs)
}

func cloneCaptions(c *editspec.Captions) *editspec.Captions {
	if c == nil {
		return nil
	}
	return cloneJSON(c)
}

func cloneBrand(b *editspec.Brand) *editspec.Brand {
	if b == nil {
		return nil
	}
	return cloneJSON(b)
}

func cloneVoiceover(v *editspec.Voiceover) *editspec.Voiceover {
	if v == nil {
		return nil
	}
	return cloneJSON(v)
}

func cloneMix(m *editspec.Mix) *editspec.Mix {
	if m == nil {
		return nil
	}
	return cloneJSON(m)
}
