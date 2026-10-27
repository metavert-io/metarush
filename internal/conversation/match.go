package conversation

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/visual"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// Webcam matching (D103): every webcam looks different — exposure, white
// balance, contrast. Each member's picture is measured where their face is
// (the visual index; the middle of the picture without one) on frames across
// the recording, and a look (exposure, contrast, saturation, warmth, tint:
// one colour matrix the renderer applies) is fitted so it matches the
// reference: brightness and contrast fully, the colour half way (people's
// skin differs; a blue cast on a face is the camera). The difference is
// measured before and after (CIE76 ΔE and L* on the face).

// ActionMatch names tracked webcam matching.
const ActionMatch = "conversation-match"

func init() {
	voiceover.ActionTitles[ActionMatch] = "Matching the webcams"
	ops.Def("conversation.match", "Match the webcams of a conversation: measure each person's picture where their face is and fit a colour and brightness correction to the reference, measured before and after (checks the change to the cut; apply makes it a new version)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallMatchIn) (*CallMatchOut, error) {
			return match(ctx, a, in)
		})
}

// CallMatchIn matches a conversation's webcams.
type CallMatchIn struct {
	CallRefIn
	// Reference is the member whose picture the others match (default: the
	// one the recordings were lined up with, if it has a picture).
	Reference string `json:"reference,omitempty"`
	// Off takes the corrections away again.
	Off     bool   `json:"off,omitempty"`
	Project string `json:"project,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$,description=The project whose cut gets the corrections (default: the conversation's)."`
	Branch  string `json:"branch,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]*$"`
	Apply   bool   `json:"apply,omitempty"`
}

// CallMatchOut is the measurement and the change to the cut.
type CallMatchOut struct {
	CallPlanOut
	Conversation CallInfo          `json:"conversation"`
	Members      []CallMatchMember `json:"members"`
	JobID        string            `json:"jobId,omitempty"`
}

// SetJobID implements voiceover.Trackable.
func (o *CallMatchOut) SetJobID(id string) { o.JobID = id }

// JobResult implements voiceover.Trackable.
func (o *CallMatchOut) JobResult() map[string]any {
	return map[string]any{"conversationId": o.Conversation.ID, "summary": o.Summary}
}

// CallMatchMember is one webcam's measurement.
type CallMatchMember struct {
	Member    string `json:"member"`
	Name      string `json:"name"`
	Reference bool   `json:"reference,omitempty"`
	// Before and After are the colour difference to the reference's face (ΔE,
	// CIE76: about 2 is barely visible, over 10 obvious); Light the brightness
	// difference (L*).
	Before      float64        `json:"before"`
	After       float64        `json:"after"`
	LightBefore float64        `json:"lightBefore"`
	LightAfter  float64        `json:"lightAfter"`
	Look        *editspec.Look `json:"look,omitempty"`
	Faces       bool           `json:"faces" jsonschema:"description=Measured on the face (else the middle of the picture)."`
	Frames      int            `json:"frames"`
	Text        string         `json:"text"`
}

