package leave

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/secretpath"
)

// Where a workspace keeps things (D290): its media store and its database.
// A cloud bucket, a folder a sync service uploads, or a database on a public
// server is cloud storage — a client whose rules keep footage, voices,
// pictures or words from cloud storage can't have them there.

// Kept is where one kind of a workspace's things is kept.
type Kept struct {
	// Cloud: a place elsewhere on the internet (a cloud bucket, a sync
	// folder, a database on a public server).
	Cloud bool `json:"cloud"`
	// Words says where, in plain words ("a Tigris bucket", "a folder on this
	// computer").
	Words string `json:"words"`
	// Kinds are what is kept there (media: footage, voices, faces,
	// pictures; the database: words).
	Kinds []string `json:"kinds"`
}

// MediaKinds are what the media store keeps for a project.
var MediaKinds = []string{models.LeaveFootage, models.LeaveVoices, models.LeaveFaces, models.LeavePictures}

// MediaKept is where the media store keeps a workspace's files.
func MediaKept(m config.Media) Kept {
	k := Kept{Kinds: MediaKinds}
	switch m.Provider {
	case "s3":
		if host := hostOf(m.S3.Endpoint); host != "" && nearby(host) {
			k.Words = "a storage server on " + nearWords(host)
			return k
		}
		k.Cloud, k.Words = true, "a cloud bucket"
	case "gcs":
		k.Cloud, k.Words = true, "a cloud bucket"
	default:
		v := secretpath.Inspect(m.Filesystem.Root)
		if v.Kind == secretpath.KindSync {
			k.Cloud, k.Words = true, "a folder a sync service uploads to the cloud"
			return k
		}
		k.Words = "a folder on this computer"
		if m.Filesystem.Shared {
			k.Words = "a shared folder on your network"
		}
	}
	return k
}

// DatabaseKept is where the database keeps a workspace's words (transcripts,
// names, notes).
func DatabaseKept(d config.Database) Kept {
	k := Kept{Kinds: []string{models.LeaveWords}}
	if d.Local != nil {
		k.Words = "the database on this computer"
		return k
	}
	u, err := url.Parse(strings.TrimSpace(d.URI))
	if err != nil || u.Host == "" {
		k.Words = "the database"
		return k
	}
	hosts := strings.Split(u.Host, ",")
	host := hostOf("mongodb://" + hosts[0])
	if u.Scheme == "mongodb+srv" || !nearby(host) {
		k.Cloud, k.Words = true, "a database on a server elsewhere"
		return k
	}
	k.Words = "a database on " + nearWords(host)
	return k
}

// StoragePlaces are where a workspace keeps things now.
func StoragePlaces(a *app.App) []Kept {
	return []Kept{MediaKept(a.Cfg.Media), DatabaseKept(a.Cfg.Database)}
}

// StorageProblems say, for a client's rules, what of theirs is kept in cloud
// storage the rules keep it from — and what to do (empty: nothing).
func StorageProblems(a *app.App, rules *models.LeaveRules) []string {
	if rules.Empty() {
		return nil
	}
	var out []string
	for _, k := range StoragePlaces(a) {
		if !k.Cloud {
			continue
		}
		var kept []string
		for _, kind := range k.Kinds {
			if rules.Keeps(models.PlaceStorage, kind) {
				kept = append(kept, KindWords(kind))
			}
		}
		if len(kept) == 0 {
			continue
		}
		if k.Kinds[0] == models.LeaveWords {
			out = append(out, fmt.Sprintf("This workspace keeps its transcripts and other words in %s, which these rules keep words from. Use the database on this computer for this client (metarush setup), or keep the client in a workspace of its own whose database is here.", k.Words))
			continue
		}
		out = append(out, fmt.Sprintf("This workspace keeps its media in %s, so this client's %s are in cloud storage, which these rules keep them from. Move the media to a folder on this computer (Settings → Storage), or keep the client in a workspace of its own whose storage is a folder here.", k.Words, joinAnd(kept)))
	}
	return out
}

func joinAnd(xs []string) string { return join(xs, "and") }

func hostOf(endpoint string) string {
	e := strings.TrimSpace(endpoint)
	if e == "" {
		return ""
	}
	if !strings.Contains(e, "://") {
		e = "https://" + e
	}
	u, err := url.Parse(e)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// nearby reports a host on this computer or a private network.
func nearby(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func nearWords(host string) string {
	if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || host == "localhost" {
		return "this computer"
	}
	return "your network"
}

// MayStore refuses adding a client's things to the workspace's media store
// when it is cloud storage their Content Settings keep them from (D290):
// footage (with its voices and faces) or a picture, into a project ("" the
// Inbox or the library: the default's settings).
func MayStore(ctx context.Context, a *app.App, projectID string, kinds []string, what string) error {
	k := MediaKept(a.Cfg.Media)
	if !k.Cloud {
		return nil
	}
	err := Check(ctx, a, Send{Place: models.PlaceStorage, To: "cloud", ToWords: k.Words, Kinds: kinds, Projects: []string{projectID}, What: what})
	if err != nil {
		e := apperr.From(err)
		e.Hint = "This workspace keeps its media in " + k.Words + ". Keep this client's footage in a workspace whose storage is a folder on this computer, or move the media there (Settings → Storage). " + e.Hint
		return e
	}
	return nil
}

// FootageKinds are what a video or sound file carries.
var FootageKinds = []string{models.LeaveFootage, models.LeaveVoices, models.LeaveFaces}

func init() {
	resources.MayStore = func(ctx context.Context, a *app.App, projectID, what string) error {
		return MayStore(ctx, a, projectID, []string{models.LeavePictures}, what)
	}
}
