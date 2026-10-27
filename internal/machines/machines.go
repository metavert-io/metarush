package machines

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/store"
)

// LabelPrefix starts every key and database user MetaRush creates.
const LabelPrefix = "metarush-"

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// ValidName checks a machine name (lowercase letters, digits, dashes; ≤ 40).
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return apperr.New(apperr.InvalidInput, "“%s” can't be a machine name: use 1 to 40 lowercase letters, digits or dashes (for example studio-mini).", name)
	}
	return nil
}

// Label is the key / database user name for a machine.
func Label(name string) string { return LabelPrefix + name }

// InviteOptions configures `machine invite`.
type InviteOptions struct {
	Name string
	Out  string // bundle path (default ~/.metarush-bundles/metarush-join-<name>.json)
	// AllowUnsafeSecretPath permits a bundle path inside a repository that ignores
	// it (--allow-unsafe-secret-path). Sync folders are always refused.
	AllowUnsafeSecretPath bool
	KeyScope              string // auto | prefix | bucket
	Role                  string // bucket scope role
	Atlas                 bool
	AtlasProfile          string
	AtlasProjectID        string
	AccessKeyID           string // a key a person created by hand (manual providers)
	ManualSecret          string // its secret (from the environment, never argv)
}

// MachineInvite is the result of an invite. It never contains secrets.
type MachineInvite struct {
	InstallationID string   `json:"installationId"`
	Name           string   `json:"name"`
	Bundle         string   `json:"bundle"`
	KeyProvider    string   `json:"keyProvider"`
	KeyLabel       string   `json:"keyLabel,omitempty"`
	KeyID          string   `json:"keyId,omitempty"` // masked
	KeyScope       string   `json:"keyScope,omitempty"`
	KeyRole        string   `json:"keyRole,omitempty"`
	KeyVerified    bool     `json:"keyVerified"`
	DBUser         string   `json:"dbUser,omitempty"`
	DBUserCreated  bool     `json:"dbUserCreated"`
	ManualSteps    []string `json:"manualSteps,omitempty"`
	NextSteps      []string `json:"nextSteps"`
	Warnings       []string `json:"warnings,omitempty"`
}

