package ops

import (
	"context"
	"os"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/machines"
)

// MachineListIn configures machine.list.
type MachineListIn struct {
	CheckKeys bool `json:"checkKeys,omitempty" jsonschema:"description=Also ask the storage provider whether each machine's key still exists (needs the admin key)."`
}

// MachineInviteIn mints a machine's credentials and writes its join bundle.
type MachineInviteIn struct {
	Name string `json:"name" jsonschema:"pattern=^[a-z0-9-]+$,maxLength=40,description=Machine name; the key and database user are called metarush-<name>."`
	Out  string `json:"out,omitempty" jsonschema:"description=Bundle path (default ~/.metarush-bundles/metarush-join-<name>.json). Never overwritten; never inside a sync folder or a repository."`
	// AllowUnsafeSecretPath permits an --out inside a repository that ignores it.
	AllowUnsafeSecretPath bool   `json:"allowUnsafeSecretPath,omitempty" jsonschema:"description=Allow --out inside a git repository when git ignores it (never inside a sync folder)."`
	KeyScope              string `json:"keyScope,omitempty" jsonschema:"enum=auto,enum=prefix,enum=bucket,description=prefix = only objects under the store prefix (default when the store has one); bucket = a bucket role."`
	Role                  string `json:"role,omitempty" jsonschema:"enum=ReadWrite,enum=Editor,enum=ReadOnly,description=Bucket role for --key-scope bucket (default ReadWrite)."`
	Atlas                 bool   `json:"atlas,omitempty" jsonschema:"description=Also create a database user metarush-<name> (readWrite on this database only) with the Atlas CLI."`
	AtlasProfile          string `json:"atlasProfile,omitempty"`
	AtlasProjectID        string `json:"atlasProjectId,omitempty"`
	AccessKeyID           string `json:"accessKeyId,omitempty" jsonschema:"description=Use a key created by hand (providers without a key API); its secret comes from METARUSH_MACHINE_S3_SECRET_ACCESS_KEY."`
}

// MachineRevokeIn revokes a machine.
type MachineRevokeIn struct {
	Name           string `json:"name" jsonschema:"description=Machine name or installation ID."`
	Atlas          bool   `json:"atlas,omitempty" jsonschema:"description=Also delete its Atlas database user with the Atlas CLI."`
	AtlasProfile   string `json:"atlasProfile,omitempty"`
	AtlasProjectID string `json:"atlasProjectId,omitempty"`
	Force          bool   `json:"force,omitempty" jsonschema:"description=Allow revoking this installation or a key other machines share."`
}

// MachineRekeyIn gives this machine a storage key of its own in place of the
// one it has (usually a key it shares with other machines).
type MachineRekeyIn struct {
	KeyScope string `json:"keyScope,omitempty" jsonschema:"enum=auto,enum=prefix,enum=bucket,description=prefix = only objects under the store prefix (default when the store has one); bucket = a bucket role."`
	Role     string `json:"role,omitempty" jsonschema:"enum=ReadWrite,enum=Editor,enum=ReadOnly,description=Bucket role for --key-scope bucket (default ReadWrite)."`
	// AccessKeyID is a key made by hand (providers without a key API).
	AccessKeyID string `json:"accessKeyId,omitempty" jsonschema:"description=Use a key created by hand (providers without a key API); its secret comes from METARUSH_MACHINE_S3_SECRET_ACCESS_KEY."`
	// AllowUnsafeSecretPath permits <home>/.env inside a repository that ignores it.
	AllowUnsafeSecretPath bool `json:"allowUnsafeSecretPath,omitempty" jsonschema:"description=Installations that keep secrets in <home>/.env: allow it inside a git repository that ignores it."`
}

func init() {
	Def("machine.rekeyPlan", "Say what machine.rekey would do on this machine: its key, the machines sharing it, the new key's scope (changes nothing)", Flags{LocalOnly: true},
		func(ctx context.Context, a *app.App, in *MachineRekeyIn) (*machines.MachineRekey, error) {
			return machines.Rekey(ctx, a, rekeyOptions(in, true))
		})
	Def("machine.rekey", "Give this machine a storage key of its own in place of the one it shares (minted, tested, switched; the old key is never deleted) — people only, owners", Flags{Mutating: true, HumanOnly: true, LocalOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *MachineRekeyIn) (*machines.MachineRekey, error) {
			return machines.Rekey(ctx, a, rekeyOptions(in, false))
		})
	Def("machine.list", "List the workspace's machines, their storage key labels (no secrets), state and last seen", Flags{}, func(ctx context.Context, a *app.App, in *MachineListIn) (*machines.MachineList, error) {
		return machines.List(ctx, a, in.CheckKeys)
	})
	Def("machine.invite", "Mint a per-machine storage key (and optionally a database user) and write a 0600 join bundle; owners", Flags{Mutating: true, LocalOnly: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, in *MachineInviteIn) (*machines.MachineInvite, error) {
		return machines.Invite(ctx, a, machines.InviteOptions{Name: in.Name, Out: in.Out, AllowUnsafeSecretPath: in.AllowUnsafeSecretPath, KeyScope: in.KeyScope, Role: in.Role, Atlas: in.Atlas,
			AtlasProfile: in.AtlasProfile, AtlasProjectID: in.AtlasProjectID, AccessKeyID: in.AccessKeyID, ManualSecret: os.Getenv(machines.EnvManualSecret)})
	})
	Def("machine.revoke", "Delete a machine's storage key (and optionally its database user) and mark it revoked; owners", Flags{Mutating: true, LocalOnly: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, in *MachineRevokeIn) (*machines.MachineRevoke, error) {
		return machines.Revoke(ctx, a, machines.RevokeOptions{Name: in.Name, Atlas: in.Atlas, AtlasProfile: in.AtlasProfile, AtlasProjectID: in.AtlasProjectID, Force: in.Force})
	})
}

func rekeyOptions(in *MachineRekeyIn, dry bool) machines.RekeyOptions {
	o := machines.RekeyOptions{DryRun: dry, KeyScope: in.KeyScope, Role: in.Role, AccessKeyID: in.AccessKeyID, AllowUnsafeSecretPath: in.AllowUnsafeSecretPath}
	if in.AccessKeyID != "" {
		o.ManualSecret = os.Getenv(machines.EnvManualSecret)
	}
	return o
}
