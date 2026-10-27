package generate

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Pictures and clips sent with a generation are kept as exact bytes in
// MetaRush's storage (content addressed, like footage), so the record names
// exactly what was sent and a resumed job sends the same bytes again.

// stored is a file kept in the media store.
type stored struct {
	SHA256, Key, Path, MIME string
	Size                    int64
	Width, Height           int
	Seconds                 float64
}

// keep stores a local file by its content and returns where it is.
func keep(ctx context.Context, a *app.App, path, mime string) (*stored, error) {
	sha, err := media.HashFile(path)
	if err != nil {
		return nil, err
	}
	key := media.ObjectKey(a.WorkspaceID(), sha)
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info, err := a.Media.Stat(ctx, key); err != nil || info.Size != st.Size() {
		if _, err := a.Media.PutFile(ctx, key, path, media.PutOptions{ContentType: mime, SHA256: sha}, media.Resume{}, nil); err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't save a picture for the shot in its storage. Try again in a moment.")
		}
	}
	cached, err := a.Cache.Adopt(path, sha)
	if err != nil {
		cached = path
	}
	return &stored{SHA256: sha, Key: key, Path: cached, MIME: mime, Size: st.Size()}, nil
}

// local returns a stored file on this machine (from the cache, fetched when needed).
func local(ctx context.Context, a *app.App, key, sha string, size int64) (string, error) {
	return a.Cache.Ensure(ctx, a.Media, key, sha, size, nil)
}

func scratch(a *app.App, name string) (string, func(), error) {
	dir, err := os.MkdirTemp(a.Cfg.Scratch.Dir, "generate-")
	if err != nil {
		if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
			return "", nil, err
		}
		if dir, err = os.MkdirTemp(a.Cfg.Scratch.Dir, "generate-"); err != nil {
			return "", nil, err
		}
	}
	return filepath.Join(dir, name), func() { _ = os.RemoveAll(dir) }, nil
}

// loadVideo loads a clip that frames or a part can be taken from.
func loadVideo(ctx context.Context, a *app.App, assetID string) (*models.Asset, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return nil, apperr.New(apperr.NotFound, "That clip isn't in this workspace.").WithDetails(map[string]any{"assetId": assetID})
	}
	if !as.Media.HasVideo {
		return nil, apperr.New(apperr.UnsupportedMedia, "“%s” has no picture to take a frame from.", as.DisplayTitle())
	}
	if as.State != models.AssetAvailable {
		return nil, apperr.New(apperr.Conflict, "“%s” is still being added. Try again once it's ready.", as.DisplayTitle())
	}
	return &as, nil
}

func original(ctx context.Context, a *app.App, as *models.Asset) (string, error) {
	p, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
	if err != nil {
		return "", apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get “%s” from its storage.", as.DisplayTitle())
	}
	return p, nil
}

// still takes the frame at t of a clip as a JPEG (long side at most 1280
// pixels) and keeps it.
func still(ctx context.Context, a *app.App, as *models.Asset, t float64) (*stored, error) {
	if t < 0 || (as.Media.Duration > 0 && t > as.Media.Duration+0.01) {
		return nil, apperr.New(apperr.InvalidInput, "“%s” is only %.1f seconds long.", as.DisplayTitle(), as.Media.Duration)
	}
	t = math.Min(t, math.Max(0, as.Media.Duration-0.05))
	in, err := original(ctx, a, as)
	if err != nil {
		return nil, err
	}
	out, done, err := scratch(a, "frame.jpg")
	if err != nil {
		return nil, err
	}
	defer done()
	if err := (&derive.Deriver{T: a.Tools}).Frame(ctx, in, out, t, 1280); err != nil {
		return nil, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't take that frame from “%s”.", as.DisplayTitle())
	}
	s, err := keep(ctx, a, out, "image/jpeg")
	if err != nil {
		return nil, err
	}
	if mi, err := probe.Probe(ctx, a.Tools, s.Path); err == nil {
		s.Width, s.Height = mi.Width, mi.Height
	}
	return s, nil
}

