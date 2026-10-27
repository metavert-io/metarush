package machines

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// RekeyOptions configures `machine rekey`.
type RekeyOptions struct {
	DryRun   bool
	KeyScope string // auto | prefix | bucket
	Role     string // bucket scope role
	// AccessKeyID is a key a person made by hand (providers without a key
	// API); ManualSecret is its secret (from the environment, never argv).
	AccessKeyID  string
	ManualSecret string
	// AllowUnsafeSecretPath permits <home>/.env inside a repository that
	// ignores it (installations that keep secrets there).
	AllowUnsafeSecretPath bool
}

// MachineRekey is the result of a rekey or its dry run. It never contains secrets.
type MachineRekey struct {
	InstallationID string `json:"installationId"`
	Name           string `json:"name"`
	DryRun         bool   `json:"dryRun"`
	// AlreadyOwn: this machine already has a storage key of its own (nothing to do).
	AlreadyOwn  bool   `json:"alreadyOwn"`
	KeyProvider string `json:"keyProvider"`
	OldKeyID    string `json:"oldKeyId,omitempty"` // masked
	// SharedWith are the other machines that still use the old key.
	SharedWith  []string `json:"sharedWith"`
	KeyLabel    string   `json:"keyLabel,omitempty"`
	KeyID       string   `json:"keyId,omitempty"` // the new key, masked
	KeyScope    string   `json:"keyScope,omitempty"`
	KeyRole     string   `json:"keyRole,omitempty"`
	KeyVerified bool     `json:"keyVerified"`
	// SecretsIn is where this machine keeps the new key's secret.
	SecretsIn string `json:"secretsIn,omitempty" jsonschema:"enum=keychain,enum=env"`
	// OldSecretRemoved: this computer's copy of the old key's secret was
	// removed from the keychain (the old key itself is never deleted).
	OldSecretRemoved bool     `json:"oldSecretRemoved"`
	Steps            []string `json:"steps,omitempty"` // dry run: what it would do, in order
	ManualSteps      []string `json:"manualSteps,omitempty"`
	NextSteps        []string `json:"nextSteps"`
	Warnings         []string `json:"warnings,omitempty"`
}

// envRefRe matches a whole ${VARIABLE} reference.
var envRefRe = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// secretRef says where config.yaml keeps the storage secret: a keychain
// entry or an environment variable (in <home>/.env).
type secretRef struct {
	mode    string // keychain | env
	name    string // the keychain entry or the variable
	written string // the reference as written
}

func storageSecretRef(home string) (secretRef, error) {
	n, err := config.GetField(home, []string{"media", "s3", "secretAccessKey"})
	if err != nil {
		return secretRef{}, err
	}
	v := ""
	if n != nil {
		v = strings.TrimSpace(n.Value)
	}
	switch {
	case config.IsSecretRef(v):
		return secretRef{mode: "keychain", name: strings.TrimPrefix(v, "keychain:"), written: v}, nil
	case envRefRe.MatchString(v):
		return secretRef{mode: "env", name: envRefRe.FindStringSubmatch(v)[1], written: v}, nil
	}
	return secretRef{}, apperr.New(apperr.NotConfigured, "MetaRush can't tell where this machine keeps its storage secret, so it can't replace it.").
		WithHint("Its settings should name a keychain entry or an environment variable: set MetaRush up again.").WithCommand("metarush init --reconfigure")
}

// keyOverrides lists the environment variables that would set this machine's
// storage key instead of config.yaml, so a new key there would not be used.
// A secret kept in <home>/.env (where a rekey writes it) is fine.
func keyOverrides(ref secretRef) []string {
	var out []string
	if v, ok := os.LookupEnv(config.EnvPrefix + "S3_ACCESS_KEY_ID"); ok && v != "" {
		out = append(out, config.EnvPrefix+"S3_ACCESS_KEY_ID")
	}
	std := config.EnvPrefix + "S3_SECRET_ACCESS_KEY"
	if v, ok := os.LookupEnv(std); ok && v != "" && !(ref.mode == "env" && ref.name == std && config.FromDotEnv(std)) {
		out = append(out, std)
	}
	if ref.mode == "env" && ref.name != std {
		if v, ok := os.LookupEnv(ref.name); ok && v != "" && !config.FromDotEnv(ref.name) {
			out = append(out, ref.name)
		}
	}
	return out
}

