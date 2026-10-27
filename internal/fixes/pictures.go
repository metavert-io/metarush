package fixes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // decoders
	_ "image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/oldnames"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Pictures and logos (D152): their fixes make a new version of the resource
// (with its fix steps and the version it came from), current for new uses;
// unticking makes the original version current again. Cuts keep the version
// they pin until someone updates them.

// Picture thresholds.
const (
	PlainBorderFits = 0.90 // share of the edge in one colour
	EmptyEdgeFits   = 0.08 // share of a side that is empty
	SmallFits       = 1280 // pixels on the longer side under it
	bgTolerance     = 40   // colour distance (sum of channels) still counted as the background
)

// SegmentSubject is Apple Vision's subject mask on a Mac (the on-screen
// helper): in is a picture, out gets a PNG of the subject on transparency.
// nil elsewhere; pictures without a plain background then keep theirs.
var SegmentSubject func(ctx context.Context, a *app.App, in, out string) error

// ErrNoSubject: Apple Vision found no subject in the picture.
var ErrNoSubject = errors.New("no subject found")

func loadPicture(ctx context.Context, a *app.App, id string) (*models.Resource, *models.ResourceVersion, error) {
	var r models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), id, &r); err != nil {
		return nil, nil, apperr.New(apperr.NotFound, "MetaRush can't find that picture.").WithDetails(map[string]any{"id": id})
	}
	if r.Kind != models.ResourceImage {
		return &r, nil, apperr.New(apperr.InvalidInput, "Only pictures (PNG, JPEG, WebP) have fixes; “%s” is a %s.", r.Title, resourceKindWords(r.Kind))
	}
	from := r.Current
	if r.Fixes != nil && r.Fixes.From != "" && r.Version(r.Fixes.From) != nil {
		from = r.Fixes.From
	}
	v := r.Version(from)
	if v == nil {
		return &r, nil, apperr.New(apperr.NotFound, "“%s” has no picture to fix.", r.Title)
	}
	return &r, v, nil
}

func resourceKindWords(k string) string {
	switch k {
	case models.ResourceSVG:
		return "drawing (SVG)"
	case models.ResourcePDF:
		return "PDF"
	case models.ResourceFont:
		return "font"
	}
	return k
}

// pictureFile is a version's file on this machine.
func pictureFile(ctx context.Context, a *app.App, v *models.ResourceVersion) (string, error) {
	if !v.Stored {
		return "", apperr.New(apperr.Conflict, "This picture is only on the computer whose linked folder holds it; fix it there.")
	}
	return a.Cache.Ensure(ctx, a.Media, v.StorageKey, v.SHA256, v.Size, nil)
}

func decodePicture(path string) (*image.NRGBA, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("this picture couldn't be read: %v", err)
	}
	out := image.NewNRGBA(img.Bounds())
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out, nil
}

// PictureFacts is what MetaRush measures in a picture.
type PictureFacts struct {
	Width, Height int
	HasAlpha      bool
	// Background is the edge colour; Plain the share of the edge in it.
	Background color.NRGBA
	Plain      float64
	// Margins are the empty share of each side (left, top, right, bottom).
	Margins [4]float64
}

func pictureFacts(img *image.NRGBA) PictureFacts {
	b := img.Bounds()
	f := PictureFacts{Width: b.Dx(), Height: b.Dy()}
	for y := b.Min.Y; y < b.Max.Y && !f.HasAlpha; y += max(1, b.Dy()/300) {
		for x := b.Min.X; x < b.Max.X; x += max(1, b.Dx()/300) {
			if img.NRGBAAt(x, y).A < 250 {
				f.HasAlpha = true
				break
			}
		}
	}
	edge := edgePixels(img)
	f.Background = medianColour(edge)
	near := 0
	for _, c := range edge {
		if colourDist(c, f.Background) <= bgTolerance {
			near++
		}
	}
	if len(edge) > 0 {
		f.Plain = float64(near) / float64(len(edge))
	}
	x0, y0, x1, y1 := contentBox(img, f.Background, f.HasAlpha)
	if x1 > x0 && y1 > y0 {
		f.Margins = [4]float64{float64(x0-b.Min.X) / float64(b.Dx()), float64(y0-b.Min.Y) / float64(b.Dy()), float64(b.Max.X-x1) / float64(b.Dx()), float64(b.Max.Y-y1) / float64(b.Dy())}
	}
	return f
}