func match(ctx context.Context, a *app.App, in *CallMatchIn) (*CallMatchOut, error) {
	c, err := load(ctx, a, in.Conversation, in.ProjectID)
	if err != nil {
		return nil, err
	}
	run := func(ctx context.Context) (*CallMatchOut, error) {
		out := &CallMatchOut{Members: []CallMatchMember{}}
		if in.Off {
			for i := range c.Members {
				c.Members[i].Look = nil
				if c.Members[i].Color != nil {
					c.Members[i].Color.Look = nil
				}
			}
			out.Summary = "The webcams play as recorded again."
		} else if err := measureAndFit(ctx, a, c, in.Reference, out); err != nil {
			return nil, err
		}
		if err := save(ctx, a, c); err != nil {
			return nil, err
		}
		a.Audit(ctx, "conversation.match", "conversation", c.ID, first(c.ProjectIDs), map[string]any{"off": in.Off})
		out.Conversation = view(ctx, a, c)
		// The cut's pins get the looks.
		pid := in.Project
		if pid == "" {
			pid = first(c.ProjectIDs)
		}
		rev, err := latest(ctx, a, pid, in.Branch)
		if err != nil {
			return nil, err
		}
		out.Changes = []string{}
		if rev == nil {
			return out, nil
		}
		var eops []editspec.Op
		for _, name := range sortedNames(rev.Spec.Conversations) {
			pin := rev.Spec.Conversations[name]
			if pin.ID != c.ID {
				continue
			}
			changed := false
			for k := range pin.Members {
				var look *editspec.Look
				for _, m := range c.Members {
					if m.AssetID == pin.Members[k].AssetID {
						look = m.Look
					}
				}
				if string(mustJSON(look)) != string(mustJSON(pin.Members[k].Look)) {
					pin.Members[k].Look = look
					changed = true
				}
			}
			if changed {
				p := pin
				eops = append(eops, editspec.Op{Op: "setConversation", ID: name, Conversation: &p})
			}
		}
		if len(eops) == 0 {
			return out, nil
		}
		what := "match the webcams"
		if in.Off {
			what = "play the webcams as recorded"
		}
		out.Changes = append(out.Changes, what)
		if out.Result, err = planCheck(ctx, a, rev, strings.ToUpper(what[:1])+what[1:]+" in "+c.Name, eops); err != nil {
			return nil, err
		}
		if in.Apply {
			out.Applied = applyResult(ctx, a, out.Result)
			out.Summary = settle(out.Summary, out.Applied)
		} else {
			out.Summary += " Apply it to put it in the cut."
		}
		return out, nil
	}
	return voiceover.TrackWork(ctx, a, ActionMatch, "", first(c.ProjectIDs), run)
}

