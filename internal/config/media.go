package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// mediaEnv are the METARUSH_* variables that override config.yaml's media
// section (applyEnv), with the field each one sets.
var mediaEnv = []struct {
	name     string
	provider string // the provider the variable matters for ("" = every one)
	field    func(*Media) *string
}{
	{"MEDIA_PROVIDER", "", func(m *Media) *string { return &m.Provider }},
	{"MEDIA_ROOT", "filesystem", func(m *Media) *string { return &m.Filesystem.Root }},
	{"S3_ENDPOINT", "s3", func(m *Media) *string { return &m.S3.Endpoint }},
	{"S3_REGION", "s3", func(m *Media) *string { return &m.S3.Region }},
	{"S3_BUCKET", "s3", func(m *Media) *string { return &m.S3.Bucket }},
	{"S3_PREFIX", "s3", func(m *Media) *string { return &m.S3.Prefix }},
	{"S3_ACCESS_KEY_ID", "s3", func(m *Media) *string { return &m.S3.AccessKeyID }},
	{"S3_SECRET_ACCESS_KEY", "s3", func(m *Media) *string { return &m.S3.SecretAccessKey }},
	{"GCS_BUCKET", "gcs", func(m *Media) *string { return &m.GCS.Bucket }},
	{"GCS_PREFIX", "gcs", func(m *Media) *string { return &m.GCS.Prefix }},
	{"GCS_CREDENTIALS", "gcs", func(m *Media) *string { return &m.GCS.CredentialsFile }},
}

// MediaOverrides lists the environment variables (the shell's, or
// <home>/.env's) that would replace these media settings once they are in
// config.yaml, because METARUSH_* variables win over the file.
func MediaOverrides(m Media) []string {
	var out []string
	for _, e := range mediaEnv {
		v, ok := os.LookupEnv(EnvPrefix + e.name)
		if !ok || v == "" || (e.provider != "" && e.provider != m.Provider) {
			continue
		}
		cur := *e.field(&m)
		if e.name == "MEDIA_ROOT" || e.name == "GCS_CREDENTIALS" {
			v, cur = filepath.Clean(expandTilde(v)), filepath.Clean(expandTilde(cur))
		}
		if v != cur {
			out = append(out, EnvPrefix+e.name)
		}
	}
	return out
}

// MediaFile is a media store described in a file of its own (the new storage
// of `metarush storage migrate --to <file>`): the keys of config.yaml's media
// section (provider, then filesystem, s3 or gcs), at the top level or under
// media:. Secrets are written as references only, keychain:<name> or
// ${VARIABLE}, never as the secret itself.
type MediaFile struct {
	// Media has the references resolved: what to connect with now.
	Media Media
	// Node is the section as written (references intact, relative paths made
	// absolute): what config.yaml gets when the move switches over.
	Node *yaml.Node
	// SecretVar is the environment variable an S3 secret comes from (${NAME}).
	SecretVar string
}

var (
	envOnly    = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)(:)?\}$`)
	envWithDef = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*:.+\}$`)
)

// LoadMediaFile reads and checks a store description. Relative paths in it
// are relative to the file.
func LoadMediaFile(home, path string) (*MediaFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, errors.New("the file is too large to be a storage description")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("the file is not YAML or JSON: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("the file needs a media section: provider (filesystem, s3 or gcs) and its settings")
	}
	node := doc.Content[0]
	if m := mapGet(node, "media"); m != nil {
		node = m
	}
	if node.Kind != yaml.MappingNode || mapGet(node, "provider") == nil {
		return nil, errors.New("the file needs a media section: provider (filesystem, s3 or gcs) and its settings")
	}
	dir, _ := filepath.Abs(filepath.Dir(path))
	for _, p := range [][]string{{"filesystem", "root"}, {"gcs", "credentialsFile"}} {
		if s := nodeAt(node, p...); s != nil && s.Kind == yaml.ScalarNode && s.Value != "" &&
			!strings.Contains(s.Value, "${") && !strings.HasPrefix(s.Value, "~") && !filepath.IsAbs(s.Value) {
			s.Value, s.Style = filepath.Join(dir, s.Value), 0
		}
	}
	f := &MediaFile{Node: node}
	if prov := mapGet(node, "provider"); prov.Value == "s3" {
		s := nodeAt(node, "s3", "secretAccessKey")
		v := ""
		if s != nil {
			v = strings.TrimSpace(s.Value)
		}
		switch {
		case v == "":
			return nil, errors.New("the s3 section needs secretAccessKey: ${VARIABLE} (an environment variable holding the secret) or keychain:<name>")
		case IsSecretRef(v):
		case envWithDef.MatchString(v):
			return nil, errors.New("secretAccessKey cannot have a default value: write ${VARIABLE} and set the variable")
		case envOnly.MatchString(v):
			f.SecretVar = envOnly.FindStringSubmatch(v)[1]
		default:
			return nil, errors.New("secretAccessKey must be a reference, ${VARIABLE} (an environment variable) or keychain:<name>: MetaRush never reads a secret from a file")
		}
	}
	m, err := parseMedia(home, node)
	if err != nil {
		return nil, err
	}
	f.Media = m
	return f, nil
}

