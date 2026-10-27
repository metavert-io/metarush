package motion

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/store"
)

// Versions that travel in a file another workspace exported (D219): a motion
// graphic (or a shared library) is its canonical archive, so it travels as
// bytes and comes in as the same version (the same hash). What comes in is
// kept, never run: it has no record here until it is checked in this
// workspace (motion check --version name@hash: the import scan, type check,
// bundle and test frames in the jailed browser, like any graphic), and it
// renders only once a person here approved it. A version this workspace has
// already checked keeps what people here decided.

// Keep stores an archive a file brought, after checking it is canonical and
// the version it says it is. known reports a version this workspace has
// already checked.
func Keep(ctx context.Context, a *app.App, b []byte, want string) (arc *Archive, known bool, err error) {
	arc, err = Parse(b)
	if err != nil {
		return nil, false, apperr.Wrap(apperr.Integrity, err, "A motion graphic in the file isn't one MetaRush can read.")
	}
	if arc.Hash != want {
		return nil, false, apperr.New(apperr.Integrity, "A motion graphic in the file isn't the version it says it is.")
	}
	if _, err := Get(ctx, a.DB, a.WorkspaceID(), want); err == nil {
		return arc, true, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}
	key := media.ObjectKey(a.WorkspaceID(), want)
	if info, err := a.Media.Stat(ctx, key); err == nil && info.Size == int64(len(b)) {
		return arc, false, nil
	}
	if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
		return nil, false, err
	}
	tmp, err := os.CreateTemp(a.Cfg.Scratch.Dir, "motion-archive-")
	if err != nil {
		return nil, false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return nil, false, err
	}
	if err := tmp.Close(); err != nil {
		return nil, false, err
	}
	if _, err := a.Media.PutFile(ctx, key, tmp.Name(), media.PutOptions{ContentType: "application/json", SHA256: want}, media.Resume{}, nil); err != nil {
		return nil, false, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't keep a motion graphic in its storage.")
	}
	return arc, false, nil
}

// Kept reads a version kept by Keep (or checked here) back from the media
// store and unpacks it into a new folder named after the graphic under dir,
// for its check. It returns that folder.
func Kept(ctx context.Context, a *app.App, name, hash, dir string) (string, error) {
	if !ValidName(name) || !ValidHash(hash) {
		return "", apperr.New(apperr.InvalidInput, "Say which version: name@version, with the whole version.")
	}
	key := media.ObjectKey(a.WorkspaceID(), hash)
	info, err := a.Media.Stat(ctx, key)
	if err != nil {
		return "", apperr.New(apperr.NotFound, "MetaRush doesn't keep that version of %s.", name).WithHint("It comes with a file from another workspace (or a check of its folder).")
	}
	p, err := a.Cache.Ensure(ctx, a.Media, key, hash, info.Size, nil)
	if err != nil {
		return "", apperr.Wrap(apperr.Storage, err, "fetch the motion graphic %s", name)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	arc, err := Parse(b)
	if err != nil || !bytes.Equal(arc.Bytes, b) || arc.Hash != hash {
		return "", apperr.New(apperr.Integrity, "The kept version of %s isn't what it should be.", name)
	}
	out := filepath.Join(dir, name)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	if err := arc.Extract(out); err != nil {
		return "", err
	}
	return out, nil
}