// Invite mints this machine's credentials and writes the join bundle. On any
// failure everything it created (key, policy, database user, file) is removed.
func Invite(ctx context.Context, a *app.App, o InviteOptions) (res *MachineInvite, err error) {
	cfg := a.Cfg
	name := strings.ToLower(strings.TrimSpace(o.Name))
	if err := ValidName(name); err != nil {
		return nil, err
	}
	label := Label(name)
	var existing []models.Installation
	if err := a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), bson.M{"name": name, "state": bson.M{"$ne": models.InstallationRevoked}}, &existing); err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, apperr.New(apperr.Conflict, "A machine named “%s” is already in this workspace.", name).
			WithHint("Pick another name, or revoke that machine first.").
			WithDetails(map[string]any{"installationId": existing[0].ID}).WithCommand("metarush machine revoke " + name)
	}
	out := o.Out
	if out == "" {
		dir, derr := secretpath.DefaultDir()
		if derr != nil {
			return nil, derr
		}
		out = filepath.Join(dir, "metarush-join-"+name+".json")
	}
	if out, err = filepath.Abs(out); err != nil {
		return nil, err
	}
	// Bundles hold credentials: never inside a sync folder or where git would commit them.
	if cerr := secretpath.Check(out, "the join file", o.AllowUnsafeSecretPath); cerr != nil {
		return nil, cerr
	}
	if _, err := os.Stat(out); err == nil {
		return nil, apperr.New(apperr.Conflict, "There is already a join file at %s, and MetaRush won't overwrite it.", shortPath(out)).
			WithHint("Move or delete that file, or choose another place with --out.").WithDetails(map[string]any{"path": out})
	}
	now := time.Now().UTC()
	insID := ids.New(ids.Installation)
	res = &MachineInvite{InstallationID: insID, Name: name, Bundle: out, KeyProvider: ProviderName(cfg)}
	b := &Bundle{Kind: BundleKind, Version: 1, CreatedAt: now, InvitedBy: a.Actor, Workspace: cfg.Workspace,
		Installation: config.Ref{ID: insID, Name: name},
		Database: BundleDB{Name: cfg.Database.Name, Host: config.HostOf(cfg.Database.URI), AtlasOrgID: cfg.Database.AtlasOrgID,
			AtlasOrgName: cfg.Database.AtlasOrgName, AtlasProjectID: cfg.Database.AtlasProjectID}}
	var undo []func()
	defer func() {
		if err != nil {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
			res = nil
		}
	}()
	bg := context.WithoutCancel(ctx)

	// Storage key.
	var ref *models.StorageKeyRef
	switch cfg.Media.Provider {
	case "s3":
		s := cfg.Media.S3
		bs := &BundleS3{Preset: s.Preset, Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, Prefix: s.Prefix, ForcePathStyle: s.ForcePathStyle, KeyLabel: label}
		if o.AccessKeyID != "" {
			if o.ManualSecret == "" {
				return nil, apperr.New(apperr.InvalidInput, "--access-key-id also needs the key's secret, in %s.", EnvManualSecret).
					WithHint("Type it without it showing (read -s " + EnvManualSecret + "; export " + EnvManualSecret + "), then run the invite again.")
			}
			bs.AccessKeyID, bs.SecretAccessKey = o.AccessKeyID, o.ManualSecret
			ref = &models.StorageKeyRef{Provider: "manual", KeyID: o.AccessKeyID, Label: label, Bucket: s.Bucket, Prefix: strings.Trim(s.Prefix, "/"), Scope: "manual", CreatedAt: now}
			res.KeyProvider = "manual"
			res.ManualSteps = append(res.ManualSteps, "This key was made by hand: revoking the machine later won't delete it, so delete the key in your storage provider's console then.")
		} else {
			p, perr := For(cfg, label)
			if m, ok := IsManual(perr); ok {
				return nil, apperr.New(apperr.NotConfigured, "%s", m.Error()).WithHint("Make the key by hand as the steps say (add --details to see them), then run the invite again with --access-key-id.").
					WithDetails(map[string]any{"provider": m.Provider, "steps": m.Steps})
			} else if perr != nil {
				return nil, perr
			}
			scope := o.KeyScope
			if scope == "auto" {
				scope = ""
			}
			m, merr := p.Mint(ctx, MintRequest{Label: label, Bucket: s.Bucket, Prefix: s.Prefix, Scope: scope, Role: o.Role})
			if merr != nil {
				return nil, apperr.Wrap(apperr.Storage, merr, "MetaRush couldn't make a storage key for “%s”.", name)
			}
			undo = append(undo, func() { _ = p.Revoke(bg, m.Ref) })
			ref = &m.Ref
			bs.AccessKeyID, bs.SecretAccessKey = m.Ref.KeyID, m.Secret
		}
		res.KeyLabel, res.KeyID, res.KeyScope, res.KeyRole = label, MaskKeyID(ref.KeyID), ref.Scope, ref.Role
		if verr := VerifyKey(ctx, s, bs.AccessKeyID, bs.SecretAccessKey); verr != nil {
			return nil, apperr.Wrap(apperr.Storage, verr, "The new storage key for “%s” didn't work when MetaRush tried it, so nothing was kept.", name)
		}
		res.KeyVerified = true
		b.Media = BundleMedia{Provider: "s3", S3: bs}
	case "filesystem":
		if !cfg.Media.Filesystem.Shared {
			return nil, apperr.New(apperr.NotConfigured, "This workspace keeps its footage on this machine's disk, which other machines can't reach.").
				WithHint("Move MetaRush's storage to a shared place first: an S3-compatible bucket or a shared drive.").WithDetails(map[string]any{"steps": filesystemSteps(cfg)})
		}
		b.Media = BundleMedia{Provider: "filesystem", Filesystem: cfg.Media.Filesystem}
		res.ManualSteps = append(res.ManualSteps, filesystemSteps(cfg)...)
	default:
		return nil, apperr.New(apperr.NotConfigured, "MetaRush doesn't know the storage “%s” that config.yaml names.", cfg.Media.Provider)
	}

	// Database user.
	host := config.HostOf(cfg.Database.URI)
	isAtlas := strings.Contains(host, ".mongodb.net")
	at := &Atlas{Tools: a.Tools, Profile: o.AtlasProfile, ProjectID: firstNonEmpty(o.AtlasProjectID, cfg.Database.AtlasProjectID)}
	cluster := ""
	switch {
	case o.Atlas && !isAtlas:
		res.Warnings = append(res.Warnings, "--atlas did nothing: the database isn't on MongoDB Atlas. Give the new machine its own database user (readWrite on "+cfg.Database.Name+") yourself.")
	case o.Atlas:
		if rerr := at.Ready(ctx); rerr != nil {
			res.Warnings = append(res.Warnings, "No database user was made: "+rerr.Error())
			res.ManualSteps = append(res.ManualSteps, "Create the machine's database user: "+at.CreateCommand(label, cfg.Database.Name, ""))
			break
		}
		if c, cerr := at.ClusterForHost(ctx, host); cerr == nil {
			cluster = c
		} else {
			res.Warnings = append(res.Warnings, "The database user isn't limited to one cluster: "+cerr.Error())
		}
		exists, xerr := at.UserExists(ctx, label)
		if xerr != nil {
			return nil, apperr.Wrap(apperr.Database, xerr, "MetaRush couldn't check whether the database user %s already exists.", label)
		}
		if exists {
			return nil, apperr.New(apperr.Conflict, "An Atlas database user named %s already exists.", label).
				WithHint("Pick another machine name, or delete that database user first.").WithCommand(at.DeleteCommand(label))
		}
		pw, cerr := at.CreateUser(ctx, label, cfg.Database.Name, cluster, "MetaRush installation "+name+" ("+insID+"), workspace "+cfg.Workspace.ID)
		if cerr != nil {
			return nil, apperr.Wrap(apperr.Database, cerr, "MetaRush couldn't create the Atlas database user %s.", label)
		}
		undo = append(undo, func() { _ = at.DeleteUser(bg, label) })
		uri, uerr := WithUser(cfg.Database.URI, label, pw)
		if uerr != nil {
			return nil, uerr
		}
		b.Database.URI, b.Database.User = uri, label
		res.DBUser, res.DBUserCreated = label, true
	case isAtlas:
		res.ManualSteps = append(res.ManualSteps, "No database user was made (add --atlas to make one). Give the machine its own user ("+at.CreateCommand(label, cfg.Database.Name, "")+
			"), and set METARUSH_MONGODB_URI on the new machine before it joins.")
	}
	if isAtlas {
		res.ManualSteps = append(res.ManualSteps, "Allow the new machine's network in Atlas (skip if it is already covered): "+at.AccessListCommand(label))
	}

	// Bundle, then the installation record.
	if werr := WriteBundle(out, b); werr != nil {
		return nil, werr
	}
	undo = append(undo, func() { _ = os.Remove(out) })
	inst := models.Installation{ID: insID, WorkspaceID: a.WorkspaceID(), Name: name, State: models.InstallationInvited, StorageKey: ref,
		DBUser: res.DBUser, InvitedAt: &now, InvitedBy: &a.Actor, CreatedAt: now}
	if ref != nil {
		inst.StorageKeyID = ref.KeyID
	}
	if ierr := a.DB.Insert(ctx, store.CInstallations, inst); ierr != nil {
		return nil, ierr
	}
	details := map[string]any{"name": name, "keyProvider": res.KeyProvider, "keyLabel": res.KeyLabel, "keyId": res.KeyID, "keyScope": res.KeyScope, "dbUser": res.DBUser}
	if cluster != "" {
		details["cluster"] = cluster
	}
	a.Audit(ctx, "machine.invite", "installation", insID, "", details)
	res.NextSteps = []string{
		"Move the join file " + shortPath(out) + " to the new machine over a safe channel (AirDrop, a password manager or scp): it holds passwords and keys.",
		"On the new machine, run: metarush machine join <that file> (it deletes the file once the machine has joined).",
		"Then delete this machine's copy of " + filepath.Base(out) + ".",
	}
	return res, nil
}