// Rekey gives the machine running it a storage key of its own in place of the
// one it has: usually the key every machine that joined before per-machine
// keys shares (D122). It mints a key limited to the workspace's footage (as
// invite does), tests it, keeps its secret where this machine keeps secrets,
// switches config.yaml to it and checks that MetaRush, loading its settings
// the way it does, works with it; anything that fails puts everything back
// and deletes the new key. The old key is never deleted: the result says when
// it can be, once no machine uses it. DryRun only says what it would do.
func Rekey(ctx context.Context, a *app.App, o RekeyOptions) (res *MachineRekey, err error) {
	cfg := a.Cfg
	home := cfg.Home()
	res = &MachineRekey{InstallationID: cfg.Installation.ID, Name: cfg.Installation.Name, DryRun: o.DryRun, KeyProvider: ProviderName(cfg), SharedWith: []string{}, NextSteps: []string{}}
	if o.AccessKeyID != "" {
		res.KeyProvider = "manual"
	}
	switch cfg.Media.Provider {
	case "s3":
	case "filesystem":
		return nil, apperr.New(apperr.NotConfigured, "This workspace keeps its footage on a drive, which has no MetaRush keys to replace.").
			WithHint("Give this machine its own account on the file server instead.").WithDetails(map[string]any{"steps": filesystemSteps(cfg)})
	default:
		return nil, apperr.New(apperr.NotConfigured, "This machine's storage has no MetaRush keys to replace.").
			WithHint("Give it its own credentials in your storage provider's console, then point config.yaml at them.")
	}
	s := cfg.Media.S3
	old := s.AccessKeyID
	res.OldKeyID = MaskKeyID(old)
	inst, err := findInstallation(ctx, a, cfg.Installation.ID)
	if err != nil {
		return nil, err
	}
	if inst.State == models.InstallationRevoked {
		return nil, apperr.New(apperr.Conflict, "This machine was removed from the workspace, so it can't get a new key.").
			WithHint("Invite it again from a machine that is still in the workspace.").WithCommand("metarush machine invite " + inst.Name)
	}
	var sharing []models.Installation
	if err := a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), bson.M{"_id": bson.M{"$ne": inst.ID}, "state": bson.M{"$ne": models.InstallationRevoked},
		"$or": bson.A{bson.M{"storageKeyId": old}, bson.M{"storageKey.keyId": old}}}, &sharing); err != nil {
		return nil, err
	}
	for _, x := range sharing {
		res.SharedWith = append(res.SharedWith, x.Name)
	}
	if k := inst.StorageKey; k != nil && k.KeyID == old && k.DeletedAt == nil && len(sharing) == 0 {
		res.AlreadyOwn = true
		res.KeyLabel, res.KeyID, res.KeyScope, res.KeyRole = k.Label, MaskKeyID(k.KeyID), k.Scope, k.Role
		res.NextSteps = append(res.NextSteps, "Nothing to do: this machine already has a storage key of its own.")
		return res, nil
	}
	if k := inst.StorageKey; k != nil && k.KeyID != old && k.DeletedAt == nil && k.KeyID != "" {
		res.Warnings = append(res.Warnings, fmt.Sprintf("The key made for this machine earlier (%s) isn't the one it uses: delete it in your storage provider's console if nothing else uses it.", MaskKeyID(k.KeyID)))
	}
	ref, err := storageSecretRef(home)
	if err != nil {
		return nil, err
	}
	res.SecretsIn = ref.mode
	if ov := keyOverrides(ref); len(ov) > 0 {
		return nil, apperr.New(apperr.Conflict, "This computer's environment sets the storage key (%s), so a new key in MetaRush's settings would not be used.", strings.Join(ov, ", ")).
			WithHint("Remove those settings from your shell setup (MetaRush keeps the key itself), then run the rekey again.").WithDetails(map[string]any{"variables": ov})
	}
	label := Label(inst.Name)
	res.KeyLabel = label
	var prov Provider
	if o.AccessKeyID == "" {
		p, perr := For(cfg, label)
		if m, ok := IsManual(perr); ok {
			if o.DryRun {
				res.ManualSteps = append(res.ManualSteps, m.Steps...)
				res.Steps = append(res.Steps, "MetaRush can't make the key itself here ("+m.Reason+"): make it by hand as the manual steps say, then run the rekey with --access-key-id.")
				res.NextSteps = rekeyNext(res, false)
				return res, nil
			}
			return nil, apperr.New(apperr.NotConfigured, "%s", m.Error()).WithHint("Make the key by hand as the steps say (add --details to see them), then run the rekey again with --access-key-id.").
				WithDetails(map[string]any{"provider": m.Provider, "steps": m.Steps})
		} else if perr != nil {
			return nil, perr
		}
		prov = p
	} else if o.ManualSecret == "" {
		return nil, apperr.New(apperr.InvalidInput, "--access-key-id also needs the key's secret, in %s.", EnvManualSecret).
			WithHint("Type it without it showing (read -s " + EnvManualSecret + "; export " + EnvManualSecret + "), then run the rekey again.")
	}
	where := s.Bucket
	if p := strings.Trim(s.Prefix, "/"); p != "" && o.KeyScope != ScopeBucket {
		where += "/" + p + "/"
	}
	if o.DryRun {
		if prov != nil {
			res.Steps = append(res.Steps, fmt.Sprintf("Make a storage key named %s that can only reach %s, and test it.", label, where))
		} else {
			res.Steps = append(res.Steps, "Test the key you made by hand.")
		}
		keep := "this computer's keychain"
		if ref.mode == "env" {
			keep = "MetaRush's .env file on this computer"
		}
		res.Steps = append(res.Steps, "Keep its secret in "+keep+", switch this machine to it, and check that MetaRush works with it. If anything fails, everything is put back"+
			map[bool]string{true: " and the new key is deleted.", false: "."}[prov != nil])
		res.NextSteps = rekeyNext(res, prov != nil)
		return res, nil
	}

	now := time.Now().UTC()
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
	// The new key.
	var key models.StorageKeyRef
	var secret string
	if prov != nil {
		scope := o.KeyScope
		if scope == "auto" {
			scope = ""
		}
		m, merr := prov.Mint(ctx, MintRequest{Label: label, Bucket: s.Bucket, Prefix: s.Prefix, Scope: scope, Role: o.Role})
		if merr != nil {
			return nil, apperr.Wrap(apperr.Storage, merr, "MetaRush couldn't make a storage key for this machine; nothing changed.")
		}
		undo = append(undo, func() { _ = prov.Revoke(bg, m.Ref) })
		key, secret = m.Ref, m.Secret
	} else {
		key = models.StorageKeyRef{Provider: "manual", KeyID: o.AccessKeyID, Label: label, Bucket: s.Bucket, Prefix: strings.Trim(s.Prefix, "/"), Scope: "manual", CreatedAt: now}
		secret = o.ManualSecret
	}
	logbook.RememberSecret(secret)
	if key.KeyID == old {
		return nil, apperr.New(apperr.InvalidInput, "That is the key this machine has now.").WithHint("Give it a different key.")
	}
	res.KeyID, res.KeyScope, res.KeyRole = MaskKeyID(key.KeyID), key.Scope, key.Role
	if verr := VerifyKey(ctx, s, key.KeyID, secret); verr != nil {
		return nil, apperr.Wrap(apperr.Storage, verr, "The new storage key didn't work when MetaRush tried it, so nothing changed.")
	}
	res.KeyVerified = true

	// Switch this machine to it.
	cfgPath := config.Path(home)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	undo = append(undo, func() { _ = restoreFile(cfgPath, before) })
	switch ref.mode {
	case "keychain":
		name := "s3-secret-access-key-" + keySuffix(key.KeyID)
		stored, serr := config.StoreSecret(home, name, secret)
		if serr != nil {
			return nil, apperr.New(apperr.NotConfigured, "The keychain couldn't keep the new key's secret (%v), so nothing changed.", serr)
		}
		undo = append(undo, func() { _ = config.DeleteSecret(home, name) })
		if err := config.SetField(home, []string{"media", "s3", "secretAccessKey"}, stored); err != nil {
			return nil, err
		}
	case "env":
		prev, had := os.LookupEnv(ref.name)
		if err := config.SetDotEnvVar(home, ref.name, secret, o.AllowUnsafeSecretPath); err != nil {
			return nil, apperr.Wrap(apperr.NotConfigured, err, "MetaRush couldn't keep the new key's secret in its .env file, so nothing changed.")
		}
		undo = append(undo, func() {
			if had && prev != "" {
				_ = config.SetDotEnvVar(home, ref.name, prev, true)
			} else {
				_, _ = config.UnsetDotEnvVar(home, ref.name)
			}
		})
	}
	if err := config.SetField(home, []string{"media", "s3", "accessKeyId"}, key.KeyID); err != nil {
		return nil, err
	}
	if err := checkRekey(ctx, home, a.Media.Locator(), key.KeyID, secret); err != nil {
		return nil, err
	}

	// The record, the audit trail, and this computer's copy of the old secret.
	if err := a.DB.Update(ctx, store.CInstallations, a.WorkspaceID(), inst.ID, bson.M{"$set": bson.M{"storageKeyId": key.KeyID, "storageKey": key}}); err != nil {
		return nil, err
	}
	undo = nil // switched: nothing to put back from here on
	if ref.mode == "keychain" && ref.name != "s3-secret-access-key-"+keySuffix(key.KeyID) {
		if derr := config.DeleteSecret(home, ref.name); derr == nil {
			res.OldSecretRemoved = true
		} else {
			res.Warnings = append(res.Warnings, "This computer's keychain still holds the old key's secret: delete the entry yourself (MetaRush no longer uses it).")
		}
	}
	a.Audit(ctx, "machine.rekey", "installation", inst.ID, "", map[string]any{"name": inst.Name, "oldKeyId": res.OldKeyID, "keyProvider": res.KeyProvider, "keyLabel": key.Label,
		"keyId": res.KeyID, "keyScope": key.Scope, "keyRole": key.Role, "sharedWith": res.SharedWith, "secretsIn": ref.mode})
	res.NextSteps = rekeyNext(res, prov != nil)
	return res, nil
}

