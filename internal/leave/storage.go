package leave

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/store"
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

// DatabaseKinds are what the workspace's database keeps of a project (D337):
// its words (transcripts, names, notes), and the voice and face prints
// MetaRush knows people by (fingerprints of their voices and faces, made
// from the footage). Not footage or pictures: those are in the media store.
var DatabaseKinds = []string{models.LeaveVoices, models.LeaveFaces, models.LeaveWords}

// DatabaseHolds says what the database holds of a kind, in plain words.
func DatabaseHolds(kind string) string {
	switch kind {
	case models.LeaveVoices:
		return "the voice prints MetaRush knows people's voices by"
	case models.LeaveFaces:
		return "the face prints MetaRush knows people's faces by"
	}
	return "the transcripts, names, notes and other words"
}

// DatabaseKept is where the database keeps a workspace's words and the
// voice and face prints (DatabaseKinds).
func DatabaseKept(d config.Database) Kept {
	k := Kept{Kinds: DatabaseKinds}
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
		if slices.Contains(k.Kinds, models.LeaveWords) { // the database
			var held []string
			for _, kind := range k.Kinds {
				if rules.Keeps(models.PlaceStorage, kind) {
					held = append(held, DatabaseHolds(kind))
				}
			}
			out = append(out, fmt.Sprintf("This workspace keeps %s in %s, which these rules keep %s from. Use the database on this computer for this client (metarush database move --to here), or keep the client in a workspace of its own whose database is here.", joinAnd(held), k.Words, joinAnd(kept)))
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


// ArchiveDatabaseCheck is the database check (D337) for a restore: the
// archive's own clients' rules (no database holds its records yet; each
// reads one of its collections) against the database it would go into, before
// any record does. Nothing is recorded — the database holds nothing yet —
// and the refusal is said to the person restoring.
func ArchiveDatabaseCheck(to config.Database, each func(coll string, fn func(bson.Raw) error) error) error {
	k := DatabaseKept(to)
	if !k.Cloud {
		return nil
	}
	c := &checker{rules: map[string]*models.LeaveRules{}}
	if err := each(store.CContentSettings, func(raw bson.Raw) error {
		var rec models.ContentSettings
		if err := bson.Unmarshal(raw, &rec); err != nil {
			return err
		}
		if !rec.Leave.Empty() {
			c.rules[rec.ProjectID] = rec.Leave
		}
		return nil
	}); err != nil {
		return err
	}
	if len(c.rules) == 0 {
		return nil
	}
	var ps []content.ProjectChoice
	if err := each(store.CProjects, func(raw bson.Raw) error {
		var p content.ProjectChoice
		if err := bson.Unmarshal(raw, &p); err != nil {
			return err
		}
		ps = append(ps, p)
		return nil
	}); err != nil {
		return err
	}
	def := ""
	if err := each(store.CContentDefaults, func(raw bson.Raw) error {
		def, _ = raw.Lookup("projectId").StringValueOK()
		return nil
	}); err != nil {
		return err
	}
	c.g = content.FromRecords(def, ps)
	for _, pid := range append(c.g.Projects(), "") {
		s := Send{Place: models.PlaceStorage, To: "cloud", ToWords: k.Words, Kinds: k.Kinds, Projects: []string{pid}, What: "the workspace's database"}
		if r := c.problem(s); r != nil {
			heldWords(r)
			e := r.Err()
			e.Hint = "Restore it into the database on this computer (metarush setup --restore with the database on this computer) or one on your network; once it's restored, an owner can change the rules there. " + e.Hint
			return e
		}
	}
	return nil
}

// heldWords adds to a refusal what of the kind it keeps the database holds
// ("…: they don't go to cloud storage. The workspace's database holds the
// voice prints MetaRush knows people's voices by.").
func heldWords(r *Refusal) {
	if r.Kind != "" {
		r.Message += " The workspace's database holds " + DatabaseHolds(r.Kind) + "."
	}
}