// measureAndFit measures every picture member and fits each one's look to
// the reference (in place on the record).
func measureAndFit(ctx context.Context, a *app.App, c *models.Conversation, refName string, out *CallMatchOut) error {
	as, err := assets(ctx, a, c)
	if err != nil {
		return err
	}
	var pics []int
	for i, m := range c.Members {
		if x := as[m.AssetID]; m.HasPicture() && x != nil && x.Media.HasVideo {
			pics = append(pics, i)
		}
	}
	if len(pics) < 2 {
		return invalid("matching needs at least two webcams; %s has %d", c.Name, len(pics))
	}
	ref := -1
	switch {
	case refName != "":
		m, err := findMember(c, refName)
		if err != nil {
			return err
		}
		_, ref = c.Member(m.ID)
		if !containsInt(pics, ref) {
			return invalid("%s has no picture to match the others to", m.Name)
		}
	default:
		ref = pics[0]
		if c.Sync != nil {
			if _, i := c.Member(c.Sync.Reference); i >= 0 && containsInt(pics, i) && c.Members[i].UseOf() != editspec.UsePicture {
				ref = i
			}
		}
		// A shared gallery view is never the reference when a person's webcam is there.
		if c.Members[ref].UseOf() == editspec.UsePicture {
			for _, i := range pics {
				if c.Members[i].UseOf() != editspec.UsePicture {
					ref = i
					break
				}
			}
		}
	}
	var idList []string
	for _, i := range pics {
		idList = append(idList, c.Members[i].AssetID)
	}
	faces := visual.LoadFaces(ctx, a.DB, a.WorkspaceID(), idList)
	samples := map[int]*pictureSample{}
	for k, i := range pics {
		m := c.Members[i]
		_ = voiceover.Step(ctx, k, len(pics)+1, "Looking at "+possessive(m.Name)+" webcam")
		path, err := local(ctx, a, as[m.AssetID])
		if err != nil {
			return err
		}
		ps, err := samplePicture(ctx, a.Tools, path, as[m.AssetID].Media.Duration, faces[m.AssetID])
		if err != nil {
			return fmt.Errorf("%s picture: %w", possessive(m.Name), err)
		}
		samples[i] = ps
	}
	_ = voiceover.Step(ctx, len(pics), len(pics)+1, "Fitting the corrections")
	rs := samples[ref]
	now := time.Now().UTC()
	var worst float64
	for _, i := range pics {
		m := &c.Members[i]
		ps := samples[i]
		col := &models.MemberColor{Mean: ps.meanRGB(nil), Spread: ps.spreadRGB(), Frames: ps.frames, Faces: ps.faces, At: now}
		mm := CallMatchMember{Member: m.ID, Name: m.Name, Faces: ps.faces, Frames: ps.frames}
		before := ps.stats(nil)
		ref0 := rs.stats(nil)
		col.DeltaEBefore, col.LightBefore = round1(deltaE(before.lab, ref0.lab)), round1(math.Abs(before.lab[0]-ref0.lab[0]))
		if i == ref {
			mm.Reference = true
			m.Look = nil
			col.DeltaEAfter, col.LightAfter = 0, 0
			mm.Text = "the reference: the others are matched to it"
		} else {
			look := fitLook(ps, ref0)
			after := ps.stats(look)
			col.Look, m.Look = look, look
			col.DeltaEAfter, col.LightAfter = round1(deltaE(after.lab, ref0.lab)), round1(math.Abs(after.lab[0]-ref0.lab[0]))
			mm.Look = look
			mm.Text = fmt.Sprintf("colour difference %s → %s, brightness %s → %s", dE(col.DeltaEBefore), dE(col.DeltaEAfter), dE(col.LightBefore), dE(col.LightAfter))
			if look == nil {
				mm.Text = "already matches"
			}
			worst = math.Max(worst, col.DeltaEAfter)
		}
		mm.Before, mm.After, mm.LightBefore, mm.LightAfter = col.DeltaEBefore, col.DeltaEAfter, col.LightBefore, col.LightAfter
		m.Color = col
		out.Members = append(out.Members, mm)
	}
	out.Summary = fmt.Sprintf("Matched the webcams to %s.", possessive(c.Members[ref].Name)+" picture")
	if worst > 10 {
		out.Summary += " Some still differ noticeably (the lighting is very different); check them in the preview."
	}
	return nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// dE says a difference in plain numbers ("12" or "1.5").
func dE(v float64) string {
	if v >= 10 {
		return strconv.Itoa(int(math.Round(v)))
	}
	return strconv.FormatFloat(round1(v), 'f', -1, 64)
}

// ---- measuring ----

// pictureSample is the pixels of a member's face region over frames (sRGB 0–1).
type pictureSample struct {
	px     [][3]float64
	frames int
	faces  bool
}

const (
	sampleW, sampleH = 160, 90
	sampleFrames     = 12
)

// samplePicture reads about a dozen frames spread over a recording, small,
// and keeps the pixels where the face is.
func samplePicture(ctx context.Context, ts *tools.Set, path string, dur float64, idx *visual.Index) (*pictureSample, error) {
	every := math.Max(2, dur/sampleFrames)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-i", path, "-map", "0:v:0", "-vf",
		fmt.Sprintf("fps=1/%s,scale=%d:%d:flags=area", strconv.FormatFloat(every, 'f', 3, 64), sampleW, sampleH),
		"-frames:v", strconv.Itoa(sampleFrames + 1), "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1"}
	res, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args})
	if err != nil {
		return nil, err
	}
	frame := sampleW * sampleH * 3
	n := len(res.Stdout) / frame
	if n == 0 {
		return nil, fmt.Errorf("no frames could be read")
	}
	// Where the skin is: the middle of the index's typical face box (cheeks,
	// nose; no hair or background); else the upper middle of the picture,
	// where webcams put people's faces.
	box := [4]float64{0.44, 0.27, 0.12, 0.25}
	ps := &pictureSample{frames: n}
	if idx != nil {
		if b, ok := visual.FaceBox(idx, 0, math.Max(dur, idx.Duration)+1); ok {
			box = [4]float64{b[0] + 0.2*b[2], b[1] + 0.2*b[3], 0.6 * b[2], 0.6 * b[3]}
			ps.faces = true
		}
	}
	x0, y0 := int(math.Max(0, box[0])*sampleW), int(math.Max(0, box[1])*sampleH)
	x1, y1 := int(math.Min(1, box[0]+box[2])*sampleW), int(math.Min(1, box[1]+box[3])*sampleH)
	for f := 0; f < n; f++ {
		b := res.Stdout[f*frame : (f+1)*frame]
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				i := (y*sampleW + x) * 3
				ps.px = append(ps.px, [3]float64{float64(b[i]) / 255, float64(b[i+1]) / 255, float64(b[i+2]) / 255})
			}
		}
	}
	if len(ps.px) == 0 {
		return nil, fmt.Errorf("the face region is empty")
	}
	return ps, nil
}

