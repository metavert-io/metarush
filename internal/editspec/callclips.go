package editspec

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Call clean-up for nested cuts and conversations (D182). A clip's footage
// switches are on the clip's own timeline: a footage's seconds for plain
// footage, the inner cut's timeline for a nested cut, the conversation's clock
// for a conversation clip. A nested cut switches at each hard cut between its
// clips and inside its call footage; a conversation clip at each of its shots
// and inside a speaker-view recording while it fills the frame. Who is on
// screen comes from the footage, or for a conversation from the member shown.

// clipFootage is what MetaRush knows about the picture switches of the
// cut's clip i (ok false when nothing). facts are wrapped (wrapFacts).
func clipFootage(s *Spec, i int, facts Facts) (*FootageShots, bool) {
	if i < 0 || i >= len(s.Clips) {
		return nil, false
	}
	c := s.Clips[i]
	if IsConversation(c.AssetID) {
		return convClipFootage(s, c, facts)
	}
	return switchFacts(facts, c.AssetID)
}

// footage is a nested cut's switches, shots and printed names on its own
// timeline (computed once). A nested cut with a name bar of its own has no
// printed names here: its own bar covers them and the outer one stays hidden.
func (nc *nestedCut) footage() (*FootageShots, bool) {
	if nc == nil || nc.spec == nil || nc.plan == nil {
		return nil, false
	}
	if nc.shots != nil {
		return nc.shots, len(nc.shots.Switches) > 0 || nc.shots.Names != nil
	}
	s, plan := nc.spec, nc.plan
	fps := specFPS(s)
	out := &FootageShots{Switches: []PictureSwitch{}, Shots: []FootageShot{}}
	ownBar := hasNameBar(s)
	corners := map[string]int{}
	var box *CoverBox
	for i, c := range s.Clips {
		if i >= len(plan.Clips) {
			break
		}
		pc := plan.Clips[i]
		start := float64(pc.From) / fps
		end := start + float64(pc.DurationInFrames)/fps
		rate := speedOf(c)
		if i > 0 && (c.Transition == nil || c.Transition.Type == TransitionCut || c.Transition.Duration <= 0) {
			out.Switches = append(out.Switches, PictureSwitch{At: round3(start), Last: round3(start - 1/fps)})
		}
		fs, ok := clipFootage(s, i, nc.facts)
		at := func(t float64) float64 { return start + (t-c.In)/rate }
		bounds := []float64{start}
		if ok && fs != nil {
			out.Precision = math.Max(out.Precision, fs.Precision/rate)
			for _, sw := range fs.Switches {
				if sw.At > c.In+1e-3 && sw.At < c.Out-1e-3 {
					out.Switches = append(out.Switches, PictureSwitch{At: round3(at(sw.At)), Last: round3(at(sw.Last))})
					bounds = append(bounds, at(sw.At))
				}
			}
		}
		bounds = append(bounds, end)
		for k := 0; k+1 < len(bounds); k++ {
			sh := FootageShot{From: round3(bounds[k]), To: round3(bounds[k+1])}
			if ok && fs != nil {
				mid := c.In + ((bounds[k]+bounds[k+1])/2-start)*rate
				if fsh := fs.ShotAt(mid); fsh != nil {
					sh.Person, sh.By, sh.Printed, sh.member = fsh.Person, fsh.By, fsh.Printed, fsh.member
				}
				if !ownBar && fs.Names != nil {
					b := fs.Names.Box
					if fsh := fs.ShotAt(mid); fsh != nil && fsh.Cover != nil {
						b = *fsh.Cover
					}
					// Where the inner clip shows it, in the nested cut's picture.
					if cv, in := coverIn(pc, s.Output, b); in {
						sh.Cover = &cv
						if box == nil {
							box = &cv
						}
						corners[fs.Names.Corner]++
					}
				}
			}
			sh.noNames = sh.Cover == nil
			out.Shots = append(out.Shots, sh)
		}
	}
	sort.SliceStable(out.Switches, func(a, b int) bool { return out.Switches[a].At < out.Switches[b].At })
	if box != nil {
		out.Names = &PrintedNames{Corner: topCorner(corners), Box: *box, Shots: sum(corners)}
	}
	nc.shots = out
	return out, len(out.Switches) > 0 || out.Names != nil
}

