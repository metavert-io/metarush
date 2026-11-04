package resources

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Pictures taken from a clip say so inside the file (D457): a still frame or a
// contact sheet MetaRush writes (footage frames, footage sheet) carries a JPEG
// comment naming the workspace, the clip and the time, and AddFile reads it
// into the version's provenance (Source.FrameOf). A picture taken from a
// reference — someone else's video, kept to learn from — is then treated like
// the reference itself wherever it would be shown or sent: the cut check, the
// ship gate, AI work and generated video, abilities, Remotion pictures and
// Recipes' files. Whether the clip is a reference is read when it matters, so
// marking a clip later, or saying it's yours, counts for the pictures too.
// Provenance travels with the file, so no list of hashes is kept; a picture
// edited outside MetaRush and saved again loses it, like any other comment.

// frameMarkPrefix starts the comment; the rest is JSON.
const frameMarkPrefix = "metarush-frame/1 "

type frameMark struct {
	Workspace string  `json:"ws"`
	Asset     string  `json:"asset"`
	At        float64 `json:"at"`
}

// MarkFrame writes where a picture was taken from into a JPEG MetaRush made of
// a clip (a comment after the file's header segments).
func MarkFrame(path, workspaceID, assetID string, at float64) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return errors.New("not a JPEG")
	}
	payload, _ := json.Marshal(frameMark{Workspace: workspaceID, Asset: assetID, At: at})
	payload = append([]byte(frameMarkPrefix), payload...)
	// After SOI and the APPn segments (JFIF and Exif stay first).
	pos := 2
	for pos+4 <= len(b) && b[pos] == 0xFF && b[pos+1] >= 0xE0 && b[pos+1] <= 0xEF {
		n := int(binary.BigEndian.Uint16(b[pos+2 : pos+4]))
		if n < 2 || pos+2+n > len(b) {
			return errors.New("a damaged JPEG header")
		}
		pos += 2 + n
	}
	seg := make([]byte, 4, 4+len(payload))
	seg[0], seg[1] = 0xFF, 0xFE
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	out := make([]byte, 0, len(b)+len(seg))
	out = append(append(append(out, b[:pos]...), seg...), b[pos:]...)
	tmp := path + ".marking"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readFrameMark reads the comment MarkFrame wrote (nil when there is none):
// only the header segments are read, never the picture itself.
func readFrameMark(path string) *frameMark {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	r := bufio.NewReader(io.LimitReader(f, 1<<20))
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil || soi[0] != 0xFF || soi[1] != 0xD8 {
		return nil
	}
	for {
		m, err := r.ReadByte()
		if err != nil || m != 0xFF {
			return nil
		}
		kind, err := r.ReadByte()
		for err == nil && kind == 0xFF { // fill bytes
			kind, err = r.ReadByte()
		}
		if err != nil || kind == 0xDA || kind == 0xD9 { // the picture starts, or the file ends
			return nil
		}
		if kind == 0x01 || (kind >= 0xD0 && kind <= 0xD7) {
			continue // markers without a length
		}
		var l [2]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return nil
		}
		n := int(binary.BigEndian.Uint16(l[:])) - 2
		if n < 0 {
			return nil
		}
		if kind != 0xFE || n < len(frameMarkPrefix) || n > 4096 {
			if _, err := r.Discard(n); err != nil {
				return nil
			}
			continue
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil
		}
		if !bytes.HasPrefix(body, []byte(frameMarkPrefix)) {
			continue
		}
		var fm frameMark
		if json.Unmarshal(body[len(frameMarkPrefix):], &fm) != nil || fm.Asset == "" {
			return nil
		}
		return &fm
	}
}

// frameOf is the provenance a marked file gives in this workspace: the clip
// and its title, when the file was taken here (nil otherwise).
func frameOf(ctx context.Context, a *app.App, path string) *models.ResourceFrame {
	fm := readFrameMark(path)
	if fm == nil || fm.Workspace != a.WorkspaceID() {
		return nil
	}
	f := &models.ResourceFrame{AssetID: fm.Asset, At: fm.At}
	var as models.Asset
	if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), fm.Asset, &as) == nil {
		f.Title = as.DisplayTitle()
	}
	return f
}

// FrameReference is the reference a version of a picture was taken from (a
// still frame of someone else's video, D457), nil when it wasn't. sha "" is
// the version new uses get; the start of a SHA-256 finds its version.
func FrameReference(ctx context.Context, a *app.App, r *models.Resource, sha string) *models.Asset {
	if r == nil {
		return nil
	}
	f := r.FrameOf(sha)
	if f == nil || f.AssetID == "" {
		return nil
	}
	var as models.Asset
	if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), f.AssetID, &as) != nil || !as.IsReference() {
		return nil
	}
	return &as
}

// FrameReferenceWords says a picture taken from a reference plainly ("“Hook
// still” is a frame of “Their video”, a reference: someone else's video, kept
// to learn from").
func FrameReferenceWords(r *models.Resource, ref *models.Asset) string {
	return fmt.Sprintf("“%s” is a frame of “%s”, a reference: someone else's video, kept to learn from", pictureTitle(r), strings.TrimSpace(ref.DisplayTitle()))
}

// RefuseFrameOfReference refuses to show, send or make anything from a
// picture taken from a reference (D457), the way a reference itself is
// refused (D441): what to do is in the words, the command behind Details.
func RefuseFrameOfReference(ctx context.Context, a *app.App, r *models.Resource, sha, doing string) error {
	ref := FrameReference(ctx, a, r, sha)
	if ref == nil {
		return nil
	}
	return apperr.New(apperr.Conflict, "%s, so MetaRush doesn't %s. If it's your own video, say so on its page (This is mine) and try again.", FrameReferenceWords(r, ref), doing).
		WithCommand(ref.ReferenceMineCommand()).
		WithDetails(map[string]any{"reason": "reference", "assetId": ref.ID, "resourceId": r.ID})
}

func pictureTitle(r *models.Resource) string {
	if t := strings.TrimSpace(r.Title); t != "" {
		return t
	}
	return r.Name
}