func edgePixels(img *image.NRGBA) []color.NRGBA {
	b := img.Bounds()
	var out []color.NRGBA
	step := max(1, max(b.Dx(), b.Dy())/400)
	for x := b.Min.X; x < b.Max.X; x += step {
		out = append(out, img.NRGBAAt(x, b.Min.Y), img.NRGBAAt(x, b.Max.Y-1))
	}
	for y := b.Min.Y; y < b.Max.Y; y += step {
		out = append(out, img.NRGBAAt(b.Min.X, y), img.NRGBAAt(b.Max.X-1, y))
	}
	return out
}

func medianColour(cs []color.NRGBA) color.NRGBA {
	if len(cs) == 0 {
		return color.NRGBA{255, 255, 255, 255}
	}
	ch := func(get func(color.NRGBA) uint8) uint8 {
		v := make([]int, len(cs))
		for i, c := range cs {
			v[i] = int(get(c))
		}
		sort.Ints(v)
		return uint8(v[len(v)/2])
	}
	return color.NRGBA{ch(func(c color.NRGBA) uint8 { return c.R }), ch(func(c color.NRGBA) uint8 { return c.G }), ch(func(c color.NRGBA) uint8 { return c.B }), ch(func(c color.NRGBA) uint8 { return c.A })}
}

func colourDist(a, b color.NRGBA) int {
	d := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	return d(a.R, b.R) + d(a.G, b.G) + d(a.B, b.B) + d(a.A, b.A)
}

// contentBox is the box of what isn't background (or transparent).
func contentBox(img *image.NRGBA, bg color.NRGBA, alpha bool) (x0, y0, x1, y1 int) {
	b := img.Bounds()
	x0, y0, x1, y1 = b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			empty := c.A < 12 || (!alpha && colourDist(c, bg) <= bgTolerance)
			if empty {
				continue
			}
			x0, y0 = min(x0, x), min(y0, y)
			x1, y1 = max(x1, x+1), max(y1, y+1)
		}
	}
	return
}

// pictureMarks are the picture's fixes that fit.
func pictureMarks(f PictureFacts) []models.FixMark {
	bg := models.FixMark{Fix: models.FixBackground, Value: round(f.Plain, 3), Unit: "share", Measured: fmt.Sprintf("%.0f%% of its edge is one colour", f.Plain*100)}
	if f.HasAlpha {
		bg.Measured = "it already has a see-through background"
	} else if f.Plain >= PlainBorderFits {
		bg.Fits, bg.Why = true, "It has a plain background"
	}
	m := 0.0
	for _, v := range f.Margins {
		m = math.Max(m, v)
	}
	edges := models.FixMark{Fix: models.FixEdges, Value: round(m, 3), Unit: "share", Measured: fmt.Sprintf("up to %.0f%% empty on a side", m*100)}
	if m >= EmptyEdgeFits {
		edges.Fits, edges.Why = true, "It has empty space around it"
	}
	long := max(f.Width, f.Height)
	larger := models.FixMark{Fix: models.FixLarger, Value: float64(long), Unit: "px", Measured: fmt.Sprintf("%d × %d pixels", f.Width, f.Height)}
	if long < SmallFits {
		larger.Fits, larger.Why = true, fmt.Sprintf("It's small (%d × %d)", f.Width, f.Height)
	}
	return []models.FixMark{bg, edges, larger}
}