// convClipFootage is a conversation clip's switches and shots on the
// conversation's clock: each shot it cuts to, and while a recording with
// switches of its own (a call app's speaker view) fills the frame, those
// switches mapped onto the clock. A member shown alone is who is on screen
// (by "member"), unless their recording's own shot says who it shows.
func convClipFootage(s *Spec, c Clip, facts Facts) (*FootageShots, bool) {
	pin, ok := s.Conversations[ConvName(c.AssetID)]
	if !ok {
		return nil, false
	}
	shots := ClipShots(s, c)
	if len(shots) == 0 {
		return nil, false
	}
	fps := specFPS(s)
	out := &FootageShots{Switches: []PictureSwitch{}, Shots: []FootageShot{}}
	corners := map[string]int{}
	var box *CoverBox
	for k, sh := range shots {
		a, b := math.Max(sh.At, c.In), c.Out
		if k+1 < len(shots) {
			b = math.Min(b, shots[k+1].At)
		}
		if b <= a {
			continue
		}
		if sh.At > c.In+1e-3 && sh.At < c.Out-1e-3 && sh.Dissolve <= 0 {
			out.Switches = append(out.Switches, PictureSwitch{At: round3(sh.At), Last: round3(sh.At - 1/fps)})
		}
		if sh.Kind() != "member" {
			out.Shots = append(out.Shots, FootageShot{From: round3(a), To: round3(b)}) // several people, a card or other footage
			continue
		}
		m, ok := pin.Member(sh.Member)
		if !ok || !m.HasPicture() {
			out.Shots = append(out.Shots, FootageShot{From: round3(a), To: round3(b)})
			continue
		}
		fs, has := switchFacts(facts, m.AssetID)
		bounds := []float64{a}
		if has && fs != nil {
			out.Precision = math.Max(out.Precision, fs.Precision/m.Rate())
			for _, sw := range fs.Switches {
				if t := m.ClockAt(sw.At); t > a+1e-3 && t < b-1e-3 {
					out.Switches = append(out.Switches, PictureSwitch{At: round3(t), Last: round3(m.ClockAt(sw.Last))})
					bounds = append(bounds, t)
				}
			}
			if fs.Names != nil {
				corners[fs.Names.Corner]++
				if box == nil {
					bx := fs.Names.Box
					box = &bx
				}
			}
		}
		bounds = append(bounds, b)
		for j := 0; j+1 < len(bounds); j++ {
			fsh := FootageShot{From: round3(bounds[j]), To: round3(bounds[j+1]), Person: m.Name, By: "member", member: m.ID}
			if has && fs != nil {
				if inner := fs.ShotAt(m.SourceAt((bounds[j] + bounds[j+1]) / 2)); inner != nil {
					fsh.Printed = inner.Printed
					if inner.Person != "" && len(fs.Switches) > 0 {
						fsh.Person, fsh.By = inner.Person, inner.By // a speaker view shows whoever it switched to
					}
				}
			}
			out.Shots = append(out.Shots, fsh)
		}
	}
	sort.SliceStable(out.Switches, func(a, b int) bool { return out.Switches[a].At < out.Switches[b].At })
	if box != nil {
		out.Names = &PrintedNames{Corner: topCorner(corners), Box: *box, Shots: sum(corners)}
	}
	return out, len(out.Switches) > 0 || out.Names != nil
}

// convShotCover is where the printed names of the recording a conversation
// clip shows alone at clock time t sit in the output picture: the member's
// recording's names area drawn through its tile. ok is false in a layout, a
// card or other footage, or when that recording has no printed names.
func convShotCover(pc PlanClip, c Clip, t float64, facts Facts, out Output, fps float64) (CoverBox, bool) {
	if pc.Conversation == nil {
		return CoverBox{}, false
	}
	f := int(math.Round((math.Max(t, c.In) - c.In) * fps))
	for _, ps := range pc.Conversation.Shots {
		if f < ps.From || f >= ps.From+ps.DurationInFrames {
			continue
		}
		if ps.Kind != "member" || len(ps.Tiles) != 1 {
			return CoverBox{}, false
		}
		fs, ok := switchFacts(facts, ps.Tiles[0].AssetID)
		if !ok || fs == nil || fs.Names == nil {
			return CoverBox{}, false
		}
		r, in := tileArea(ps.Tiles[0], fs.Names.Box)
		if !in || out.Width <= 0 || out.Height <= 0 {
			return CoverBox{}, false
		}
		W, H := float64(out.Width), float64(out.Height)
		return CoverBox{X: round3(r.X / W), Y: round3(r.Y / H), W: round3(r.W / W), H: round3(r.H / H)}, true
	}
	return CoverBox{}, false
}

