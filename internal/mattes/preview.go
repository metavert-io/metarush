package mattes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // pictures may be GIFs
	"image/jpeg"
	_ "image/png" // pictures may be PNGs
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp" // pictures may be WebP

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/local"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/matting"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/tools"
)

// What can show behind the people (editspec.ClipBackground's With).
const (
	WithColour  = "colour"
	WithBlur    = "blur"
	WithPicture = "picture"
	WithFootage = "footage"
)

// CutoutPreviewIn asks how a clip looks at one moment with its background replaced.
type CutoutPreviewIn struct {
	AssetID   string  `json:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	ProjectID string  `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$,description=The project whose pictures a picture is looked up in."`
	At        float64 `json:"at" jsonschema:"minimum=0,description=The moment of the clip\\, in seconds."`
	Fixed     string  `json:"fixed,omitempty" jsonschema:"maxLength=64,description=The clip's media: original\\, or a fixed copy's SHA-256 (default: what it plays now)."`
	With      string  `json:"with" jsonschema:"enum=colour,enum=blur,enum=picture,enum=footage,description=What shows behind the people."`
	Colour    string  `json:"colour,omitempty" jsonschema:"pattern=^(#[0-9a-fA-F]{6})?$"`
	Blur      float64 `json:"blur,omitempty" jsonschema:"minimum=0,maximum=100,description=How strong the blur is (pixels at 1080; default 28)."`
	Picture   string  `json:"picture,omitempty" jsonschema:"maxLength=200,description=A picture from the resources library (its short name)."`
	Footage   string  `json:"footage,omitempty" jsonschema:"pattern=^(ast_[0-9a-z]{26})?$,description=Another clip to show behind."`
	FootageAt float64 `json:"footageAt,omitempty" jsonschema:"minimum=0,description=The moment of that clip to show."`
	Feather   float64 `json:"feather,omitempty" jsonschema:"minimum=0,maximum=40,description=How soft the people's edges are (pixels at 1080)."`
	Width     int     `json:"width,omitempty" jsonschema:"minimum=0,maximum=1920,description=The preview's width (default 960)."`
	// Out is where the CLI writes the still (default: a folder of its own).
	Out string `json:"out,omitempty" jsonschema:"maxLength=1024"`
}

// CutoutPreviewOut is a still of a clip with its background replaced.
type CutoutPreviewOut struct {
	Path    string `json:"path" jsonschema:"description=The still (a JPEG) on this computer."`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Model   string `json:"model" jsonschema:"description=The model that found the people for it (provider/model)."`
	Summary string `json:"summary"`
}

// DefaultBlur and DefaultFeather are a blur's strength and an edge's
// softness when none is said (pixels at 1080).
const (
	DefaultBlur    = 28.0
	DefaultFeather = 2.0
)