// removeBackground makes the edge colour see-through where it reaches from
// the edges (a plain background), with soft edges and the background's tint
// taken out of the outline.
func removeBackground(img *image.NRGBA, bg color.NRGBA) *image.NRGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewNRGBA(b)
	copy(out.Pix, img.Pix)
	seen := make([]bool, w*h)
	var queue []int
	push := func(x, y int) {
		i := y*w + x
		if seen[i] {
			return
		}
		if colourDist(img.NRGBAAt(b.Min.X+x, b.Min.Y+y), bg) > bgTolerance {
			return
		}
		seen[i] = true
		queue = append(queue, i)
	}
	for x := 0; x < w; x++ {
		push(x, 0)
		push(x, h-1)
	}
	for y := 0; y < h; y++ {
		push(0, y)
		push(w-1, y)
	}
	for len(queue) > 0 {
		i := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x, y := i%w, i/w
		if x > 0 {
			push(x-1, y)
		}
		if x+1 < w {
			push(x+1, y)
		}
		if y > 0 {
			push(x, y-1)
		}
		if y+1 < h {
			push(x, y+1)
		}
	}
	for i, bgPix := range seen {
		x, y := i%w, i/w
		px := b.Min.X + x
		py := b.Min.Y + y
		if bgPix {
			out.SetNRGBA(px, py, color.NRGBA{bg.R, bg.G, bg.B, 0})
			continue
		}
		// Next to the background: soft edges by how far the colour is from it.
		nearBG := (x > 0 && seen[i-1]) || (x+1 < w && seen[i+1]) || (y > 0 && seen[i-w]) || (y+1 < h && seen[i+w])
		if !nearBG {
			continue
		}
		c := img.NRGBAAt(px, py)
		d := float64(colourDist(c, bg))
		alpha := math.Max(0.15, math.Min(1, (d-bgTolerance)/(2*bgTolerance)+0.5))
		un := func(v, bv uint8) uint8 {
			x := (float64(v) - (1-alpha)*float64(bv)) / alpha
			return uint8(math.Max(0, math.Min(255, math.Round(x))))
		}
		out.SetNRGBA(px, py, color.NRGBA{un(c.R, bg.R), un(c.G, bg.G), un(c.B, bg.B), uint8(math.Round(alpha * float64(c.A)))})
	}
	return out
}

// trimEdges crops a picture to what is in it, keeping a small margin.
func trimEdges(img *image.NRGBA, bg color.NRGBA, alpha bool) *image.NRGBA {
	x0, y0, x1, y1 := contentBox(img, bg, alpha)
	b := img.Bounds()
	if x1 <= x0 || y1 <= y0 {
		return img
	}
	m := max(4, int(0.03*float64(max(x1-x0, y1-y0))))
	r := image.Rect(max(b.Min.X, x0-m), max(b.Min.Y, y0-m), min(b.Max.X, x1+m), min(b.Max.Y, y1+m))
	out := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
	return out
}

// enlarge makes a small picture two or four times larger (Catmull-Rom) and
// sharpens it gently (an unsharp mask over the colour, not the transparency).
func enlarge(img *image.NRGBA) (*image.NRGBA, int) {
	b := img.Bounds()
	long := max(b.Dx(), b.Dy())
	f := 2
	if long < 600 {
		f = 4
	}
	for f > 1 && long*f > 4096 {
		f--
	}
	if f < 2 {
		return img, 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx()*f, b.Dy()*f))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return unsharp(dst, 0.6), f
}

// unsharp sharpens with a 3×3 blur: v + amount × (v − blur(v)).
func unsharp(img *image.NRGBA, amount float64) *image.NRGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewNRGBA(b)
	copy(out.Pix, img.Pix)
	k := [3]float64{0.25, 0.5, 0.25}
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*img.Stride + x*4
			for c := 0; c < 3; c++ {
				blur := 0.0
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						blur += k[dy+1] * k[dx+1] * float64(img.Pix[(y+dy)*img.Stride+(x+dx)*4+c])
					}
				}
				v := float64(img.Pix[i+c])
				out.Pix[i+c] = uint8(math.Max(0, math.Min(255, math.Round(v+amount*(v-blur)))))
			}
		}
	}
	return out
}

