package browserext

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/metavert-io/metarush/extension"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/ops"
)

// ExtensionStatusIn has no options.
type ExtensionStatusIn struct{}

// ExtensionStatusOut is the extension's state on this machine.
type ExtensionStatusOut struct {
	// Folder is where this machine keeps the extension for "Load unpacked"
	// (refreshed from the copy built into MetaRush).
	Folder  string `json:"folder"`
	Version string `json:"version"`
	// Address is what the extension connects to on this machine.
	Address string `json:"address"`
	// Browsers are the asking person's own paired browsers ("Your browsers", D197).
	Browsers []Browser `json:"browsers"`
	// Everyone is every paired browser with whose it is ("Everyone's
	// browsers"): for owners, who may unpair any of them; empty for members.
	Everyone []PairedBrowser `json:"everyone"`
	// Owner: the asking person may unpair anyone's browser.
	Owner bool `json:"owner"`
	// CodeWaiting: a pairing code the asking person made is not used yet (until CodeExpiresAt).
	CodeWaiting   bool       `json:"codeWaiting"`
	CodeExpiresAt *time.Time `json:"codeExpiresAt,omitempty"`
}

// PairedBrowser is a paired browser with whose it is, as an owner sees it.
type PairedBrowser struct {
	Browser
	// Person is the name of the person who paired it, as it is now.
	Person string `json:"person"`
	// Mine: the asking person paired it.
	Mine bool `json:"mine,omitempty"`
	// Stopped says why it can't send anything now (its person's account is
	// turned off or removed), in plain words; empty while it works.
	Stopped string `json:"stopped,omitempty"`
}

// ExtensionPairIn has no options.
type ExtensionPairIn struct{}

// ExtensionPairOut is a one-time pairing code.
type ExtensionPairOut struct {
	Code      string    `json:"code" jsonschema:"description=Type this into the extension (works once\\, for 10 minutes)."`
	ExpiresAt time.Time `json:"expiresAt"`
	Address   string    `json:"address"`
	Steps     []string  `json:"steps"`
	// For is the person the browser will be paired for (the one asking).
	For string `json:"for"`
}

// ExtensionRevokeIn unpairs one browser, all of yours, or (owners) everyone's.
type ExtensionRevokeIn struct {
	ID string `json:"id,omitempty" jsonschema:"description=The paired browser (ext_…): one of yours\\, or (owners) anyone's."`
	// All unpairs every browser the asking person paired.
	All bool `json:"all,omitempty" jsonschema:"description=Every browser you paired."`
	// Everyone unpairs every browser anyone paired (owners).
	Everyone bool `json:"everyone,omitempty" jsonschema:"description=Every browser anyone paired (owners only)."`
}

// ExtensionRevokeOut says how many were unpaired.
type ExtensionRevokeOut struct {
	Revoked  int             `json:"revoked"`
	Browsers []Browser       `json:"browsers"`
	Everyone []PairedBrowser `json:"everyone"`
}

// Of is this installation's pairings, with pairings from before people had
// pairings belonging to the person it was set up for (D197).
func Of(a *app.App) *Store {
	return For(a.Cfg.StateDir()).Owned(Person{ID: a.Cfg.User.ID, Name: a.Cfg.User.Name})
}

// everyone lists every paired browser with whose it is and whether it works.
func everyone(ctx context.Context, a *app.App, st *Store, me string) []PairedBrowser {
	out := []PairedBrowser{}
	for _, b := range st.List() {
		name, reason, ok := auth.MaySend(ctx, a, b.UserID)
		pb := PairedBrowser{Browser: b, Person: firstNonEmpty(name, b.UserName, "someone"), Mine: b.UserID == me}
		if !ok {
			pb.Stopped = auth.SenderWords(reason, pb.Person)
		}
		out = append(out, pb)
	}
	return out
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// Install writes the built-in extension to <home>/browser-extension (only files
// that changed), so the folder a browser loaded stays current after an update.
func Install(home string) (string, string, error) {
	dir := filepath.Join(home, "browser-extension")
	src := extension.FS()
	err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, b) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		return "", "", err
	}
	return dir, manifestVersion(), nil
}

func manifestVersion() string {
	b, err := fs.ReadFile(extension.FS(), "manifest.json")
	if err != nil {
		return ""
	}
	var m struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(b, &m)
	return m.Version
}

// address is what the extension connects to: this computer's own address,
// or the public one (remote access, D128) for a browser on another computer.
func address(ctx context.Context, a *app.App) string {
	if u := a.Cfg.Remote().PublicURL; u != "" && ops.CallerFrom(ctx).Elsewhere {
		return u
	}
	return "http://127.0.0.1:" + strconv.Itoa(a.Cfg.Dashboard.Port)
}