// Preview is how a clip looks at one moment with its background replaced,
// as a JPEG: the people found in that frame by this computer's best model
// (the cut-out itself may be made on another computer, with another), over
// what goes behind them, their edges as soft as asked. Nothing is kept.
func Preview(ctx context.Context, a *app.App, in *CutoutPreviewIn) ([]byte, *CutoutPreviewOut, error) {
	as, err := clipFor(ctx, a, in.AssetID)
	if err != nil {
		return nil, nil, err
	}
	fixed := strings.TrimSpace(in.Fixed)
	if fixed == "" {
		fixed = "now"
	}
	_, _, key, sha, size, _, err := mediaOf(ctx, a, as, fixed)
	if err != nil {
		return nil, nil, err
	}
	refs := LocalModels(a)
	if len(refs) == 0 {
		return nil, nil, apperr.New(apperr.ToolMissing, "This computer can't find the people in footage, so it can't show a preview here.").
			WithHint("Add “Replacing a video's background” in Settings → Components (on a Mac, “Seeing what's on screen”).").
			WithDetails(map[string]any{"component": matting.Component}).WithCommand("metarush components add " + matting.Component)
	}
	provider, model, _ := strings.Cut(refs[0], "/")
	env := aiservices.Env{Tools: a.Tools, Cfg: a.Cfg, Home: a.Cfg.Home(), App: a}
	mm, err := local.Matter(env, model)
	if err != nil {
		return nil, nil, err
	}
	src, err := a.Cache.Ensure(ctx, a.Media, key, sha, size, nil)
	if err != nil {
		return nil, nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get “%s” from its storage.", as.DisplayTitle())
	}
	v, err := matting.Probe(ctx, a.Tools, src)
	if err != nil {
		return nil, nil, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't read “%s”.", as.DisplayTitle())
	}
	at := math.Max(0, math.Min(in.At, math.Max(0, v.Duration-1/v.FPS)))
	pw := in.Width
	if pw <= 0 {
		pw = 960
	}
	pw = matting.Even(min(pw, v.Width, 1920))
	ph := matting.Even(int(math.Round(float64(pw) * float64(v.Height) / float64(v.Width))))
	fg, err := frameAt(ctx, a.Tools, src, at, pw, ph, "rgb24")
	if err != nil {
		return nil, nil, err
	}
	mw, mh := mm.Size(v.Width, v.Height)
	mw, mh = matting.Even(mw), matting.Even(mh)
	small, err := frameAt(ctx, a.Tools, src, at, mw, mh, mm.Format())
	if err != nil {
		return nil, nil, err
	}
	st, err := mm.Open(ctx, mw, mh)
	if err != nil {
		return nil, nil, err
	}
	raw, err := st.Next(small)
	if cerr := st.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, nil, apperr.Wrap(apperr.ToolFailed, err, "The people in “%s” couldn't be found for the preview.", as.DisplayTitle())
	}
	alpha := scaleGray(raw, mw, mh, pw, ph)
	scale := float64(min(pw, ph)) / 1080
	feather := in.Feather
	if feather == 0 {
		feather = DefaultFeather
	}
	alpha = featherGray(alpha, pw, ph, feather*scale)
	bg, err := backgroundFor(ctx, a, in, fg, pw, ph, scale)
	if err != nil {
		return nil, nil, err
	}
	out := image.NewRGBA(image.Rect(0, 0, pw, ph))
	for i := 0; i < pw*ph; i++ {
		m := float64(alpha[i]) / 255
		for c := 0; c < 3; c++ {
			out.Pix[4*i+c] = uint8(math.Round(float64(fg[3*i+c])*m + float64(bg[3*i+c])*(1-m)))
		}
		out.Pix[4*i+3] = 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 88}); err != nil {
		return nil, nil, err
	}
	info := &CutoutPreviewOut{Width: pw, Height: ph, Model: provider + "/" + model,
		Summary: fmt.Sprintf("“%s” at %s with %s behind the people (found by %s on this computer).", as.DisplayTitle(), clock(at), withWords(in.With), modelTitle(provider, model))}
	return buf.Bytes(), info, nil
}

// withWords says what goes behind the people.
func withWords(with string) string {
	switch with {
	case WithBlur:
		return "a blur of the clip"
	case WithPicture:
		return "a picture"
	case WithFootage:
		return "another clip"
	}
	return "a colour"
}

func modelTitle(provider, model string) string {
	if m, err := aiservices.ModelFor(provider, aiservices.KindVideoBackground, model); err == nil {
		return m.Title
	}
	return model
}

// frameAt is one frame of a file at w×h in a pixel format, as bytes.
func frameAt(ctx context.Context, ts *tools.Set, src string, at float64, w, h int, format string) ([]byte, error) {
	var buf bytes.Buffer
	_, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Stdout: &buf, Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", at), "-i", src, "-map", "0:v:0", "-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:%d:flags=area,format=%s", w, h, format),
		"-f", "rawvideo", "pipe:1"}})
	if err != nil {
		return nil, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't take that frame.")
	}
	if buf.Len() < w*h*matting.Bytes(format) {
		return nil, apperr.New(apperr.ToolFailed, "MetaRush couldn't take that frame.")
	}
	return buf.Bytes()[:w*h*matting.Bytes(format)], nil
}