func (ps *pictureSample) meanRGB(l *editspec.Look) [3]float64 {
	var s [3]float64
	for _, p := range ps.px {
		q := editspec.ApplyLook(l, p)
		for k := range s {
			s[k] += q[k]
		}
	}
	for k := range s {
		s[k] = round3(s[k] / float64(len(ps.px)))
	}
	return s
}

func (ps *pictureSample) spreadRGB() [3]float64 {
	m := ps.meanRGB(nil)
	var s [3]float64
	for _, p := range ps.px {
		for k := range s {
			s[k] += (p[k] - m[k]) * (p[k] - m[k])
		}
	}
	for k := range s {
		s[k] = round3(math.Sqrt(s[k] / float64(len(ps.px))))
	}
	return s
}

// picStats are what matching compares: the mean colour (Lab) and how much the
// brightness varies (L* spread: contrast).
type picStats struct {
	lab    [3]float64
	spread float64
}

// stats measures the pixels through a look, leaving out the darkest and
// brightest tenth (eyes, mouth, highlights, a bit of background).
func (ps *pictureSample) stats(l *editspec.Look) picStats {
	step := max(1, len(ps.px)/4000)
	labs := make([][3]float64, 0, len(ps.px)/step+1)
	for i := 0; i < len(ps.px); i += step {
		labs = append(labs, toLab(editspec.ApplyLook(l, ps.px[i])))
	}
	sort.Slice(labs, func(i, j int) bool { return labs[i][0] < labs[j][0] })
	lo, hi := len(labs)/10, len(labs)-len(labs)/10
	if hi <= lo {
		lo, hi = 0, len(labs)
	}
	var st picStats
	for _, lab := range labs[lo:hi] {
		for k := range st.lab {
			st.lab[k] += lab[k]
		}
	}
	n := float64(hi - lo)
	for k := range st.lab {
		st.lab[k] /= n
	}
	for _, lab := range labs[lo:hi] {
		st.spread += (lab[0] - st.lab[0]) * (lab[0] - st.lab[0])
	}
	st.spread = math.Sqrt(st.spread / n)
	return st
}

// toLab converts sRGB (0–1) to CIE L*a*b* (D65).
func toLab(c [3]float64) [3]float64 {
	lin := func(v float64) float64 {
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	r, g, b := lin(c[0]), lin(c[1]), lin(c[2])
	x := (0.4124*r + 0.3576*g + 0.1805*b) / 0.95047
	y := 0.2126*r + 0.7152*g + 0.0722*b
	z := (0.0193*r + 0.1192*g + 0.9505*b) / 1.08883
	f := func(t float64) float64 {
		if t > 216.0/24389 {
			return math.Cbrt(t)
		}
		return (24389.0/27*t + 16) / 116
	}
	fx, fy, fz := f(x), f(y), f(z)
	return [3]float64{116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)}
}

func deltaE(a, b [3]float64) float64 {
	return math.Sqrt((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))
}

// ---- fitting ----

// The look's limits: corrections, never a different picture.
var lookLo = [5]float64{0.6, 0.7, 0.5, -0.25, -0.2}
var lookHi = [5]float64{1.6, 1.4, 1.6, 0.25, 0.2}

// castShare is how much of the colour difference is matched: people's skin
// differs, so a face moves half way to the reference's colour (all the way in
// brightness and contrast).
const castShare = 0.5