// part cuts [from, to) of a clip into a small MP4 (H.264, at most 720p on
// the short side, sound kept unless silent) and keeps it.
func part(ctx context.Context, a *app.App, as *models.Asset, from, to float64, silent bool) (*stored, error) {
	if to <= from+0.2 {
		return nil, apperr.New(apperr.InvalidInput, "That part of “%s” is too short.", as.DisplayTitle())
	}
	if as.Media.Duration > 0 && to > as.Media.Duration+0.05 {
		to = as.Media.Duration
	}
	in, err := original(ctx, a, as)
	if err != nil {
		return nil, err
	}
	out, done, err := scratch(a, "part.mp4")
	if err != nil {
		return nil, err
	}
	defer done()
	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-ss", ff(from), "-i", in, "-t", ff(to - from), "-map", "0:v:0",
		"-vf", "scale='if(gt(iw,ih),-2,min(720,iw))':'if(gt(iw,ih),min(720,ih),-2)',format=yuv420p", "-c:v", "libx264", "-preset", "veryfast", "-crf", "20"}
	if silent || !as.Media.HasAudio {
		args = append(args, "-an")
	} else {
		args = append(args, "-map", "0:a:0?", "-c:a", "aac", "-b:a", "128k")
	}
	args = append(args, "-movflags", "+faststart", out)
	if _, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
		return nil, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't cut that part of “%s”.", as.DisplayTitle())
	}
	s, err := keep(ctx, a, out, "video/mp4")
	if err != nil {
		return nil, err
	}
	if mi, err := probe.Probe(ctx, a.Tools, s.Path); err == nil {
		s.Width, s.Height, s.Seconds = mi.Width, mi.Height, mi.Duration
	}
	return s, nil
}

func ff(v float64) string { return strconv.FormatFloat(math.Max(0, v), 'f', 3, 64) }

// picture keeps a resources-library picture as a JPEG (upright, long side at
// most 2048 pixels, see-through parts on white).
func picture(ctx context.Context, a *app.App, projectID, ref string) (*stored, *models.Resource, string, error) {
	return pictureOf(ctx, a, projectID, ref, false)
}

func pictureAsIs(ctx context.Context, a *app.App, projectID, ref string) (*stored, *models.Resource, string, error) {
	return pictureOf(ctx, a, projectID, ref, true)
}

// pictureOf keeps a resources-library picture: as a JPEG at most 2048
// pixels wide (what video models take), or as its own file.
func pictureOf(ctx context.Context, a *app.App, projectID, ref string, asIs bool) (*stored, *models.Resource, string, error) {
	name, version := ref, ""
	if i := lastAt(ref); i > 0 {
		name, version = ref[:i], ref[i+1:]
	}
	view, err := projectLinksView(ctx, a, projectID)
	if err != nil {
		return nil, nil, "", err
	}
	e, err := resources.Find(ctx, a, projectID, view, name)
	if err != nil {
		return nil, nil, "", err
	}
	r := &e.Resource
	if r.Kind != models.ResourceImage {
		return nil, nil, "", apperr.New(apperr.InvalidInput, "“%s” isn't a photo or picture file (PNG, JPEG or WebP), so it can't be shown to the video model.", r.Title)
	}
	sha := ""
	if version != "" {
		for _, v := range r.Versions {
			if len(version) >= 6 && len(v.SHA256) >= len(version) && v.SHA256[:len(version)] == version {
				sha = v.SHA256
			}
		}
		if sha == "" {
			return nil, nil, "", apperr.New(apperr.NotFound, "“%s” has no version %s.", r.Title, version)
		}
	}
	path, v, err := resources.LocalFile(ctx, a, r, sha, 0)
	if err != nil {
		return nil, nil, "", err
	}
	if asIs {
		s, err := keep(ctx, a, path, v.MIME)
		if err != nil {
			return nil, nil, "", err
		}
		return s, r, fmt.Sprintf("%s@%s", r.Name, v.SHA256[:12]), nil
	}
	jpg, err := resources.StillJPEG(path, 2048)
	if err != nil {
		return nil, nil, "", apperr.Wrap(apperr.UnsupportedMedia, err, "MetaRush couldn't read the picture “%s”.", r.Title)
	}
	out, done, err := scratch(a, "picture.jpg")
	if err != nil {
		return nil, nil, "", err
	}
	defer done()
	if err := os.WriteFile(out, jpg, 0o600); err != nil {
		return nil, nil, "", err
	}
	s, err := keep(ctx, a, out, "image/jpeg")
	if err != nil {
		return nil, nil, "", err
	}
	return s, r, fmt.Sprintf("%s@%s", r.Name, v.SHA256[:12]), nil
}

func projectLinksView(ctx context.Context, a *app.App, projectID string) (*projectlinks.View, error) {
	return projectlinks.Load(ctx, a, projectID)
}

func lastAt(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '@' {
			return i
		}
	}
	return -1
}