// shortPath shows a file in the person's home folder as ~/… (the full path
// stays in the JSON output).
func shortPath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + p[len(h):]
	}
	return p
}

// VerifyKey proves a key can write, range-read and delete under the store's
// prefix. New keys can take a moment to propagate, so it retries briefly.
func VerifyKey(ctx context.Context, s config.S3, keyID, secret string) error {
	st, err := media.NewS3(media.S3Config{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, Prefix: s.Prefix, AccessKeyID: keyID, SecretAccessKey: secret, ForcePathStyle: s.ForcePathStyle})
	if err != nil {
		return err
	}
	var last error
	for i := 0; i < 6; i++ {
		if last = RoundTrip(ctx, st); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * time.Second):
		}
	}
	return last
}

// RoundTrip writes a probe object, reads part of it back and deletes it.
func RoundTrip(ctx context.Context, st media.Store) error {
	tmp, err := os.CreateTemp("", "metarush-keyprobe-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	payload := []byte("metarush key probe " + time.Now().UTC().Format(time.RFC3339Nano))
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	key := "probes/" + ids.New("prb")
	if _, err := st.PutFile(ctx, key, tmp.Name(), media.PutOptions{ContentType: "text/plain"}, media.Resume{}, nil); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	rc, _, err := st.Get(ctx, key, 9, 3)
	if err != nil {
		return fmt.Errorf("ranged read: %w", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != string(payload[9:12]) {
		return errors.New("ranged read returned wrong bytes")
	}
	if err := st.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

// RevokeOptions configures `machine revoke`.
type RevokeOptions struct {
	Name           string // machine name or installation ID
	Atlas          bool
	AtlasProfile   string
	AtlasProjectID string
	Force          bool
}

// MachineRevoke is the result of a revoke.
type MachineRevoke struct {
	InstallationID string   `json:"installationId"`
	Name           string   `json:"name"`
	AlreadyRevoked bool     `json:"alreadyRevoked"`
	KeyLabel       string   `json:"keyLabel,omitempty"`
	KeyID          string   `json:"keyId,omitempty"` // masked
	KeyDeleted     bool     `json:"keyDeleted"`
	DBUser         string   `json:"dbUser,omitempty"`
	DBUserDeleted  bool     `json:"dbUserDeleted"`
	ManualSteps    []string `json:"manualSteps,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

func findInstallation(ctx context.Context, a *app.App, nameOrID string) (*models.Installation, error) {
	var list []models.Installation
	filter := bson.M{"name": strings.ToLower(nameOrID)}
	if ids.Valid(ids.Installation, nameOrID) {
		filter = bson.M{"_id": nameOrID}
	}
	if err := a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), filter, &list, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})); err != nil {
		return nil, err
	}
	var live []models.Installation
	for _, i := range list {
		if i.State != models.InstallationRevoked {
			live = append(live, i)
		}
	}
	switch {
	case len(live) == 1:
		return &live[0], nil
	case len(live) > 1:
		var idl []string
		for _, i := range live {
			idl = append(idl, i.ID)
		}
		return nil, apperr.New(apperr.Conflict, "%d machines are named “%s”.", len(live), nameOrID).
			WithHint("Revoke the one you mean by its ID (add --details to see them).").WithDetails(map[string]any{"installationIds": idl})
	case len(list) > 0:
		return &list[0], nil // already revoked: idempotent cleanup
	}
	return nil, apperr.New(apperr.NotFound, "There's no machine named “%s” in this workspace.", nameOrID).
		WithHint("The Settings page lists this workspace's machines.").WithCommand("metarush machine list")
}

func keyIDOf(i *models.Installation) string {
	if i.StorageKey != nil && i.StorageKey.KeyID != "" {
		return i.StorageKey.KeyID
	}
	return i.StorageKeyID
}

// Revoke deletes a machine's key (and, with Atlas, its database user) and marks
// the installation revoked. It refuses to cut off the installation running it,
// or a key other live installations share, unless Force.
func Revoke(ctx context.Context, a *app.App, o RevokeOptions) (*MachineRevoke, error) {
	cfg := a.Cfg
	inst, err := findInstallation(ctx, a, strings.TrimSpace(o.Name))
	if err != nil {
		return nil, err
	}
	res := &MachineRevoke{InstallationID: inst.ID, Name: inst.Name, AlreadyRevoked: inst.State == models.InstallationRevoked, DBUser: inst.DBUser}
	keyID := keyIDOf(inst)
	if inst.StorageKey != nil {
		res.KeyLabel = inst.StorageKey.Label
	}
	res.KeyID = MaskKeyID(keyID)
	current := ""
	if cfg.Media.Provider == "s3" {
		current = cfg.Media.S3.AccessKeyID
	}
	if !o.Force && !res.AlreadyRevoked {
		if inst.ID == cfg.Installation.ID {
			return nil, apperr.New(apperr.Conflict, "MetaRush won't revoke “%s”: it's the machine you are using now.", inst.Name).
				WithHint("Revoke it from another machine that can manage keys, or add --force.")
		}
		if keyID != "" && keyID == current {
			return nil, apperr.New(apperr.Conflict, "MetaRush won't revoke “%s”: it uses the same storage key as this machine, so deleting the key would cut this machine off too.", inst.Name).
				WithHint("Give this machine its own key first (metarush machine rekey), or add --force.").WithDetails(map[string]any{"keyId": MaskKeyID(keyID)})
		}
		if keyID != "" {
			var sharing []models.Installation
			_ = a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), bson.M{"_id": bson.M{"$ne": inst.ID}, "state": bson.M{"$ne": models.InstallationRevoked},
				"$or": bson.A{bson.M{"storageKeyId": keyID}, bson.M{"storageKey.keyId": keyID}}}, &sharing)
			if len(sharing) > 0 {
				var names []string
				for _, s := range sharing {
					names = append(names, s.Name)
				}
				return nil, apperr.New(apperr.Conflict, "MetaRush won't revoke “%s”: its storage key is shared with %s.", inst.Name, strings.Join(names, ", ")).
					WithHint("Give those machines their own keys first (run metarush machine rekey on each), or add --force.")
			}
		}
	}
	now := time.Now().UTC()
	set := bson.M{}
	// Storage key.
	switch {
	case inst.StorageKey != nil && inst.StorageKey.Managed && inst.StorageKey.DeletedAt == nil:
		p, perr := For(cfg, inst.StorageKey.Label)
		if m, ok := IsManual(perr); ok {
			res.ManualSteps = append(res.ManualSteps, m.Steps...)
			break
		} else if perr != nil {
			return nil, perr
		}
		if rerr := p.Revoke(ctx, *inst.StorageKey); rerr != nil {
			return nil, apperr.Wrap(apperr.Storage, rerr, "MetaRush couldn't delete the storage key of “%s”, so the machine is not revoked.", inst.Name).
				WithHint("Only the key that invited it, or an organization admin key, can delete it: revoke it from the machine that invited it, or give this one an admin key.").
				WithCommand(EnvAdminKeyID + "=<admin key id> " + EnvAdminSecret + "=<its secret> metarush machine revoke " + inst.Name)
		}
		if st, lerr := p.Lookup(ctx, inst.StorageKey.KeyID); lerr == nil && st.Exists {
			return nil, apperr.New(apperr.Storage, "The storage provider still lists the key of “%s” after it was deleted, so the machine is not revoked. Try again in a minute.", inst.Name).
				WithDetails(map[string]any{"keyId": MaskKeyID(inst.StorageKey.KeyID)})
		}
		res.KeyDeleted = true
		set["storageKey.deletedAt"] = now
	case inst.StorageKey != nil && inst.StorageKey.DeletedAt != nil:
		res.KeyDeleted = true
	case inst.StorageKey != nil:
		res.ManualSteps = append(res.ManualSteps, fmt.Sprintf("Delete the storage key %s (%s) in your storage provider's console: it was made by hand.", inst.StorageKey.Label, MaskKeyID(inst.StorageKey.KeyID)))
	case keyID != "" && keyID == current:
		res.Warnings = append(res.Warnings, "It used the same storage key as this machine, so the key was left in place (replace it if that machine is lost).")
	case keyID != "":
		res.ManualSteps = append(res.ManualSteps, fmt.Sprintf("It uses the storage key %s, which MetaRush didn't make: delete or replace it in your storage provider's console.", MaskKeyID(keyID)))
	}
	// Database user.
	at := &Atlas{Tools: a.Tools, Profile: o.AtlasProfile, ProjectID: firstNonEmpty(o.AtlasProjectID, cfg.Database.AtlasProjectID)}
	switch {
	case inst.DBUser != "" && inst.DBUserDeletedAt != nil:
		res.DBUserDeleted = true
	case inst.DBUser == "" && !config.IsLocalURI(cfg.Database.URI):
		res.Warnings = append(res.Warnings, "It had no database user of its own; if it used another user's password, change that password.")
	case inst.DBUser != "" && !strings.HasPrefix(inst.DBUser, LabelPrefix):
		res.ManualSteps = append(res.ManualSteps, "Delete its database user yourself: "+at.DeleteCommand(inst.DBUser))
	case inst.DBUser != "" && o.Atlas:
		if rerr := at.Ready(ctx); rerr != nil {
			res.Warnings = append(res.Warnings, rerr.Error())
			res.ManualSteps = append(res.ManualSteps, "Delete its database user: "+at.DeleteCommand(inst.DBUser))
			break
		}
		if derr := at.DeleteUser(ctx, inst.DBUser); derr != nil {
			if ok, _ := at.UserExists(ctx, inst.DBUser); ok {
				res.Warnings = append(res.Warnings, "MetaRush couldn't delete its database user: "+derr.Error())
				res.ManualSteps = append(res.ManualSteps, "Delete its database user: "+at.DeleteCommand(inst.DBUser))
				break
			}
		}
		res.DBUserDeleted = true
		set["dbUserDeletedAt"] = now
	case inst.DBUser != "":
		res.ManualSteps = append(res.ManualSteps, "Delete its database user (or run the revoke again with --atlas): "+at.DeleteCommand(inst.DBUser))
	}
	if !res.AlreadyRevoked {
		set["state"], set["revokedAt"], set["revokedBy"] = models.InstallationRevoked, now, a.Actor
	}
	if len(set) > 0 {
		if err := a.DB.Update(ctx, store.CInstallations, a.WorkspaceID(), inst.ID, bson.M{"$set": set}); err != nil {
			return nil, err
		}
	}
	a.Audit(ctx, "machine.revoke", "installation", inst.ID, "", map[string]any{"name": inst.Name, "keyLabel": res.KeyLabel, "keyId": res.KeyID,
		"keyDeleted": res.KeyDeleted, "dbUser": res.DBUser, "dbUserDeleted": res.DBUserDeleted, "force": o.Force, "alreadyRevoked": res.AlreadyRevoked})
	return res, nil
}

// MachineRow is one installation in `machine list` (no secrets).
type MachineRow struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Hostname   string     `json:"hostname,omitempty"`
	OS         string     `json:"os,omitempty"`
	Arch       string     `json:"arch,omitempty"`
	Version    string     `json:"version,omitempty"`
	State      string     `json:"state" jsonschema:"enum=invited,enum=active,enum=revoked"`
	Current    bool       `json:"current"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	KeyLabel   string     `json:"keyLabel,omitempty"`
	KeyID      string     `json:"keyId,omitempty"` // masked
	KeyScope   string     `json:"keyScope,omitempty"`
	KeyManaged bool       `json:"keyManaged"`
	KeyStatus  string     `json:"keyStatus" jsonschema:"enum=unchecked,enum=exists,enum=missing,enum=deleted,enum=unknown,enum=none"`
	DBUser     string     `json:"dbUser,omitempty"`
	Warnings   []string   `json:"warnings,omitempty"`
}

// MachineList lists a workspace's installations and their keys.
type MachineList struct {
	Workspace   config.Ref   `json:"workspace"`
	KeyProvider string       `json:"keyProvider"`
	Machines    []MachineRow `json:"machines"`
	KeysChecked bool         `json:"keysChecked"`
	CheckError  string       `json:"checkError,omitempty"`
	// OtherKeys are provider keys labeled metarush-* that no installation uses
	// (e.g. the CI key, or leftovers).
	OtherKeys []MachineKey `json:"otherKeys,omitempty"`
}

// List returns installations with key labels and liveness; with checkKeys it
// also asks the provider whether each managed key still exists.
func List(ctx context.Context, a *app.App, checkKeys bool) (*MachineList, error) {
	cfg := a.Cfg
	var insts []models.Installation
	if err := a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), nil, &insts, options.Find().SetSort(bson.D{{Key: "lastSeenAt", Value: -1}, {Key: "createdAt", Value: -1}})); err != nil {
		return nil, err
	}
	out := &MachineList{Workspace: cfg.Workspace, KeyProvider: ProviderName(cfg), Machines: []MachineRow{}}
	var prov Provider
	if checkKeys {
		p, err := For(cfg, "")
		if err != nil {
			out.CheckError = err.Error()
		} else {
			prov = p
			out.KeysChecked = true
		}
	}
	current := ""
	if cfg.Media.Provider == "s3" {
		current = cfg.Media.S3.AccessKeyID
	}
	users := map[string][]string{}
	for i := range insts {
		in := &insts[i]
		if in.ID == cfg.Installation.ID && in.StorageKeyID == "" {
			in.StorageKeyID = current
		}
		if k := keyIDOf(in); k != "" && in.State != models.InstallationRevoked {
			users[k] = append(users[k], in.Name)
		}
	}
	attached := map[string]bool{}
	for i := range insts {
		in := &insts[i]
		row := MachineRow{ID: in.ID, Name: in.Name, Hostname: in.Hostname, OS: in.OS, Arch: in.Arch, Version: in.Version, State: firstNonEmpty(in.State, models.InstallationActive),
			Current: in.ID == cfg.Installation.ID, DBUser: in.DBUser, KeyStatus: "none"}
		if !in.LastSeenAt.IsZero() {
			t := in.LastSeenAt
			row.LastSeenAt = &t
		}
		k := keyIDOf(in)
		attached[k] = true
		if k != "" {
			row.KeyID, row.KeyStatus = MaskKeyID(k), "unchecked"
		}
		if in.StorageKey != nil {
			row.KeyLabel, row.KeyScope, row.KeyManaged = in.StorageKey.Label, in.StorageKey.Scope, in.StorageKey.Managed
			if in.StorageKey.Role != "" {
				row.KeyScope += " (" + in.StorageKey.Role + ")"
			}
			if in.StorageKey.DeletedAt != nil {
				row.KeyStatus = "deleted"
			}
		} else if k != "" {
			row.KeyLabel = "(not made by MetaRush)"
		}
		if prov != nil && k != "" && row.KeyStatus == "unchecked" {
			if st, err := prov.Lookup(ctx, k); err != nil {
				row.KeyStatus = "unknown"
			} else if st.Exists {
				row.KeyStatus = "exists"
				if row.KeyLabel == "(not made by MetaRush)" && st.Label != "" {
					row.KeyLabel = st.Label
				}
			} else if row.KeyManaged {
				row.KeyStatus = "missing"
			} else {
				row.KeyStatus = "unknown" // not visible to this admin key
			}
		}
		if row.State != models.InstallationRevoked && len(users[k]) > 1 {
			row.Warnings = append(row.Warnings, "Shares its storage key with "+strings.Join(without(users[k], in.Name), ", ")+
				", so it can't be removed on its own: run metarush machine rekey on it to give it a key of its own.")
		}
		if row.State == models.InstallationRevoked && row.KeyStatus == "exists" && row.KeyManaged {
			row.Warnings = append(row.Warnings, "Revoked, but its storage key still exists: revoke it again to delete the key.")
		}
		if row.State == models.InstallationRevoked && in.DBUser != "" && in.DBUserDeletedAt == nil {
			row.Warnings = append(row.Warnings, "Revoked, but its database user "+in.DBUser+" still exists: delete it in Atlas, or revoke the machine again with Atlas access.")
		}
		if row.State == models.InstallationInvited && in.InvitedAt != nil && time.Since(*in.InvitedAt) > 7*24*time.Hour {
			row.Warnings = append(row.Warnings, "Invited on "+in.InvitedAt.Format("2 Jan 2006")+" but never joined: revoke it if its join file is no longer needed.")
		}
		out.Machines = append(out.Machines, row)
	}
	if prov != nil {
		if keys, err := prov.ListLabeled(ctx, LabelPrefix); err == nil {
			for _, kk := range keys {
				if !attached[kk.KeyID] {
					kk.KeyID = MaskKeyID(kk.KeyID)
					out.OtherKeys = append(out.OtherKeys, kk)
				}
			}
			sort.Slice(out.OtherKeys, func(i, j int) bool { return out.OtherKeys[i].Label < out.OtherKeys[j].Label })
		}
	}
	return out, nil
}

func without(list []string, x string) []string {
	var out []string
	for _, v := range list {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}
