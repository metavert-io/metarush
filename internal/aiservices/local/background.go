package local

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/videogen"
	"github.com/metavert-io/metarush/internal/vision"
)

// A still's background taken away on this computer (D279): Apple Vision's
// subject lifting through MetaRush's own helper (the same mask as the
// pictures fix "Remove the background", D152), on a Mac with macOS 14 or
// later. Nothing is downloaded: the helper is built from MetaRush's source.

// ModelSubject is the model's ID.
const ModelSubject = "apple-vision"

// Masker cuts the subject of a picture out into out (a PNG whose background
// is see-through); found is false when it sees no subject. Tests stand one in.
var Masker = func(ctx context.Context, e aiservices.Env, in, out string) (bool, error) {
	if _, _, err := vision.Ensure(ctx, e.Tools, e.Home); err != nil {
		return false, err
	}
	return vision.Mask(ctx, e.Tools, in, out)
}

// MaskerReady says whether the helper can run here (tests stand one in).
var MaskerReady = func(e aiservices.Env) error {
	if runtime.GOOS != "darwin" {
		return apperr.New(apperr.NotConfigured, "Taking a background away on this computer needs a Mac (macOS 14 or later).")
	}
	if e.Tools == nil || !vision.CanRun(e.Tools) {
		return apperr.New(apperr.NotConfigured, "The on-screen helper isn't set up on this Mac, so it can't take the background away here.").
			WithHint("Add “Seeing what's on screen” in Settings → Components.").WithCommand("metarush components add screen-index")
	}
	return nil
}

type background struct{ base }

func init() { aiservices.Register(&background{base{aiservices.KindBackground}}) }

// Ready says whether the subject mask can run here.
func (a *background) Ready(e aiservices.Env, _ string) error { return MaskerReady(e) }

// Submit takes the picture's background away.
func (a *background) Submit(ctx context.Context, _ string, req *aiservices.Request) (*aiservices.Ticket, error) {
	in := req.Input(aiservices.RolePicture)
	if in == nil {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "There's no picture to take the background from."}
	}
	e, done, err := env(ctx, "a picture without its background")
	if err != nil {
		return nil, err
	}
	defer done()
	if err := MaskerReady(e); err != nil {
		return nil, notHere(err)
	}
	src, err := filepath.Abs(in.Path)
	if err != nil {
		return nil, err
	}
	out := filepath.Join(e.Scratch, "cut-out.png")
	found, err := Masker(ctx, e, src, out)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "No subject was found in the picture, so there's no background to take away.",
			Hint: "Try a picture with a clear subject: a person, an animal or a thing in front."}
	}
	b, err := os.ReadFile(out)
	if err != nil || aiservices.Sniff(b) != "image/png" {
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "The helper made no picture."}
	}
	st := &aiservices.Status{State: aiservices.StateDone, Outputs: []aiservices.Output{{MIME: "image/png", Data: b}}, Usage: aiservices.Usage{Known: true}}
	return ready(st), nil
}