// ParseMediaNode resolves a media section kept as YAML (a previous store,
// recorded by a move) the way config.Load would.
func ParseMediaNode(home, text string) (Media, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return Media{}, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return Media{}, errors.New("not a media section")
	}
	return parseMedia(home, doc.Content[0])
}

// parseMedia expands ${VAR} references and resolves keychain references, the
// way config.Load does for config.yaml (without the METARUSH_* overrides).
func parseMedia(home string, node *yaml.Node) (Media, error) {
	text, err := NodeYAML(node)
	if err != nil {
		return Media{}, err
	}
	var m Media
	if err := yaml.Unmarshal([]byte(Expand(text)), &m); err != nil {
		return Media{}, fmt.Errorf("the media section: %w", err)
	}
	for _, p := range []*string{&m.S3.AccessKeyID, &m.S3.SecretAccessKey} {
		v, err := ResolveSecret(home, *p)
		if err != nil {
			return Media{}, err
		}
		*p = v
	}
	for _, p := range []*string{&m.Filesystem.Root, &m.GCS.CredentialsFile} {
		if *p != "" {
			*p = expandTilde(*p)
		}
	}
	switch m.Provider {
	case "filesystem":
		if m.Filesystem.Root == "" {
			return Media{}, errors.New("the filesystem section needs root: the folder that holds the media")
		}
	case "s3":
		switch {
		case m.S3.Bucket == "":
			return Media{}, errors.New("the s3 section needs a bucket")
		case m.S3.AccessKeyID == "":
			return Media{}, errors.New("the s3 section needs accessKeyId")
		case m.S3.SecretAccessKey == "":
			return Media{}, errors.New("the s3 secret is empty: set the environment variable secretAccessKey names")
		}
	case "gcs":
		if m.GCS.Bucket == "" {
			return Media{}, errors.New("the gcs section needs a bucket")
		}
	default:
		return Media{}, fmt.Errorf("provider must be filesystem, s3 or gcs, not %q", m.Provider)
	}
	return m, nil
}

// RefsOnly reports whether a media section holds no secret itself (an S3
// secret, if any, is a keychain or ${VAR} reference), so it may be kept.
func RefsOnly(node *yaml.Node) bool {
	s := nodeAt(node, "s3", "secretAccessKey")
	if s == nil || s.Value == "" {
		return true
	}
	return IsSecretRef(s.Value) || envOnly.MatchString(strings.TrimSpace(s.Value))
}

// SetSecretRef points the S3 secret of a media section at a keychain entry.
func (f *MediaFile) SetSecretRef(ref string) {
	if s := nodeAt(f.Node, "s3", "secretAccessKey"); s != nil {
		s.Value, s.Style, s.Tag = ref, 0, "!!str"
	}
}

// NodeYAML writes a YAML node as text.
func NodeYAML(node *yaml.Node) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(4)
	if err := enc.Encode(node); err != nil {
		return "", err
	}
	_ = enc.Close()
	return buf.String(), nil
}

// GetField returns one value of <home>/config.yaml as written (nothing
// expanded or resolved), or nil when it is not there.
func GetField(home string, path []string) (*yaml.Node, error) {
	raw, err := os.ReadFile(Path(home))
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path(home), err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, nil
	}
	return nodeAt(doc.Content[0], path...), nil
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func nodeAt(n *yaml.Node, path ...string) *yaml.Node {
	for _, k := range path {
		if n = mapGet(n, k); n == nil {
			return nil
		}
	}
	return n
}