// tileArea is where an area of a tile's recording (fractions of its picture)
// is drawn, in output pixels, clipped to what the tile shows (D182).
func tileArea(t PlanTile, b CoverBox) (PlanRect, bool) {
	x0 := t.Rect.X + t.Video.X + b.X*t.Video.W
	y0 := t.Rect.Y + t.Video.Y + b.Y*t.Video.H
	x1 := t.Rect.X + t.Video.X + (b.X+b.W)*t.Video.W
	y1 := t.Rect.Y + t.Video.Y + (b.Y+b.H)*t.Video.H
	vx0, vy0, vx1, vy1 := t.Rect.X, t.Rect.Y, t.Rect.X+t.Rect.W, t.Rect.Y+t.Rect.H
	if w := t.Window; w != nil {
		vx0, vy0 = math.Max(vx0, t.Rect.X+w.X), math.Max(vy0, t.Rect.Y+w.Y)
		vx1, vy1 = math.Min(vx1, t.Rect.X+w.X+w.W), math.Min(vy1, t.Rect.Y+w.Y+w.H)
	}
	x0, y0 = math.Max(x0, vx0), math.Max(y0, vy0)
	x1, y1 = math.Min(x1, vx1), math.Min(y1, vy1)
	if x1-x0 < 1 || y1-y0 < 1 {
		return PlanRect{}, false
	}
	return PlanRect{X: math.Round(x0), Y: math.Round(y0), W: math.Round(x1 - x0), H: math.Round(y1 - y0)}, true
}

func topCorner(corners map[string]int) string {
	best := ""
	for k, v := range corners {
		if best == "" || v > corners[best] || (v == corners[best] && k < best) {
			best = k
		}
	}
	return best
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// HasCallFootage reports whether any clip of the cut shows footage whose
// picture switches or shows printed names: plain call footage, a nested cut or
// a conversation (D182).
func HasCallFootage(s *Spec, facts Facts) bool {
	if facts == nil {
		return false
	}
	s = Clone(s)
	Normalize(s)
	facts = wrapFacts(s, facts)
	for i := range s.Clips {
		if fs, ok := clipFootage(s, i, facts); ok && fs != nil && (len(fs.Switches) > 0 || fs.Names != nil) {
			return true
		}
	}
	return false
}

// PrintedNameClips counts the cut's clips whose picture shows names a call
// app printed, and the corner most of them use (D162, D182).
func PrintedNameClips(s *Spec, facts Facts) (int, string) {
	if facts == nil {
		return 0, ""
	}
	s = Clone(s)
	Normalize(s)
	return printedNameClips(s, wrapFacts(s, facts))
}

// firstNamesOf reports a name bar written with first names only.
func firstNamesOf(o Overlay) bool { return strings.Contains(string(o.Params), `"firstNames":true`) }

// reframeNameBars moves a cut's name bars to where its printed names are at
// its current shape (D182): a nested cut played at another shape crops its
// clips differently, so the area each bar covers is worked out again. The
// names stay as written; only "cover" changes.
func reframeNameBars(s *Spec, facts Facts) {
	key := func(ch OverlayChange) string { return ch.Footage + "@" + strconv.FormatFloat(ch.At, 'f', 3, 64) }
	for i := range s.Overlays {
		o := &s.Overlays[i]
		if o.Template != TplNameBar {
			continue
		}
		nb, ok := BuildNameBar(s, facts, NameBarOptions{ID: o.ID, FirstNames: firstNamesOf(*o), KeepTitles: true})
		if !ok {
			continue
		}
		var fresh, base map[string]any
		_ = json.Unmarshal(nb.Overlay.Params, &fresh)
		_ = json.Unmarshal(o.Params, &base)
		if base == nil {
			base = map[string]any{}
		}
		if c, ok := fresh["cover"]; ok {
			base["cover"] = c
		} else {
			delete(base, "cover") // its names are cropped away at this shape
		}
		o.Params, _ = json.Marshal(base)
		covers := map[string]any{}
		for _, ch := range nb.Overlay.Changes {
			var f map[string]any
			_ = json.Unmarshal(ch.Params, &f)
			covers[key(ch)] = f["cover"]
		}
		for j := range o.Changes {
			ch := &o.Changes[j]
			c, known := covers[key(*ch)]
			if !known {
				continue
			}
			var f map[string]any
			if json.Unmarshal(ch.Params, &f) != nil || f == nil {
				continue
			}
			if c == nil {
				delete(f, "cover")
			} else {
				f["cover"] = c
			}
			ch.Params, _ = json.Marshal(f)
		}
	}
}

// nameBarOf is the cut's name bar (nil when it has none).
func nameBarOf(s *Spec) *Overlay {
	for i := range s.Overlays {
		if s.Overlays[i].Template == TplNameBar {
			return &s.Overlays[i]
		}
	}
	return nil
}

// tileTagCover is where a layout tile's recording shows a printed name, in
// pixels of the tile, for its own name tag to cover when the cut has a name
// bar (D182: one bar can't cover several tiles).
func tileTagCover(s *Spec, facts Facts, t PlanTile) *PlanRect {
	if nameBarOf(s) == nil {
		return nil
	}
	fs, ok := switchFacts(facts, t.AssetID)
	if !ok || fs == nil || fs.Names == nil {
		return nil
	}
	r, in := tileArea(t, fs.Names.Box)
	if !in {
		return nil
	}
	return &PlanRect{X: r.X - t.Rect.X, Y: r.Y - t.Rect.Y, W: r.W, H: r.H}
}