func lookOf(v [5]float64) *editspec.Look {
	for k := range v {
		v[k] = math.Max(lookLo[k], math.Min(lookHi[k], v[k]))
	}
	r := func(x float64) float64 { return math.Round(x*1000) / 1000 }
	l := &editspec.Look{Exposure: r(v[0]), Contrast: r(v[1]), Saturation: r(v[2]), Warmth: r(v[3]), Tint: r(v[4])}
	if math.Abs(l.Exposure-1) < 0.01 && math.Abs(l.Contrast-1) < 0.01 && math.Abs(l.Saturation-1) < 0.01 && math.Abs(l.Warmth) < 0.005 && math.Abs(l.Tint) < 0.005 {
		return nil
	}
	return l
}

// fitLook finds the look that brings a picture's face to the reference's:
// brightness and contrast fully, the colour by castShare (Nelder–Mead).
func fitLook(ps *pictureSample, ref picStats) *editspec.Look {
	// A few thousand pixels are plenty.
	sub := &pictureSample{frames: ps.frames}
	step := max(1, len(ps.px)/3000)
	for i := 0; i < len(ps.px); i += step {
		sub.px = append(sub.px, ps.px[i])
	}
	own := sub.stats(nil)
	ta := own.lab[1] + castShare*(ref.lab[1]-own.lab[1])
	tb := own.lab[2] + castShare*(ref.lab[2]-own.lab[2])
	cost := func(v [5]float64) float64 {
		st := sub.stats(lookOf(v))
		dl := st.lab[0] - ref.lab[0]
		da, db := st.lab[1]-ta, st.lab[2]-tb
		ds := st.spread - ref.spread
		// Out-of-range corners cost a little more, so the fit stays inside.
		pen := 0.0
		for k := range v {
			if v[k] < lookLo[k] {
				pen += (lookLo[k] - v[k]) * 100
			}
			if v[k] > lookHi[k] {
				pen += (v[k] - lookHi[k]) * 100
			}
		}
		return dl*dl + da*da + db*db + ds*ds + pen
	}
	start := [5]float64{1, 1, 1, 0, 0}
	best := nelderMead(cost, start, [5]float64{0.15, 0.1, 0.15, 0.05, 0.05}, 400)
	return lookOf(best)
}

func nelderMead(f func([5]float64) float64, x0, scale [5]float64, iters int) [5]float64 {
	type pt struct {
		x [5]float64
		v float64
	}
	pts := make([]pt, 6)
	pts[0] = pt{x0, f(x0)}
	for i := 0; i < 5; i++ {
		x := x0
		x[i] += scale[i]
		pts[i+1] = pt{x, f(x)}
	}
	for it := 0; it < iters; it++ {
		sort.Slice(pts, func(i, j int) bool { return pts[i].v < pts[j].v })
		if pts[5].v-pts[0].v < 1e-6 {
			break
		}
		var cen [5]float64
		for i := 0; i < 5; i++ {
			for k := range cen {
				cen[k] += pts[i].x[k] / 5
			}
		}
		at := func(t float64) [5]float64 {
			var x [5]float64
			for k := range x {
				x[k] = cen[k] + t*(pts[5].x[k]-cen[k])
			}
			return x
		}
		xr := at(-1)
		vr := f(xr)
		switch {
		case vr < pts[0].v:
			xe := at(-2)
			if ve := f(xe); ve < vr {
				pts[5] = pt{xe, ve}
			} else {
				pts[5] = pt{xr, vr}
			}
		case vr < pts[4].v:
			pts[5] = pt{xr, vr}
		default:
			xc := at(0.5)
			if vc := f(xc); vc < pts[5].v {
				pts[5] = pt{xc, vc}
			} else {
				for i := 1; i < 6; i++ {
					var x [5]float64
					for k := range x {
						x[k] = pts[0].x[k] + 0.5*(pts[i].x[k]-pts[0].x[k])
					}
					pts[i] = pt{x, f(x)}
				}
			}
		}
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].v < pts[j].v })
	return pts[0].x
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("conversation.match", ops.Offering{ID: "matched-webcams", Group: "edit", Title: "Matched webcams",
		Does:  "Matches the webcams' brightness, contrast and colour to one person's picture, measured before and after.",
		Words: []string{"webcam colours", "make cameras match"}, Needs: ops.Needs{Works: []string{"editing"}}})
}