// backgroundFor is what goes behind the people, w×h RGB.
func backgroundFor(ctx context.Context, a *app.App, in *CutoutPreviewIn, fg []byte, w, h int, scale float64) ([]byte, error) {
	switch in.With {
	case "", WithColour:
		c := parseColour(in.Colour)
		out := make([]byte, 3*w*h)
		for i := 0; i < w*h; i++ {
			out[3*i], out[3*i+1], out[3*i+2] = c.R, c.G, c.B
		}
		return out, nil
	case WithBlur:
		b := in.Blur
		if b == 0 {
			b = DefaultBlur
		}
		return blurRGB(fg, w, h, b*scale), nil
	case WithPicture:
		img, err := pictureNamed(ctx, a, in.ProjectID, in.Picture)
		if err != nil {
			return nil, err
		}
		return coverRGB(img, w, h), nil
	case WithFootage:
		other, err := clipFor(ctx, a, in.Footage)
		if err != nil {
			return nil, err
		}
		_, _, key, sha, size, _, err := mediaOf(ctx, a, other, "now")
		if err != nil {
			return nil, err
		}
		src, err := a.Cache.Ensure(ctx, a.Media, key, sha, size, nil)
		if err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get “%s” from its storage.", other.DisplayTitle())
		}
		v, err := matting.Probe(ctx, a.Tools, src)
		if err != nil {
			return nil, err
		}
		ow := matting.Even(min(v.Width, 1920))
		oh := matting.Even(int(math.Round(float64(ow) * float64(v.Height) / float64(v.Width))))
		raw, err := frameAt(ctx, a.Tools, src, math.Min(in.FootageAt, math.Max(0, v.Duration-0.1)), ow, oh, "rgb24")
		if err != nil {
			return nil, err
		}
		img := image.NewRGBA(image.Rect(0, 0, ow, oh))
		for i := 0; i < ow*oh; i++ {
			img.Pix[4*i], img.Pix[4*i+1], img.Pix[4*i+2], img.Pix[4*i+3] = raw[3*i], raw[3*i+1], raw[3*i+2], 255
		}
		return coverRGB(img, w, h), nil
	}
	return nil, apperr.New(apperr.InvalidInput, "Behind the people goes a colour, a blur of the clip, a picture or another clip, not “%s”.", in.With)
}

