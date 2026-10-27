package machines

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
)

// BundleKind marks a MetaRush join bundle.
const BundleKind = "metarush-join-bundle"

// Bundle is everything a new machine needs to join a workspace. It contains
// secrets (the machine's storage key, and a database URI when a user was
// created for it): it is written 0600 and should be moved over a secure channel
// and deleted after `machine join`.
type Bundle struct {
	Kind         string       `json:"kind"`
	Version      int          `json:"version"`
	CreatedAt    time.Time    `json:"createdAt"`
	InvitedBy    models.Actor `json:"invitedBy"`
	Workspace    config.Ref   `json:"workspace"`
	Installation config.Ref   `json:"installation"`
	Database     BundleDB     `json:"database"`
	Media        BundleMedia  `json:"media"`
	Notes        []string     `json:"notes,omitempty"`
}

// BundleDB names the shared database. URI (with credentials) is present only
// when invite created a database user for this machine.
type BundleDB struct {
	Name           string `json:"name"`
	Host           string `json:"host"`
	URI            string `json:"uri,omitempty"`
	User           string `json:"user,omitempty"`
	AtlasOrgID     string `json:"atlasOrgId,omitempty"`
	AtlasOrgName   string `json:"atlasOrgName,omitempty"`
	AtlasProjectID string `json:"atlasProjectId,omitempty"`
}

// BundleMedia is the canonical media store with this machine's own key.
type BundleMedia struct {
	Provider   string            `json:"provider"`
	Filesystem config.Filesystem `json:"filesystem,omitempty"`
	S3         *BundleS3         `json:"s3,omitempty"`
}

// BundleS3 is an S3-compatible store plus the machine's key.
type BundleS3 struct {
	Preset          string `json:"preset,omitempty"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix,omitempty"`
	ForcePathStyle  bool   `json:"forcePathStyle,omitempty"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	KeyLabel        string `json:"keyLabel,omitempty"`
}

// WriteBundle writes b to path with mode 0600, refusing to overwrite anything.
func WriteBundle(path string, b *Bundle) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("there is already a join file at %s, and MetaRush won't overwrite it (move or delete it, or choose another place with --out)", shortPath(path))
		}
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// ReadBundle loads and validates a bundle. It reports (as a warning) when the
// file is readable by other users.
func ReadBundle(path string) (*Bundle, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var warnings []string
	if st, err := f.Stat(); err == nil && st.Mode().Perm()&0o077 != 0 {
		warnings = append(warnings, fmt.Sprintf("Other people on this machine can read the join file %s, and it holds passwords and keys: make it private (chmod 600) and delete it after joining.", shortPath(path)))
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	var b Bundle
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return nil, nil, fmt.Errorf("%s isn't a MetaRush join file (%w)", shortPath(path), err)
	}
	if b.Kind != BundleKind {
		return nil, nil, fmt.Errorf("%s isn't a MetaRush join file", shortPath(path))
	}
	if b.Version != 1 {
		return nil, nil, fmt.Errorf("this join file comes from a newer MetaRush (version %d of the file): update MetaRush on this machine first", b.Version)
	}
	if b.Workspace.ID == "" || b.Installation.ID == "" || b.Database.Name == "" {
		return nil, nil, errors.New("the join file is incomplete (it needs the workspace, the machine and the database): make a new one with machine invite")
	}
	switch b.Media.Provider {
	case "s3":
		if b.Media.S3 == nil || b.Media.S3.Bucket == "" || b.Media.S3.AccessKeyID == "" || b.Media.S3.SecretAccessKey == "" {
			return nil, nil, errors.New("the join file has no complete storage key: make a new one with machine invite")
		}
	case "filesystem":
		if b.Media.Filesystem.Root == "" {
			return nil, nil, errors.New("the join file doesn't say where the shared storage is: make a new one with machine invite")
		}
	default:
		return nil, nil, fmt.Errorf("the join file names a storage MetaRush doesn't know (%q)", b.Media.Provider)
	}
	return &b, warnings, nil
}
