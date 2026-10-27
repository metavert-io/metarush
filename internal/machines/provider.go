// Package machines manages per-machine credentials for a shared workspace: one
// object-store key (and optionally one database user) per installation, so a
// lost laptop can be revoked without touching anyone else.
//
// Flow: an admin machine runs `machine invite <name>`, which mints a key scoped
// to the workspace's bucket (and prefix), optionally creates a least-privilege
// database user, and writes a 0600 join bundle. The new machine runs
// `machine join <bundle>`. `machine revoke <name>` deletes the key (and user)
// and marks the installation revoked.
package machines

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
)

// Key scopes for minted keys.
const (
	// ScopePrefix limits the key to objects under the store's prefix (an IAM
	// policy on bucket/prefix/*). Default when the store has a prefix.
	ScopePrefix = "prefix"
	// ScopeBucket grants a bucket role on the whole bucket.
	ScopeBucket = "bucket"
)

// MintRequest describes the key to create.
type MintRequest struct {
	Label  string // metarush-<name>
	Bucket string
	Prefix string
	Scope  string // prefix | bucket
	Role   string // bucket scope: ReadWrite (default) | Editor | ReadOnly
}

// Minted is a new key. Secret is shown once and only ever written to a 0600 file.
type Minted struct {
	Ref    models.StorageKeyRef
	Secret string
}

// KeyStatus is what the provider reports about an existing key.
type KeyStatus struct {
	Exists bool
	Label  string
	Status string
	Detail string
}

// Provider mints and revokes per-machine object-store keys.
type Provider interface {
	Name() string
	Mint(ctx context.Context, req MintRequest) (*Minted, error)
	Revoke(ctx context.Context, ref models.StorageKeyRef) error
	Lookup(ctx context.Context, keyID string) (*KeyStatus, error)
	// ListLabeled lists keys this admin can see whose label starts with prefix.
	ListLabeled(ctx context.Context, prefix string) ([]MachineKey, error)
}