// rekeyNext is what to do after a rekey (or, for a dry run, after it would
// run): the machines still on the old key, and when to delete it.
func rekeyNext(res *MachineRekey, minted bool) []string {
	var next []string
	if !res.DryRun {
		next = append(next, "MetaRush running in the background on this computer switches to the new key by itself within a few seconds.")
	}
	console := "your storage provider's console"
	if res.KeyProvider == "tigris" {
		console = "the Tigris console (Access keys)"
	}
	if len(res.SharedWith) > 0 {
		next = append(next, fmt.Sprintf("%s still %s the old key: run metarush machine rekey on %s too.", strings.Join(res.SharedWith, ", "),
			map[bool]string{true: "uses", false: "use"}[len(res.SharedWith) == 1], map[bool]string{true: "that machine", false: "each of them"}[len(res.SharedWith) == 1]))
		next = append(next, fmt.Sprintf("Once every machine has its own key, delete the old key (%s) in %s. MetaRush never deletes it itself.", res.OldKeyID, console))
	} else {
		next = append(next, fmt.Sprintf("No other machine uses the old key (%s): once nothing else does either (a CI job or a script), delete it in %s. MetaRush never deletes it itself.", res.OldKeyID, console))
	}
	if minted && os.Getenv(EnvAdminKeyID) == "" {
		next = append(next, "The new key can only reach the workspace's footage, so this machine can't make keys any more: to invite or remove machines from it later, set "+
			EnvAdminKeyID+" and "+EnvAdminSecret+" to a key that may (for that command only).")
	}
	return next
}