// sharpness is the mean gradient at the picture's own size (for before and after).
func sharpness(img *image.NRGBA) float64 {
	b := img.Bounds()
	s, n := 0.0, 0
	for y := b.Min.Y + 1; y < b.Max.Y; y += max(1, b.Dy()/400) {
		for x := b.Min.X + 1; x < b.Max.X; x += max(1, b.Dx()/400) {
			c, l, u := img.NRGBAAt(x, y), img.NRGBAAt(x-1, y), img.NRGBAAt(x, y-1)
			luma := func(p color.NRGBA) float64 { return 0.299*float64(p.R) + 0.587*float64(p.G) + 0.114*float64(p.B) }
			s += math.Abs(luma(c)-luma(l)) + math.Abs(luma(c)-luma(u))
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return s / float64(n)
}

// pictureView is a picture's fixes.
func pictureView(ctx context.Context, a *app.App, id string) (*View, error) {
	r, v, err := loadPicture(ctx, a, id)
	if err != nil {
		return nil, err
	}
	fx := models.SourceFixes{}
	if r.Fixes != nil {
		fx = *r.Fixes
	}
	out := &View{ID: r.ID, Title: r.Title, Picture: true, Kind: models.SourcePicture, KindWords: models.SourceKindWords(models.SourcePicture),
		Kinds: []KindChoice{{models.SourcePicture, "Picture or logo"}}, Ticked: fx.Ticked, State: fx.State, Error: fx.Error, Using: "original", Fixes: []Row{}, Cuts: []CutUse{}}
	if out.Ticked == nil {
		out.Ticked = []string{}
	}
	if fx.State == models.FixPreparing && fx.JobID != "" {
		out.JobIDs = []string{fx.JobID}
	}
	marks := fx.Marks
	if len(marks) == 0 || fx.MarksFor != v.SHA256 {
		if p, err := pictureFile(ctx, a, v); err == nil {
			if img, err := decodePicture(p); err == nil {
				marks = pictureMarks(pictureFacts(img))
			}
		}
	}
	byFix := map[string]models.FixMark{}
	for _, m := range marks {
		byFix[m.Fix] = m
	}
	var cp *models.SourceCopy
	if fx.Copy != nil && r.Current == fx.Copy.SHA256 {
		out.Using = "fixed"
		var c models.SourceCopy
		if a.DB.GetByID(ctx, store.CSourceCopies, a.WorkspaceID(), fx.Copy.ID, &c) == nil {
			cp = &c
			out.Copy = &CopyView{SHA256: c.SHA256, Fixes: stepsOf(&c), Words: ops.CopyWords(&c), Measures: c.Measures, Notes: c.Notes, Took: c.Took, Size: c.Size}
		}
	}
	after := map[string]string{}
	if cp != nil {
		for _, m := range cp.Measures {
			after[m.Fix] = m.Words
		}
	}
	for _, f := range models.FixesFor(models.SourcePicture) {
		row := Row{ID: f.ID, Label: f.Label, Result: f.Result, Ticked: slices.Contains(fx.Ticked, f.ID), After: after[f.ID]}
		if m, ok := byFix[f.ID]; ok {
			row.Fits, row.Measured = m.Fits, m.Measured
			if m.Fits {
				row.Why = m.Why
			}
		}
		out.Fixes = append(out.Fixes, row)
	}
	if out.Using == "fixed" && out.Copy != nil {
		out.UsingWords = "New uses get the fixed version: " + out.Copy.Words + ". The original version is kept."
	} else {
		out.UsingWords = "New uses get the original."
	}
	// Cuts whose latest version pins this picture.
	var projects []models.Project
	_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"archived": bson.M{"$ne": true}}, &projects)
	var heads []string
	byProject := map[string]models.Project{}
	for _, p := range projects {
		byProject[p.ID] = p
		for _, h := range p.Branches {
			if h.Head != "" {
				heads = append(heads, h.Head)
			}
		}
	}
	var revs []models.Revision
	if len(heads) > 0 {
		_ = a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": heads}}, &revs)
	}
	seen := map[string]bool{}
	for _, rev := range revs {
		for _, pin := range rev.Spec.Resources {
			if pin.ID != r.ID || seen[rev.ID] {
				continue
			}
			p := byProject[rev.ProjectID]
			seen[rev.ID] = true
			cu := CutUse{ProjectID: p.ID, ProjectName: p.Name, Branch: rev.Branch, CutName: ops.CutName(ctx, a, &p, rev.Branch), RevisionID: rev.ID, Number: rev.Number}
			switch {
			case pin.SHA256 == r.Current:
				cu.Plays, cu.PlaysWords = map[bool]string{true: "fixed", false: "original"}[out.Using == "fixed"], "uses the version new uses get"
			default:
				cu.Plays, cu.PlaysWords = "other", "uses another version"
				if out.Using == "fixed" {
					cu.Offer = fmt.Sprintf("Use the fixed picture in “%s”", cu.CutName)
				} else {
					cu.Offer, cu.Original = fmt.Sprintf("Use the original picture in “%s”", cu.CutName), true
				}
			}
			out.Cuts = append(out.Cuts, cu)
		}
	}
	out.Summary = summary(out)
	return out, nil
}

