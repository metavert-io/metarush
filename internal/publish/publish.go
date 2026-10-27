// Package publish posts a finished video to a video platform as a person
// (D305): a people-only step, every time, through that person's own sign-in.
// The sign-in is the platform's OAuth flow run by the person in a browser on
// this computer, with the platform client the computer's owner registered
// (their own, never one MetaRush ships); its refresh token is kept in this
// computer's keychain under that person, never in the database, the config,
// the log or an answer. Tests use internal/testutil/fakeyoutube; the real
// services are refused under go test.
package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/logbook"
)

// Platforms MetaRush publishes to.
const (
	YouTube = "youtube"
)

// Privacy settings of a posted video.
const (
	Private  = "private"
	Unlisted = "unlisted"
	Public   = "public"
)

// Identity is who a person is signed in as on a platform.
type Identity struct {
	Platform  string    `json:"platform"`
	ChannelID string    `json:"channelId"`
	Channel   string    `json:"channel" jsonschema:"description=The channel's name."`
	Scopes    []string  `json:"scopes,omitempty"`
	At        time.Time `json:"at" jsonschema:"description=When they signed in."`
}

// Caption is a captions file to send with the video.
type Caption struct {
	Language string // es, pt-BR
	Name     string // "English", "Spanish (machine-translated)"
	Format   string // vtt | srt
	Content  string
}

// Video is what is sent.
type Video struct {
	Path        string
	Size        int64
	Title       string
	Description string
	Tags        []string
	Privacy     string
	Language    string // the spoken language (BCP-47)
	// Synthetic says the video shows realistic AI-made people, places or
	// events (the platform's own disclosure).
	Synthetic bool
	Captions  []Caption
	// Session is an unfinished upload to go on with (the platform's resumable
	// session, kept only on this computer); empty starts a new one.
	Session string `json:"-"`
	// Started is told the session before any of the file is sent, so an upload
	// cut short (MetaRush stopped) can go on later.
	Started func(session string) `json:"-"`
	// Mine says, once the video is up, whether this sender still owns the
	// post (another MetaRush may have taken it over); captions are sent only
	// by its owner.
	Mine func() bool `json:"-"`
}

// Posted is the video on the platform.
type Posted struct {
	VideoID  string
	URL      string
	Captions []string // languages the platform took
	// Notes say, in plain words, what didn't go as asked (captions not taken, privacy kept).
	Notes []string
	// Late: it reached the platform after the post had been marked as not
	// posted (it stays marked so; Notes say what happened).
	Late bool
	// NotMine: the video is up, but another sender had taken the post over,
	// so this one sent no captions and recorded nothing.
	NotMine bool
}

// Progress reports bytes sent.
type Progress func(sent, total int64)

type progressKey struct{}

// WithProgress asks the upload to report its progress (the CLI's line).
func WithProgress(ctx context.Context, p Progress) context.Context {
	return context.WithValue(ctx, progressKey{}, p)
}

func progressOf(ctx context.Context) Progress {
	p, _ := ctx.Value(progressKey{}).(Progress)
	return p
}

// ProgressOf is the progress a caller asked for (nil: none).
func ProgressOf(ctx context.Context) Progress { return progressOf(ctx) }

// Opener shows a sign-in page in the person's browser on this computer;
// done removes what it left for the browser (called once the sign-in is over).
type Opener func(ctx context.Context, url string) (done func(), err error)

// ErrNotSetUp: the computer's owner hasn't registered the platform client.
var ErrNotSetUp = errors.New("not set up")

// ErrNotSignedIn: the person hasn't signed in on this computer.
var ErrNotSignedIn = errors.New("not signed in")

// Problem is a platform's refusal in plain words.
type Problem struct {
	Status int
	Msg    string
	Hint   string
	// Command fixes it (shown behind Details).
	Command string
	// Retry: trying again later may work (the service was busy).
	Retry bool
	// Gone: the platform no longer has the unfinished upload a resume named.
	Gone bool
}

func (p *Problem) Error() string { return p.Msg }

// realRefused reports that the platform's real servers can't be used: under
// go test (fakes only, like the Tailscale and rclone fakes).
func realRefused() bool { return testing.Testing() }

// loopback reports a base address on this computer (a fake platform).
func loopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	if u.Hostname() == "localhost" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

// Keychain names: the platform client per computer (the owner's), each
// person's sign-in per computer.
func clientSecretName(platform string) string { return "publish." + platform + ".client" }

func personSecretName(platform, person string) string {
	if person == "" {
		person = "owner"
	}
	return "publish." + platform + ".person." + person
}

// Client is the platform client an owner registered (an OAuth client of the
// "desktop app" kind in their own project).
type Client struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