func status(ctx context.Context, a *app.App) (*ExtensionStatusOut, error) {
	dir, v, err := Install(a.Cfg.Home())
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't write the browser extension's folder.")
	}
	st := Of(a)
	me := a.Actor.UserID
	out := &ExtensionStatusOut{Folder: dir, Version: v, Address: address(ctx, a), Browsers: st.ListFor(me), Everyone: []PairedBrowser{}, Owner: ops.IsOwnerCaller(ctx)}
	if out.Owner {
		out.Everyone = everyone(ctx, a, st, st.whose(me))
	}
	if ok, exp := st.Pending(time.Now(), me); ok {
		out.CodeWaiting, out.CodeExpiresAt = true, &exp
	}
	return out, nil
}

func init() {
	ops.Def("extension.status", "The \"Send to MetaRush\" browser extension on this machine: its folder (for Load unpacked), your paired browsers (owners: everyone's), whether your pairing code is waiting", ops.Flags{}, func(ctx context.Context, a *app.App, _ *ExtensionStatusIn) (*ExtensionStatusOut, error) {
		return status(ctx, a)
	})
	ops.Def("extension.pair", "Make a one-time code (10 minutes) that pairs your browser's MetaRush extension with this machine: what it sends is yours; it can only send videos and pages, never read anything; people only", ops.Flags{Mutating: true, HumanOnly: true}, func(ctx context.Context, a *app.App, _ *ExtensionPairIn) (*ExtensionPairOut, error) {
		who := Person{ID: a.Actor.UserID, Name: a.Actor.UserName}
		code, exp, err := Of(a).NewCode(time.Now(), who)
		if err != nil {
			return nil, err
		}
		a.Log.Secret(code) // the code never reaches the activity log
		a.Audit(ctx, "extension.pair", "installation", a.Cfg.Installation.ID, "", map[string]any{"expiresAt": exp, "for": who.ID})
		_, _, _ = Install(a.Cfg.Home()) // the folder the steps point at is there and current
		install := "Not installed yet? Open chrome://extensions, turn on Developer mode, choose Load unpacked and pick the extension's folder (Settings → Browser extension shows it, with a Copy button)."
		if ops.CallerFrom(ctx).Elsewhere {
			// A browser on another computer: the folder is on the computer that runs MetaRush.
			install = "Not installed yet? Download the extension (Settings → Browser extension), unzip it, open chrome://extensions, turn on Developer mode, choose Load unpacked and pick the unzipped folder. In the extension, use this address: " + address(ctx, a) + "."
		}
		return &ExtensionPairOut{Code: code, ExpiresAt: exp, Address: address(ctx, a), For: firstNonEmpty(who.Name, "you"), Steps: []string{
			"In Chrome (or Brave, Edge), open the extension's popup: “Send to MetaRush” in the toolbar.",
			"Type this code and choose Pair. It works once, for 10 minutes; what the browser sends is yours.",
			install,
		}}, nil
	})
	ops.Def("extension.revoke", "Unpair a browser's MetaRush extension: one of yours, all of yours, or (owners) anyone's; its token stops working at once", ops.Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *ExtensionRevokeIn) (*ExtensionRevokeOut, error) {
		n := 0
		for _, set := range []bool{in.ID != "", in.All, in.Everyone} {
			if set {
				n++
			}
		}
		if n != 1 {
			return nil, apperr.New(apperr.InvalidInput, "Say which browser to unpair, all of yours, or everyone's.")
		}
		st := Of(a)
		me := st.whose(a.Actor.UserID)
		var revoked int
		var err error
		switch {
		case in.ID != "":
			b, ok := st.Get(in.ID)
			if !ok {
				return nil, apperr.New(apperr.NotFound, "That browser isn't paired with this machine.").WithDetails(map[string]any{"browser": in.ID})
			}
			if b.UserID != me {
				// Someone else's browser: an owner's to unpair (D197).
				if err := ops.RequireOwner(ctx, a, "extension.revoke"); err != nil {
					return nil, err
				}
			}
			revoked, err = st.Revoke(in.ID)
		case in.All:
			revoked, err = st.RevokeFor(me)
		default:
			if err := ops.RequireOwner(ctx, a, "extension.revoke"); err != nil {
				return nil, err
			}
			revoked, err = st.Revoke("")
		}
		if err != nil {
			return nil, err
		}
		a.Audit(ctx, "extension.revoke", "installation", a.Cfg.Installation.ID, "", map[string]any{"id": in.ID, "all": in.All, "everyone": in.Everyone, "revoked": revoked})
		out := &ExtensionRevokeOut{Revoked: revoked, Browsers: st.ListFor(me), Everyone: []PairedBrowser{}}
		if ops.IsOwnerCaller(ctx) {
			out.Everyone = everyone(ctx, a, st, me)
		}
		return out, nil
	})
}