// setPicture changes a picture's ticked fixes.
func setPicture(ctx context.Context, a *app.App, in *SetIn) (*View, error) {
	r, v, err := loadPicture(ctx, a, in.ID)
	if err != nil {
		return nil, err
	}
	fx := models.SourceFixes{Kind: models.SourcePicture}
	if r.Fixes != nil {
		fx = *r.Fixes
	}
	ticked := append([]string{}, fx.Ticked...)
	if in.Only != nil {
		ticked = append([]string{}, (*in.Only)...)
	}
	for _, id := range in.Tick {
		if !slices.Contains(ticked, id) {
			ticked = append(ticked, id)
		}
	}
	ticked = slices.DeleteFunc(ticked, func(id string) bool { return slices.Contains(in.Untick, id) })
	for _, id := range ticked {
		if !offered(models.SourcePicture, id) {
			return nil, apperr.New(apperr.InvalidInput, "Pictures have three fixes: background, edges and larger (not %q).", id)
		}
	}
	ticked = models.RunOrder(ticked)
	take := 0
	if in.Again {
		take = int(time.Now().Unix())
	}
	fp := Fingerprint(ticked, take)
	now := time.Now().UTC()
	actor := a.Actor
	from := v.SHA256
	set := bson.M{"fixes.kind": models.SourcePicture, "fixes.ticked": ticked, "fixes.fingerprint": fp, "fixes.from": from, "fixes.updatedAt": now, "fixes.updatedBy": actor, "fixes.error": "", "updatedAt": now}
	var job *models.Job
	switch {
	case fp == "":
		set["fixes.state"], set["current"] = "", from
	default:
		id := ops.CopyID(a.WorkspaceID(), from, fp)
		var c models.SourceCopy
		if a.DB.GetByID(ctx, store.CSourceCopies, a.WorkspaceID(), id, &c) == nil && c.State == models.FixReady && r.Version(c.SHA256) != nil {
			set["fixes.state"], set["current"] = models.FixReady, c.SHA256
			set["fixes.copy"] = models.FixedCopyRef{ID: c.ID, SHA256: c.SHA256, Size: c.Size, MIME: c.MIME, Fingerprint: fp, Fixes: stepsOf(&c), At: now}
		} else {
			if c.ID == "" {
				steps := models.FixSteps{Version: StepsVersion, Kind: models.SourcePicture, Take: take}
				for _, f := range copyFixes(ticked) {
					steps.Steps = append(steps.Steps, models.FixStep{Fix: f})
				}
				c = models.SourceCopy{ID: id, WorkspaceID: a.WorkspaceID(), ResourceID: r.ID, SourceSHA256: from, Fingerprint: fp, FixSteps: steps, State: models.FixPreparing,
					CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
				if err := a.DB.Insert(ctx, store.CSourceCopies, c); err != nil && !store.IsDuplicateKey(err) {
					return nil, err
				}
			} else {
				_ = a.DB.Update(ctx, store.CSourceCopies, a.WorkspaceID(), id, bson.M{"$set": bson.M{"state": models.FixPreparing, "error": "", "updatedAt": now}})
			}
			j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: TypePrepare, IdempotencyKey: TypePrepare + ":" + id, Priority: 5, ProjectID: r.ProjectID, MaxAttempts: 2, Actor: a.Actor,
				Payload: map[string]any{"resourceId": r.ID, "copyId": id, "doing": Doing(ticked), "title": r.Title}})
			if err != nil {
				return nil, err
			}
			if j.State == models.JobFailed || j.State == models.JobCanceled {
				if again, err := a.Queue.Retry(ctx, j.ID); err == nil {
					j = again
				}
			}
			job = j
			set["fixes.state"], set["fixes.jobId"] = models.FixPreparing, j.ID
		}
	}
	unset := bson.M{oldnames.FixesFingerprint: ""}
	if fp == "" {
		unset["fixes.copy"] = ""
	}
	if err := a.DB.Update(ctx, store.CResources, a.WorkspaceID(), r.ID, bson.M{"$set": set, "$unset": unset}); err != nil {
		return nil, err
	}
	a.Audit(ctx, "asset.fixes.set", "resource", r.ID, r.ProjectID, map[string]any{"ticked": ticked, "fingerprint": fp})
	vw, err := pictureView(ctx, a, r.ID)
	if err != nil {
		return nil, err
	}
	if job != nil {
		vw.JobIDs = []string{job.ID}
	}
	return vw, nil
}

