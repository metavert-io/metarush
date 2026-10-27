package machines

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	"github.com/metavert-io/metarush/internal/tools"
)

// Atlas drives the MongoDB Atlas CLI (which holds its own login) to create and
// delete per-machine database users. Passwords go to the CLI on stdin, never in
// argv.
type Atlas struct {
	Tools     *tools.Set
	Profile   string // atlas -P profile ("" = the CLI's default)
	ProjectID string
}

// ErrAtlasUnavailable means the Atlas CLI is missing or not logged in.
var ErrAtlasUnavailable = errors.New("the MongoDB Atlas command-line tool is not installed or not signed in")

func (a *Atlas) args(extra ...string) []string {
	if a.Profile != "" {
		extra = append(extra, "-P", a.Profile)
	}
	return extra
}

func (a *Atlas) profileFlag() string {
	if a.Profile != "" {
		return " -P " + a.Profile
	}
	return ""
}

func (a *Atlas) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	spec := tools.Spec{Tool: tools.Atlas, Args: a.args(args...)}
	if stdin != nil {
		spec.Stdin = bytes.NewReader(stdin)
	}
	res, err := a.Tools.Run(ctx, spec)
	if err != nil {
		return nil, err
	}
	return res.Stdout, nil
}

// Ready reports whether the CLI is installed and logged in.
func (a *Atlas) Ready(ctx context.Context) error {
	if a.Tools == nil || !a.Tools.Has(tools.Atlas) {
		return fmt.Errorf("%w. Install it (brew install mongodb-atlas-cli) and sign in with atlas auth login", ErrAtlasUnavailable)
	}
	if a.ProjectID == "" {
		return errors.New("MetaRush doesn't know this workspace's Atlas project: add --atlas-project-id (or record it with init --reconfigure)")
	}
	if _, err := a.run(ctx, nil, "auth", "whoami"); err != nil {
		return fmt.Errorf("%w. Sign in with atlas auth login%s", ErrAtlasUnavailable, a.profileFlag())
	}
	return nil
}

// ClusterForHost returns the name of the project's cluster whose SRV host is host.
func (a *Atlas) ClusterForHost(ctx context.Context, host string) (string, error) {
	out, err := a.run(ctx, nil, "clusters", "list", "--projectId", a.ProjectID, "-o", "json")
	if err != nil {
		return "", err
	}
	var res struct {
		Results []struct {
			Name              string `json:"name"`
			ConnectionStrings struct {
				StandardSrv string `json:"standardSrv"`
			} `json:"connectionStrings"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("atlas clusters list: %w", err)
	}
	host = strings.ToLower(host)
	for _, c := range res.Results {
		if u, err := url.Parse(c.ConnectionStrings.StandardSrv); err == nil && strings.ToLower(u.Host) == host {
			return c.Name, nil
		}
	}
	return "", fmt.Errorf("no cluster of the Atlas project %s is at %s", a.ProjectID, host)
}

// UserExists reports whether a database user exists in the project.
func (a *Atlas) UserExists(ctx context.Context, username string) (bool, error) {
	_, err := a.run(ctx, nil, "dbusers", "describe", username, "--projectId", a.ProjectID, "-o", "json")
	if err == nil {
		return true, nil
	}
	var tf *tools.ErrFailed
	if errors.As(err, &tf) && (strings.Contains(tf.Stderr, "404") || strings.Contains(strings.ToLower(tf.Stderr), "not found") || strings.Contains(tf.Stderr, "USER_NOT_FOUND") || strings.Contains(tf.Stderr, "USERNAME_NOT_FOUND")) {
		return false, nil
	}
	return false, err
}

// CreateUser creates a SCRAM user with readWrite on database only, limited to
// cluster (when known). It returns the generated password.
func (a *Atlas) CreateUser(ctx context.Context, username, database, cluster, description string) (string, error) {
	pw, err := password(32)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"databaseName": "admin", "groupId": a.ProjectID, "username": username, "password": pw, "description": description,
		"roles": []map[string]string{{"roleName": "readWrite", "databaseName": database}},
	}
	if cluster != "" {
		body["scopes"] = []map[string]string{{"name": cluster, "type": "CLUSTER"}}
	}
	b, _ := json.Marshal(body)
	if _, err := a.run(ctx, b, "api", "databaseUsers", "createDatabaseUser", "--groupId", a.ProjectID); err != nil {
		return "", scrub(err, pw)
	}
	return pw, nil
}

// DeleteUser deletes a database user.
func (a *Atlas) DeleteUser(ctx context.Context, username string) error {
	_, err := a.run(ctx, nil, "dbusers", "delete", username, "--projectId", a.ProjectID, "--force")
	return err
}

// CreateCommand is the manual equivalent of CreateUser (password placeholder).
func (a *Atlas) CreateCommand(username, database, cluster string) string {
	scope := ""
	if cluster != "" {
		scope = " --scope " + cluster
	}
	return fmt.Sprintf("atlas dbusers create --username %s --role readWrite@%s%s --projectId %s%s   (it prompts for a password; then build the URI: mongodb+srv://%s:<password>@<host>/)",
		username, database, scope, firstNonEmpty(a.ProjectID, "<project-id>"), a.profileFlag(), username)
}

// DeleteCommand is the manual equivalent of DeleteUser.
func (a *Atlas) DeleteCommand(username string) string {
	return fmt.Sprintf("atlas dbusers delete %s --projectId %s --force%s", username, firstNonEmpty(a.ProjectID, "<project-id>"), a.profileFlag())
}

// AccessListCommand is the reminder for allowing a new machine's IP.
func (a *Atlas) AccessListCommand(label string) string {
	return fmt.Sprintf("atlas accessLists create <the new machine's public IP> --type ipAddress --comment %s --projectId %s%s", label, firstNonEmpty(a.ProjectID, "<project-id>"), a.profileFlag())
}

// WithUser returns uri with its credentials replaced by user:password.
func WithUser(uri, user, pw string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword(user, pw)
	return u.String(), nil
}

func password(n int) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		x, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[x.Int64()]
	}
	return string(b), nil
}

// scrub removes a secret from an error message (defense in depth: the CLI
// should never echo it).
func scrub(err error, secret string) error {
	if err == nil || secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "REDACTED"))
}