// MachineKey is one provider key (no secret).
type MachineKey struct {
	KeyID     string    `json:"keyId"`
	Label     string    `json:"label"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	Scope     string    `json:"scope"`
}

// ManualRequired means this store's provider cannot mint keys programmatically
// (with the credentials available); Steps say exactly what a person must do.
type ManualRequired struct {
	Provider string
	Reason   string
	Steps    []string
}

func (m *ManualRequired) Error() string {
	return fmt.Sprintf("MetaRush can't make the new machine's storage key itself (%s): it has to be made by hand", m.Reason)
}

// IsManual reports a ManualRequired error and returns it.
func IsManual(err error) (*ManualRequired, bool) {
	var m *ManualRequired
	ok := errors.As(err, &m)
	return m, ok
}

// Admin credential overrides: an admin may mint keys with a different key than
// the one this installation stores media with (e.g. an organization admin key).
const (
	EnvAdminKeyID  = config.EnvPrefix + "STORAGE_ADMIN_ACCESS_KEY_ID"
	EnvAdminSecret = config.EnvPrefix + "STORAGE_ADMIN_SECRET_ACCESS_KEY"
	EnvTigrisIAM   = config.EnvPrefix + "TIGRIS_IAM_ENDPOINT"
	// EnvManualSecret carries the secret of a key a person created by hand
	// (`machine invite --access-key-id …`), so it never appears in argv.
	EnvManualSecret = config.EnvPrefix + "MACHINE_S3_SECRET_ACCESS_KEY"
)

// IsTigris reports whether an S3 config points at Tigris.
func IsTigris(s config.S3) bool {
	if s.Preset == "tigris" {
		return true
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return strings.HasSuffix(h, ".tigris.dev") || h == "t3.storage.dev" || strings.HasSuffix(h, ".t3.storage.dev")
}

// ProviderName names the key provider for a config: tigris, manual-<preset>, or none.
func ProviderName(cfg *config.Config) string {
	if cfg.Media.Provider != "s3" {
		return "none"
	}
	if IsTigris(cfg.Media.S3) {
		return "tigris"
	}
	return "manual"
}

// For returns the key provider for an installation's media store. Stores that
// cannot mint keys return a *ManualRequired describing the manual procedure for
// label (e.g. "metarush-laptop").
func For(cfg *config.Config, label string) (Provider, error) {
	if cfg.Media.Provider != "s3" {
		return nil, &ManualRequired{Provider: "filesystem", Reason: "storage on a disk has no access keys", Steps: filesystemSteps(cfg)}
	}
	s := cfg.Media.S3
	if !IsTigris(s) {
		return nil, &ManualRequired{Provider: firstNonEmpty(s.Preset, "s3"), Reason: "MetaRush can't make keys with this storage provider", Steps: manualSteps(s, label)}
	}
	id, secret := os.Getenv(EnvAdminKeyID), os.Getenv(EnvAdminSecret)
	if id == "" || secret == "" {
		id, secret = s.AccessKeyID, s.SecretAccessKey
	}
	if id == "" || secret == "" {
		return nil, &ManualRequired{Provider: "tigris", Reason: "this machine has no key that may make new keys", Steps: []string{
			"Set " + EnvAdminKeyID + " and " + EnvAdminSecret + " to a Tigris key that owns the bucket (or an organization admin key), then run it again."}}
	}
	return &TigrisProvider{IAM: &Tigris{Endpoint: os.Getenv(EnvTigrisIAM), AccessKeyID: id, SecretAccessKey: secret}}, nil
}

// TigrisProvider mints keys through Tigris's IAM API.
type TigrisProvider struct {
	IAM *Tigris
}

func (p *TigrisProvider) Name() string { return "tigris" }

// Mint creates a no_default_allow key (it can do only what is granted below —
// no listing or creating buckets, no creating keys) and grants either an IAM
// policy on bucket/prefix/* or a bucket role.
func (p *TigrisProvider) Mint(ctx context.Context, req MintRequest) (*Minted, error) {
	scope := req.Scope
	if scope == "" {
		scope = ScopeBucket
		if strings.Trim(req.Prefix, "/") != "" {
			scope = ScopePrefix
		}
	}
	ref := models.StorageKeyRef{Provider: "tigris", Label: req.Label, Bucket: req.Bucket, Prefix: strings.Trim(req.Prefix, "/"), Scope: scope, Managed: true}
	var roles []BucketRole
	switch scope {
	case ScopePrefix:
		if ref.Prefix == "" {
			return nil, errors.New("prefix scope needs a store prefix (use --key-scope bucket)")
		}
	case ScopeBucket:
		ref.Role = firstNonEmpty(req.Role, RoleReadWrite)
		switch ref.Role {
		case RoleReadOnly, RoleReadWrite, RoleEditor:
		default:
			return nil, fmt.Errorf("unknown role %q (ReadWrite, Editor or ReadOnly)", ref.Role)
		}
		roles = []BucketRole{{Bucket: req.Bucket, Role: ref.Role}}
	default:
		return nil, fmt.Errorf("unknown key scope %q (prefix or bucket)", scope)
	}
	k, err := p.IAM.CreateKey(ctx, req.Label, roles, ScopeNoDefaultAllow)
	if err != nil {
		if IsForbidden(err) {
			return nil, fmt.Errorf("%w: this machine's storage key may not make new keys, so invite from the machine whose key owns the bucket, or set %s and %s to such a key", err, EnvAdminKeyID, EnvAdminSecret)
		}
		return nil, err
	}
	ref.KeyID, ref.CreatedAt = k.ID, k.CreatedAt
	if scope == ScopePrefix {
		suffix := strings.ToLower(k.ID)
		if len(suffix) > 6 {
			suffix = suffix[len(suffix)-6:]
		}
		name := req.Label + "-" + suffix
		arn, err := p.IAM.CreatePolicy(ctx, name, "MetaRush key "+req.Label+": objects under "+req.Bucket+"/"+ref.Prefix+"/", PrefixPolicy(req.Bucket, ref.Prefix))
		if err == nil {
			ref.PolicyARN = arn
			err = p.IAM.AttachPolicy(ctx, k.ID, arn)
		}
		if err != nil {
			_ = p.Revoke(context.WithoutCancel(ctx), ref) // never leave a half-configured key behind
			return nil, fmt.Errorf("scope the key to %s/%s/: %w", req.Bucket, ref.Prefix, err)
		}
	}
	return &Minted{Ref: ref, Secret: k.Secret}, nil
}

// Revoke deletes the key and its policy. A key that no longer exists is not an error.
func (p *TigrisProvider) Revoke(ctx context.Context, ref models.StorageKeyRef) error {
	var errs []error
	if ref.KeyID != "" {
		if err := p.IAM.DeleteKey(ctx, ref.KeyID); err != nil && !isGone(err) {
			errs = append(errs, fmt.Errorf("delete key %s: %w", MaskKeyID(ref.KeyID), err))
		}
	}
	if ref.PolicyARN != "" {
		if err := p.IAM.DeletePolicy(ctx, ref.PolicyARN); err != nil && !isGone(err) {
			errs = append(errs, fmt.Errorf("delete policy: %w", err))
		}
	}
	return errors.Join(errs...)
}

func isGone(err error) bool {
	var te *TigrisError
	if errors.As(err, &te) {
		m := strings.ToLower(te.Message)
		return te.Status == 404 || strings.Contains(m, "not found") || strings.Contains(m, "does not exist") || strings.Contains(m, "nosuchentity")
	}
	return false
}

// Lookup reports whether a key exists (as visible to the admin key).
func (p *TigrisProvider) Lookup(ctx context.Context, keyID string) (*KeyStatus, error) {
	k, err := p.IAM.GetKey(ctx, keyID)
	if errors.Is(err, ErrKeyNotFound) {
		return &KeyStatus{Exists: false}, nil
	}
	if err != nil {
		return nil, err
	}
	detail := k.Scope
	for _, r := range k.Roles {
		detail += fmt.Sprintf(", %s on %s", r.Role, r.Bucket)
	}
	return &KeyStatus{Exists: true, Label: k.Name, Status: k.Status, Detail: detail}, nil
}

// ListLabeled lists the admin's keys whose label starts with prefix.
func (p *TigrisProvider) ListLabeled(ctx context.Context, prefix string) ([]MachineKey, error) {
	ks, err := p.IAM.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	var out []MachineKey
	for _, k := range ks {
		if strings.HasPrefix(k.Name, prefix) {
			out = append(out, MachineKey{KeyID: k.ID, Label: k.Name, Status: k.Status, CreatedAt: k.CreatedAt, Scope: k.Scope})
		}
	}
	return out, nil
}

func objectsARN(s config.S3) string {
	if p := strings.Trim(s.Prefix, "/"); p != "" {
		return "arn:aws:s3:::" + s.Bucket + "/" + p + "/*"
	}
	return "arn:aws:s3:::" + s.Bucket + "/*"
}

func manualSteps(s config.S3, label string) []string {
	where := s.Bucket
	if p := strings.Trim(s.Prefix, "/"); p != "" {
		where += "/" + p + "/"
	}
	common := "MetaRush needs: read, write and delete objects, multipart uploads (create, upload part, list parts, complete, abort) and listing in-progress uploads — on " + where + " only."
	finish := fmt.Sprintf("Then run: %s=<secret> metarush machine invite <name> --access-key-id <key id>   (the secret stays out of shell history if you `read -s` it first)", EnvManualSecret)
	switch s.Preset {
	case "r2":
		return []string{common,
			"Cloudflare dashboard → R2 → Manage R2 API Tokens → Create API token.",
			fmt.Sprintf("Name it %q, permission \"Object Read & Write\", \"Apply to specific buckets only\" → %s. Create, then copy the Access Key ID and Secret Access Key (shown once).", label, s.Bucket),
			finish,
			"Revoke later: delete that API token in the same R2 page."}
	case "b2":
		prefix := ""
		if p := strings.Trim(s.Prefix, "/"); p != "" {
			prefix = " --name-prefix " + p + "/"
		}
		return []string{common,
			fmt.Sprintf("b2 key create --bucket %s%s %s listBuckets,listFiles,readFiles,writeFiles,deleteFiles", s.Bucket, prefix, label),
			"It prints the applicationKeyId (use as the access key ID) and the applicationKey (the secret, shown once).",
			finish,
			"Revoke later: b2 key delete <applicationKeyId>"}
	case "aws":
		return []string{common,
			fmt.Sprintf("aws iam create-user --user-name %s", label),
			fmt.Sprintf(`aws iam put-user-policy --user-name %s --policy-name %s --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Resource":"%s"},{"Effect":"Allow","Action":["s3:ListBucket","s3:ListBucketMultipartUploads"],"Resource":"arn:aws:s3:::%s"}]}'`, label, label, objectsARN(s), s.Bucket),
			fmt.Sprintf("aws iam create-access-key --user-name %s   (prints AccessKeyId and SecretAccessKey once)", label),
			finish,
			fmt.Sprintf("Revoke later: aws iam delete-access-key --user-name %s --access-key-id <id>; aws iam delete-user-policy --user-name %s --policy-name %s; aws iam delete-user --user-name %s", label, label, label, label)}
	case "gcs":
		return []string{common,
			fmt.Sprintf("gcloud iam service-accounts create %s --display-name %q", label, "MetaRush "+label),
			fmt.Sprintf("gcloud storage buckets add-iam-policy-binding gs://%s --member=serviceAccount:%s@<project>.iam.gserviceaccount.com --role=roles/storage.objectUser", s.Bucket, label),
			fmt.Sprintf("gcloud storage hmac create %s@<project>.iam.gserviceaccount.com   (prints the access ID and secret once)", label),
			finish,
			fmt.Sprintf("Revoke later: gcloud storage hmac update <access-id> --deactivate && gcloud storage hmac delete <access-id>; gcloud iam service-accounts delete %s@<project>.iam.gserviceaccount.com", label)}
	default:
		return []string{common,
			fmt.Sprintf("In your provider's console, create an access key named %q limited to bucket %s (and, if the provider supports it, to the prefix).", label, s.Bucket),
			finish,
			"Revoke later: delete that key in the provider's console."}
	}
}

func filesystemSteps(cfg *config.Config) []string {
	fs := cfg.Media.Filesystem
	if !fs.Shared {
		return []string{"This workspace keeps its footage on this machine's disk (" + shortPath(fs.Root) + "), which other machines can't reach.",
			"Move MetaRush's storage to a shared place first: an S3-compatible bucket (Tigris, R2, B2, S3, GCS) or a shared drive (--media-shared)."}
	}
	return []string{"A shared drive has no MetaRush keys: give the new machine its own account on the file server (SMB/NFS user or NAS account) with read and write access to " + fs.Root + ".",
		"Connect the share at the same place on the new machine (the join file records " + fs.Root + ").",
		"To revoke it later, turn off that account on the file server."}
}