// Secrets live where the installation keeps them: this computer's keychain,
// or its secrets file (<home>/.env, 0600) when it keeps secrets there
// (secrets: env, computers without a keychain).
func envName(name string) string {
	var b strings.Builder
	b.WriteString("METARUSH_")
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func storeSecret(cfg *config.Config, name, value string) error {
	if cfg.SecretMode() == "env" {
		return config.SetDotEnvVar(cfg.Home(), envName(name), value, false)
	}
	_, err := config.StoreSecret(cfg.Home(), name, value)
	return err
}

func readSecret(cfg *config.Config, name string) string {
	if cfg.SecretMode() == "env" {
		v, _ := config.DotEnvValue(cfg.Home(), envName(name))
		return v
	}
	v, err := config.ResolveSecret(cfg.Home(), config.SecretRef(name))
	if err != nil {
		return ""
	}
	return v
}

func deleteSecret(cfg *config.Config, name string) error {
	if cfg.SecretMode() == "env" {
		_, err := config.UnsetDotEnvVar(cfg.Home(), envName(name))
		return err
	}
	return config.DeleteSecret(cfg.Home(), name)
}

// SaveClient keeps the platform client in this computer's keychain.
func SaveClient(cfg *config.Config, platform string, c Client) error {
	c.ID, c.Secret = strings.TrimSpace(c.ID), strings.TrimSpace(c.Secret)
	if c.ID == "" || c.Secret == "" || strings.ContainsAny(c.ID+c.Secret, " \t\r\n") {
		return &Problem{Msg: "The client ID and secret are both needed, as the platform shows them (no spaces)."}
	}
	logbook.RememberSecret(c.Secret)
	b, _ := json.Marshal(c)
	return storeSecret(cfg, clientSecretName(platform), string(b))
}

// LoadClient reads the platform client (ErrNotSetUp when there is none).
func LoadClient(cfg *config.Config, platform string) (*Client, error) {
	s := readSecret(cfg, clientSecretName(platform))
	if s == "" {
		return nil, ErrNotSetUp
	}
	var c Client
	if json.Unmarshal([]byte(s), &c) != nil || c.ID == "" {
		return nil, ErrNotSetUp
	}
	logbook.RememberSecret(c.Secret)
	return &c, nil
}

// RemoveClient forgets the platform client.
func RemoveClient(cfg *config.Config, platform string) error {
	return deleteSecret(cfg, clientSecretName(platform))
}

// signIn is a person's sign-in as kept in the keychain.
type signIn struct {
	Refresh  string   `json:"refresh"`
	Identity Identity `json:"account"`
}

func saveSignIn(cfg *config.Config, platform, person string, s signIn) error {
	logbook.RememberSecret(s.Refresh)
	b, _ := json.Marshal(s)
	return storeSecret(cfg, personSecretName(platform, person), string(b))
}

func loadSignIn(cfg *config.Config, platform, person string) (*signIn, error) {
	s := readSecret(cfg, personSecretName(platform, person))
	if s == "" {
		return nil, ErrNotSignedIn
	}
	var x signIn
	if json.Unmarshal([]byte(s), &x) != nil || x.Refresh == "" {
		return nil, ErrNotSignedIn
	}
	logbook.RememberSecret(x.Refresh)
	return &x, nil
}

// SignedIn is who a person is signed in as on this computer (nil: not signed in).
func SignedIn(cfg *config.Config, platform, person string) *Identity {
	s, err := loadSignIn(cfg, platform, person)
	if err != nil {
		return nil
	}
	a := s.Identity
	return &a
}

// Forget removes a person's sign-in from this computer.
func Forget(cfg *config.Config, platform, person string) error {
	return deleteSecret(cfg, personSecretName(platform, person))
}

// readAll reads at most n bytes of an answer.
func readAll(r io.Reader, n int64) []byte {
	b, _ := io.ReadAll(io.LimitReader(r, n))
	return b
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Secrets are the publishing secrets this computer keeps for an installation:
// each platform client's secret and the refresh tokens of the sign-ins of the
// people named (and the owner's), wherever the installation keeps them. In the
// secrets file every publishing entry counts, whoever's it is. The activity
// log hides them (a secret source) and an archive of the workspace takes them
// out.
func Secrets(cfg *config.Config, people ...string) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	add := func(s string) {
		var x struct{ Secret, Refresh string }
		if s == "" || json.Unmarshal([]byte(s), &x) != nil {
			return
		}
		for _, v := range []string{x.Secret, x.Refresh} {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
	}
	for _, p := range []string{YouTube} {
		add(readSecret(cfg, clientSecretName(p)))
		for _, person := range append([]string{"owner"}, people...) {
			if person != "" {
				add(readSecret(cfg, personSecretName(p, person)))
			}
		}
	}
	if cfg.SecretMode() == "env" && cfg.Home() != "" {
		if b, err := os.ReadFile(config.DotEnvPath(cfg.Home())); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				k, v, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(l), "export "), "=")
				if ok && strings.HasPrefix(strings.TrimSpace(k), "METARUSH_PUBLISH_") {
					add(strings.Trim(strings.TrimSpace(v), `"'`))
				}
			}
		}
	}
	return out
}

func init() {
	logbook.AddSecretSource(func(cfg *config.Config) []string { return Secrets(cfg) })
}