// preparePicture makes a picture's fixed version.
func preparePicture(ctx context.Context, a *app.App, j *models.Job, report Report) (map[string]any, error) {
	rid, _ := j.Payload["resourceId"].(string)
	copyID, _ := j.Payload["copyId"].(string)
	var c models.SourceCopy
	if err := a.DB.GetByID(ctx, store.CSourceCopies, a.WorkspaceID(), copyID, &c); err != nil {
		return nil, Final(fmt.Errorf("fixed copy %s not found", copyID))
	}
	var r models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), rid, &r); err != nil {
		return nil, Final(fmt.Errorf("picture %s not found", rid))
	}
	v := r.Version(c.SourceSHA256)
	if v == nil {
		return nil, Final(fmt.Errorf("the version of “%s” the fixes were for isn't there any more", r.Title))
	}
	start := time.Now()
	report(0.1, "getting the picture")
	p, err := pictureFile(ctx, a, v)
	if err != nil {
		return nil, Final(err)
	}
	img, err := decodePicture(p)
	if err != nil {
		return nil, Final(err)
	}
	facts := pictureFacts(img)
	before := pictureMarks(facts)
	steps := stepsOf(&c)
	var done []models.FixStep
	var notes []string
	cur := img
	alpha := facts.HasAlpha
	if slices.Contains(steps, models.FixBackground) {
		report(0.3, "removing the background")
		switch {
		case facts.Plain >= 0.6 && !facts.HasAlpha:
			cur, alpha = removeBackground(cur, facts.Background), true
			done = append(done, models.FixStep{Fix: models.FixBackground, Engine: "plain colour", Params: map[string]string{"colour": hexColour(facts.Background), "tolerance": fmt.Sprint(bgTolerance)}, Tool: "metarush"})
		case SegmentSubject != nil:
			dir := filepath.Join(a.Cfg.Scratch.Dir, "fix-"+j.ID)
			_ = os.MkdirAll(dir, 0o755)
			defer os.RemoveAll(dir)
			// The picture as decoded (upright), so the cut-out lines up with it.
			in, out := filepath.Join(dir, "picture.png"), filepath.Join(dir, "subject.png")
			var buf bytes.Buffer
			if err := png.Encode(&buf, cur); err != nil {
				return nil, err
			}
			if err := os.WriteFile(in, buf.Bytes(), 0o600); err != nil {
				return nil, err
			}
			switch err := SegmentSubject(ctx, a, in, out); {
			case errors.Is(err, ErrNoSubject):
				notes = append(notes, "MetaRush found no clear subject to keep, so the background was kept.")
			case err != nil:
				return nil, fmt.Errorf("finding the subject failed: %w", err)
			default:
				if cur, err = decodePicture(out); err != nil {
					return nil, err
				}
				alpha = true
				done = append(done, models.FixStep{Fix: models.FixBackground, Engine: "subject mask", Tool: "Apple Vision"})
			}
		default:
			notes = append(notes, "Its background isn't one plain colour, and finding the subject of a photo needs a Mac; the background was kept.")
		}
	}
	if slices.Contains(steps, models.FixEdges) {
		report(0.55, "trimming the empty edges")
		cur = trimEdges(cur, facts.Background, alpha)
		done = append(done, models.FixStep{Fix: models.FixEdges, Engine: "content box", Tool: "metarush"})
	}
	crisper := 0.0 // how much crisper than a plain enlargement to the same size
	if slices.Contains(steps, models.FixLarger) {
		report(0.7, "making it larger and sharper")
		small := cur
		var f int
		cur, f = enlarge(cur)
		if f > 1 {
			plain := image.NewNRGBA(cur.Bounds())
			draw.CatmullRom.Scale(plain, plain.Bounds(), small, small.Bounds(), draw.Src, nil)
			if p := sharpness(plain); p > 0 {
				crisper = sharpness(cur)/p - 1
			}
		}
		done = append(done, models.FixStep{Fix: models.FixLarger, Engine: "catmull-rom + unsharp", Params: map[string]string{"factor": fmt.Sprint(f)}, Tool: "metarush"})
	}
	report(0.85, "saving the fixed version")
	dir := filepath.Join(a.Cfg.Scratch.Dir, "fixpic-"+j.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	name := strings.TrimSuffix(firstNonEmpty(v.Source.Name, r.Name+"."+v.Ext), filepath.Ext(firstNonEmpty(v.Source.Name, r.Name+"."+v.Ext))) + "-fixed.png"
	out := filepath.Join(dir, name)
	var buf bytes.Buffer
	if err := png.Encode(&buf, cur); err != nil {
		return nil, err
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return nil, err
	}
	res, err := resources.AddFile(ctx, a, out, resources.AddOptions{ResourceID: r.ID, FileName: name, Via: "worker",
		Source: models.ResourceSource{Kind: models.FromGenerated, Name: name, Tool: "metarush fixes", Lineage: []models.ResourceLineage{{ResourceID: r.ID, SHA256: v.SHA256, Step: models.FixResults(steps)}}}})
	if err != nil {
		return nil, err
	}
	newFacts := pictureFacts(cur)
	afterMarks := pictureMarks(newFacts)
	var measures []models.FixMeasure
	for i, b := range before {
		if !slices.Contains(steps, b.Fix) {
			continue
		}
		m := models.FixMeasure{Fix: b.Fix, Unit: b.Unit, Before: b.Value, After: afterMarks[i].Value}
		switch b.Fix {
		case models.FixBackground:
			m.Words = fmt.Sprintf("see-through background: %s → %s", yesNo(facts.HasAlpha), yesNo(newFacts.HasAlpha))
		case models.FixEdges:
			m.Words = fmt.Sprintf("empty space on a side: %.0f%% → %.0f%%", b.Value*100, afterMarks[i].Value*100)
		case models.FixLarger:
			m.Words = fmt.Sprintf("size: %d × %d → %d × %d pixels", facts.Width, facts.Height, newFacts.Width, newFacts.Height)
			if pct := math.Round(crisper * 100); pct >= 1 {
				m.Words += fmt.Sprintf("; edges %.0f%% crisper than a plain enlargement", pct)
			}
		}
		measures = append(measures, m)
	}
	now := time.Now().UTC()
	made := c.FixSteps
	made.Steps = done
	if err := a.DB.Update(ctx, store.CSourceCopies, a.WorkspaceID(), c.ID, bson.M{"$set": bson.M{"state": models.FixReady, "sha256": res.SHA256, "size": int64(buf.Len()), "mime": "image/png",
		"measures": measures, "notes": notes, "fingerprint": c.Fingerprint, "fixSteps": made, "jobId": j.ID, "madeOn": a.Cfg.Installation.ID, "took": round(time.Since(start).Seconds(), 1), "updatedAt": now},
		"$unset": bson.M{oldnames.Fingerprint: "", oldnames.FixSteps: ""}}); err != nil {
		return nil, err
	}
	ref := models.FixedCopyRef{ID: c.ID, SHA256: res.SHA256, Size: int64(buf.Len()), MIME: "image/png", Fingerprint: c.Fingerprint, Fixes: steps, At: now}
	// The fixed version is current for new uses only while these fixes are still ticked.
	filter := oldnames.FingerprintIs(c.Fingerprint)
	filter["_id"], filter["workspaceId"] = r.ID, a.WorkspaceID()
	if _, err := a.DB.C(store.CResources).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"current": res.SHA256, "fixes.copy": ref, "fixes.fingerprint": c.Fingerprint, "fixes.state": models.FixReady, "updatedAt": now},
		"$unset": bson.M{oldnames.FixesFingerprint: ""}}); err != nil {
		return nil, err
	}
	return map[string]any{"sha256": res.SHA256, "fixes": steps}, nil
}

func hexColour(c color.NRGBA) string { return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B) }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
