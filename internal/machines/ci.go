package machines

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/secretpath"
)

// CILabel names the key used by the live-bucket CI job.
const CILabel = LabelPrefix + "ci"

// DefaultCIPrefix is where TestS3Live writes (ci-test/<timestamp>/…).
const DefaultCIPrefix = "ci-test"

// CIKey describes a minted CI key (never its secret).
type CIKey struct {
	Label    string   `json:"label"`
	KeyID    string   `json:"keyId"` // masked
	Bucket   string   `json:"bucket"`
	Endpoint string   `json:"endpoint"`
	Region   string   `json:"region"`
	Prefix   string   `json:"prefix"`
	File     string   `json:"file"`
	Replaced []string `json:"replaced,omitempty"` // masked IDs of deleted older CI keys
}

// MintCIKey creates a key that can only touch bucket/<prefix>/* (an IAM policy,
// no bucket role, no default organization operations), verifies it, and writes
// the METARUSH_TEST_S3_* variables to a new 0600 dotenv file. With replace, older
// keys labeled metarush-ci (visible to this admin key) are deleted afterwards.
func MintCIKey(ctx context.Context, a *app.App, prefix, file string, replace, allowUnsafePath bool) (*CIKey, error) {
	if err := secretpath.Check(file, "the CI credentials file", allowUnsafePath); err != nil {
		return nil, err
	}
	cfg := a.Cfg
	if cfg.Media.Provider != "s3" || !IsTigris(cfg.Media.S3) {
		return nil, apperr.New(apperr.NotConfigured, "ci-key can mint keys on Tigris only").
			WithHint("create a key limited to <bucket>/ci-test/* in your provider's console and set the METARUSH_TEST_S3_* CI secrets by hand")
	}
	prefix = strings.Trim(firstNonEmpty(prefix, DefaultCIPrefix), "/")
	s := cfg.Media.S3
	p, err := For(cfg, CILabel)
	if err != nil {
		return nil, err
	}
	tp, ok := p.(*TigrisProvider)
	if !ok {
		return nil, errors.New("unexpected provider")
	}
	var old []MachineKey
	if replace {
		if old, err = tp.ListLabeled(ctx, CILabel); err != nil {
			return nil, err
		}
	}
	m, err := tp.Mint(ctx, MintRequest{Label: CILabel, Bucket: s.Bucket, Prefix: prefix, Scope: ScopePrefix})
	if err != nil {
		return nil, apperr.Wrap(apperr.Storage, err, "could not mint the CI key")
	}
	bg := context.WithoutCancel(ctx)
	ciS3 := config.S3{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, Prefix: prefix + "/keyprobe", ForcePathStyle: s.ForcePathStyle}
	if err := VerifyKey(ctx, ciS3, m.Ref.KeyID, m.Secret); err != nil {
		_ = tp.Revoke(bg, m.Ref)
		return nil, apperr.Wrap(apperr.Storage, err, "the new CI key failed a round trip under %s/%s/", s.Bucket, prefix)
	}
	env := fmt.Sprintf("METARUSH_TEST_S3_BUCKET=%s\nMETARUSH_TEST_S3_ENDPOINT=%s\nMETARUSH_TEST_S3_REGION=%s\nMETARUSH_TEST_S3_ACCESS_KEY_ID=%s\nMETARUSH_TEST_S3_SECRET_ACCESS_KEY=%s\n",
		s.Bucket, s.Endpoint, firstNonEmpty(s.Region, "auto"), m.Ref.KeyID, m.Secret)
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = tp.Revoke(bg, m.Ref)
		return nil, err
	}
	if _, err := f.WriteString(env); err != nil {
		f.Close()
		os.Remove(file)
		_ = tp.Revoke(bg, m.Ref)
		return nil, err
	}
	f.Close()
	out := &CIKey{Label: CILabel, KeyID: MaskKeyID(m.Ref.KeyID), Bucket: s.Bucket, Endpoint: s.Endpoint, Region: firstNonEmpty(s.Region, "auto"), Prefix: prefix, File: file}
	for _, k := range old {
		if err := tp.RevokeByID(ctx, k.KeyID); err != nil {
			return out, fmt.Errorf("new key written to %s, but deleting the old CI key %s failed: %w", file, MaskKeyID(k.KeyID), err)
		}
		out.Replaced = append(out.Replaced, MaskKeyID(k.KeyID))
	}
	a.Audit(ctx, "machine.ci_key", "storage_key", out.KeyID, "", map[string]any{"label": CILabel, "keyId": out.KeyID, "prefix": prefix, "replaced": out.Replaced})
	return out, nil
}

// RevokeByID deletes a key and every policy attached to it (looked up by name).
func (p *TigrisProvider) RevokeByID(ctx context.Context, keyID string) error {
	names, err := p.IAM.ListKeyPolicies(ctx, keyID)
	if err != nil && !isGone(err) {
		return err
	}
	var arns []string
	if len(names) > 0 {
		all, err := p.IAM.ListPolicies(ctx)
		if err != nil {
			return err
		}
		want := map[string]bool{}
		for _, n := range names {
			want[n] = true
		}
		for _, pol := range all {
			if want[pol.Name] {
				arns = append(arns, pol.Arn)
			}
		}
	}
	if err := p.IAM.DeleteKey(ctx, keyID); err != nil && !isGone(err) {
		return err
	}
	for _, arn := range arns {
		if err := p.IAM.DeletePolicy(ctx, arn); err != nil && !isGone(err) {
			return err
		}
	}
	return nil
}
