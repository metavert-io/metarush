package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	_ "image/jpeg" // decoders for uploaded pictures
	"image/png"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Avatars (D184): a person's picture, kept as a 256-pixel square PNG in the
// media store by its SHA-256; the original upload is never kept.
const (
	avatarSize     = 256
	avatarMaxBytes = 5 << 20
)

// AvatarIn sets (data) or removes (remove) your picture.
type AvatarIn struct {
	Data   string `json:"data,omitempty" jsonschema:"description=The picture (PNG\\, JPEG or WebP)\\, base64-encoded\\, up to 5 MB."`
	Remove bool   `json:"remove,omitempty"`
}

// AvatarOut is your account after the change.
type AvatarOut struct {
	Account Account `json:"person"`
	Summary string  `json:"summary"`
}

func init() {
	ops.Def("account.avatar", "Set or remove your own picture (PNG, JPEG or WebP up to 5 MB; kept as a small square)", ops.Flags{Mutating: true, HumanOnly: true,
		MaxInput: int64(base64.StdEncoding.EncodedLen(avatarMaxBytes)) + 4096}, avatarOp)
}

// squareAvatar checks a picture and makes the stored square of it: the
// middle of the picture, scaled to avatarSize.
func squareAvatar(b []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || (format != "png" && format != "jpeg" && format != "webp") {
		return nil, apperr.New(apperr.InvalidInput, "That isn't a picture MetaRush can use. Choose a PNG, JPEG or WebP file.")
	}
	if cfg.Width < 32 || cfg.Height < 32 || cfg.Width*cfg.Height > 40_000_000 {
		return nil, apperr.New(apperr.InvalidInput, "Choose a picture at least 32 pixels on each side (and not a giant one).")
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush couldn't read that picture. Try another file.")
	}
	r := img.Bounds()
	side := min(r.Dx(), r.Dy())
	x0, y0 := r.Min.X+(r.Dx()-side)/2, r.Min.Y+(r.Dy()-side)/2
	out := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	draw.CatmullRom.Scale(out, out.Bounds(), img, image.Rect(x0, y0, x0+side, y0+side), draw.Src, nil)
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SetAvatar stores a person's picture (nil data removes it).
func SetAvatar(ctx context.Context, a *app.App, userID string, data []byte) (*models.User, error) {
	defer forget(userID)
	if userID == a.Cfg.User.ID {
		if err := ensureOwnerRecord(ctx, a); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	if data == nil {
		if err := a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), userID, bson.M{"$unset": bson.M{"avatar": ""}, "$set": bson.M{"updatedAt": now}}); err != nil {
			return nil, err
		}
		a.Audit(ctx, "account.avatar", "user", userID, "", map[string]any{"removed": true})
		return GetUser(ctx, a, userID)
	}
	png, err := squareAvatar(data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(png)
	sha := hex.EncodeToString(sum[:])
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if info, err := a.Media.Stat(ctx, key); err != nil || info.Size != int64(len(png)) {
		tmp, err := os.CreateTemp(a.Cfg.Scratch.Dir, "avatar-*.png")
		if err != nil {
			return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't keep your picture.")
		}
		defer os.Remove(tmp.Name())
		_, werr := tmp.Write(png)
		tmp.Close()
		if werr != nil {
			return nil, apperr.Wrap(apperr.Internal, werr, "MetaRush couldn't keep your picture.")
		}
		if _, err := a.Media.PutFile(ctx, key, tmp.Name(), media.PutOptions{ContentType: "image/png", SHA256: sha}, media.Resume{}, nil); err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't store your picture.")
		}
	}
	av := models.Avatar{SHA256: sha, Size: int64(len(png)), UpdatedAt: now}
	if err := a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), userID, bson.M{"$set": bson.M{"avatar": av, "updatedAt": now}}); err != nil {
		return nil, err
	}
	a.Audit(ctx, "account.avatar", "user", userID, "", nil)
	return GetUser(ctx, a, userID)
}

func avatarOp(ctx context.Context, a *app.App, in *AvatarIn) (*AvatarOut, error) {
	var data []byte
	if !in.Remove {
		if strings.TrimSpace(in.Data) == "" {
			return nil, apperr.New(apperr.InvalidInput, "Choose a picture, or remove the one you have.")
		}
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.Data))
		if err != nil || len(b) == 0 {
			return nil, apperr.New(apperr.InvalidInput, "MetaRush couldn't read that picture. Try again.")
		}
		if len(b) > avatarMaxBytes {
			return nil, apperr.New(apperr.InvalidInput, "That picture is larger than 5 MB; choose a smaller one.")
		}
		data = b
	}
	u, err := SetAvatar(ctx, a, you(a), data)
	if err != nil {
		return nil, err
	}
	msg := "Your picture is set."
	if in.Remove {
		msg = "Your picture is removed; your initials show instead."
	}
	return &AvatarOut{Account: personOf(ctx, a, u, false), Summary: msg}, nil
}

// AvatarURL is where the dashboard shows a person's picture ("" when they have none).
func AvatarURL(u *models.User) string {
	if u == nil || u.Avatar == nil || u.Avatar.SHA256 == "" {
		return ""
	}
	return "/media/avatars/" + u.ID + "?v=" + u.Avatar.SHA256[:12]
}