// checkRekey is the last check before a rekey keeps its changes (tests make
// it fail to see everything put back).
var checkRekey = checkRekeyed

// checkRekeyed loads the settings the way MetaRush will and checks that they
// use the new key and that it works.
func checkRekeyed(ctx context.Context, home, locator, keyID, secret string) error {
	cfg, err := config.Load(home)
	if err != nil {
		return apperr.New(apperr.NotConfigured, "With the new key MetaRush's settings would not load (%v), so it kept the old key.", err)
	}
	if cfg.Media.S3.AccessKeyID != keyID || cfg.Media.S3.SecretAccessKey != secret {
		return apperr.New(apperr.Conflict, "Something on this computer overrides MetaRush's settings, so it would not use the new key; it kept the old one.").
			WithHint("Check this computer's environment for METARUSH_S3_ variables, then run the rekey again.")
	}
	st, err := app.NewMediaStore(cfg)
	if err != nil {
		return apperr.New(apperr.NotConfigured, "With the new key MetaRush could not open the storage (%v), so it kept the old key.", err)
	}
	if st.Locator() != locator {
		return apperr.New(apperr.Conflict, "With the new key MetaRush would use another storage, so it kept the old key.")
	}
	if err := RoundTrip(ctx, st); err != nil {
		return apperr.Wrap(apperr.Storage, err, "With the settings it would use, MetaRush couldn't write to the storage, so it kept the old key.")
	}
	return nil
}

// keySuffix is a key ID's last characters, for names that tell keys apart.
func keySuffix(id string) string {
	s := strings.ToLower(id)
	if len(s) > 8 {
		s = s[len(s)-8:]
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// restoreFile puts a file's earlier contents back (atomically, 0600).
func restoreFile(path string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".restore")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