// pictureNamed reads a resources-library picture (never one taken from
// someone else's video, D457).
func pictureNamed(ctx context.Context, a *app.App, projectID, ref string) (image.Image, error) {
	name, version := strings.TrimSpace(ref), ""
	if i := strings.LastIndex(name, "@"); i > 0 {
		name, version = name[:i], name[i+1:]
	}
	if name == "" {
		return nil, apperr.New(apperr.InvalidInput, "Choose the picture to show behind the people.")
	}
	view, err := projectlinks.Load(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	e, err := resources.Find(ctx, a, projectID, view, name)
	if err != nil {
		return nil, err
	}
	r := &e.Resource
	if r.Kind != models.ResourceImage {
		return nil, apperr.New(apperr.InvalidInput, "“%s” isn't a photo or picture file.", r.Title)
	}
	sha := ""
	if version != "" {
		for _, v := range r.Versions {
			if strings.HasPrefix(v.SHA256, version) {
				sha = v.SHA256
			}
		}
	}
	if err := resources.RefuseFrameOfReference(ctx, a, r, sha, "show it behind the people"); err != nil {
		return nil, err
	}
	path, _, err := resources.LocalFile(ctx, a, r, sha, 0)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(io.LimitReader(f, 256<<20))
	if err != nil {
		return nil, apperr.New(apperr.UnsupportedMedia, "“%s” can't be read as a picture here.", r.Title)
	}
	return img, nil
}

// parseColour reads #rrggbb (black otherwise).
func parseColour(s string) color.RGBA {
	var c color.RGBA
	if len(s) == 7 && s[0] == '#' {
		_, _ = fmt.Sscanf(s[1:], "%02x%02x%02x", &c.R, &c.G, &c.B)
	}
	return c
}

// coverRGB scales a picture to cover w×h (cropping what overflows, the
// middle kept), as RGB bytes.
func coverRGB(img image.Image, w, h int) []byte {
	b := img.Bounds()
	sw, sh := float64(b.Dx()), float64(b.Dy())
	f := math.Max(float64(w)/sw, float64(h)/sh)
	ox, oy := (sw*f-float64(w))/2, (sh*f-float64(h))/2
	out := make([]byte, 3*w*h)
	for y := 0; y < h; y++ {
		sy := b.Min.Y + min(b.Dy()-1, int((float64(y)+oy)/f))
		for x := 0; x < w; x++ {
			sx := b.Min.X + min(b.Dx()-1, int((float64(x)+ox)/f))
			r, g, bb, _ := img.At(sx, sy).RGBA()
			out[3*(y*w+x)], out[3*(y*w+x)+1], out[3*(y*w+x)+2] = uint8(r>>8), uint8(g>>8), uint8(bb>>8)
		}
	}
	return out
}

// scaleGray scales a w×h grey image to W×H (bilinear).
func scaleGray(src []byte, w, h, W, H int) []byte {
	out := make([]byte, W*H)
	for y := 0; y < H; y++ {
		fy := math.Max(0, (float64(y)+0.5)*float64(h)/float64(H)-0.5)
		y0 := min(h-1, int(fy))
		y1 := min(h-1, y0+1)
		ty := fy - float64(y0)
		for x := 0; x < W; x++ {
			fx := math.Max(0, (float64(x)+0.5)*float64(w)/float64(W)-0.5)
			x0 := min(w-1, int(fx))
			x1 := min(w-1, x0+1)
			tx := fx - float64(x0)
			v := (float64(src[y0*w+x0])*(1-tx)+float64(src[y0*w+x1])*tx)*(1-ty) + (float64(src[y1*w+x0])*(1-tx)+float64(src[y1*w+x1])*tx)*ty
			out[y*W+x] = uint8(math.Round(v))
		}
	}
	return out
}

// boxBlur blurs one channel (stride step, offset off) of an image three
// times with a box of radius r: close to a Gaussian.
func boxBlur(px []byte, w, h, step, off, r int) {
	if r < 1 {
		return
	}
	tmp := make([]float64, max(w, h))
	for pass := 0; pass < 3; pass++ {
		for y := 0; y < h; y++ {
			sum := 0.0
			for x := -r; x <= r; x++ {
				sum += float64(px[(y*w+clampInt(x, 0, w-1))*step+off])
			}
			for x := 0; x < w; x++ {
				tmp[x] = sum / float64(2*r+1)
				sum += float64(px[(y*w+clampInt(x+r+1, 0, w-1))*step+off]) - float64(px[(y*w+clampInt(x-r, 0, w-1))*step+off])
			}
			for x := 0; x < w; x++ {
				px[(y*w+x)*step+off] = uint8(math.Round(tmp[x]))
			}
		}
		for x := 0; x < w; x++ {
			sum := 0.0
			for y := -r; y <= r; y++ {
				sum += float64(px[(clampInt(y, 0, h-1)*w+x)*step+off])
			}
			for y := 0; y < h; y++ {
				tmp[y] = sum / float64(2*r+1)
				sum += float64(px[(clampInt(y+r+1, 0, h-1)*w+x)*step+off]) - float64(px[(clampInt(y-r, 0, h-1)*w+x)*step+off])
			}
			for y := 0; y < h; y++ {
				px[(y*w+x)*step+off] = uint8(math.Round(tmp[y]))
			}
		}
	}
}

func clampInt(v, lo, hi int) int { return max(lo, min(hi, v)) }

// blurRGB is an RGB image blurred (sigma about s pixels).
func blurRGB(src []byte, w, h int, s float64) []byte {
	out := append([]byte(nil), src...)
	r := int(math.Round(s * 0.9))
	for c := 0; c < 3; c++ {
		boxBlur(out, w, h, 3, c, r)
	}
	return out
}

// featherGray softens a matte's edges (about s pixels), as the renderer's
// feather does: the matte times its blur, so the edge eases inward.
func featherGray(m []byte, w, h int, s float64) []byte {
	if s < 0.5 {
		return m
	}
	soft := append([]byte(nil), m...)
	boxBlur(soft, w, h, 1, 0, max(1, int(math.Round(s*0.9))))
	out := make([]byte, len(m))
	for i := range m {
		out[i] = uint8(math.Round(float64(m[i]) * float64(soft[i]) / 255))
	}
	return out
}

// previewKey names a preview by everything it shows.
func previewKey(in *CutoutPreviewIn, media, model string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%.2f|%s|%s|%.1f|%s|%s|%.2f|%.1f|%d", Version, media, model, in.At, in.With, in.Colour, in.Blur,
		in.Picture, in.Footage, in.FootageAt, in.Feather, in.Width)))
	return hex.EncodeToString(sum[:16])
}

// PreviewFile writes a preview still to a file (the CLI's; out "" a file of
// its own in this computer's scratch folder).
func PreviewFile(ctx context.Context, a *app.App, in *CutoutPreviewIn) (*CutoutPreviewOut, error) {
	b, info, err := Preview(ctx, a, in)
	if err != nil {
		return nil, err
	}
	out := strings.TrimSpace(in.Out)
	if out == "" {
		dir := filepath.Join(a.Cfg.Scratch.Dir, "cut-out-previews")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		out = filepath.Join(dir, previewKey(in, "", info.Model)+".jpg")
	}
	if !filepath.IsAbs(out) {
		if abs, err := filepath.Abs(out); err == nil {
			out = abs
		}
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't write the still.")
	}
	info.Path = out
	return info, nil
}
