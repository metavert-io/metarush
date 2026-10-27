// Package store is the only code that talks to MongoDB. Every accessor is scoped
// by workspace ID; editorial revisions use optimistic concurrency and jobs use
// atomic find-and-modify leases.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// Collection names.
const (
	CWorkspaces    = "workspaces"
	CInstallations = "installations"
	CUsers         = "users"
	CProjects      = "projects"
	CAssets        = "assets"
	CIngest        = "ingest_sessions"
	CSegments      = "transcript_segments"
	CInsights      = "footage_insights"
	CRevisions     = "edit_revisions"
	CJobs          = "jobs"
	CWorkers       = "worker_devices"
	CRenders       = "renders"
	CComments      = "comments"
	CAudit         = "audit_events"
	CMigrations    = "schema_migrations"
	CQAReports     = "qa_reports"
	CVoiceTakes    = "voiceover_takes"
	CNotifications = "notifications"
	// CAssistantConversations holds dashboard-assistant conversations (titles,
	// turns, condensed transcripts); the Claude Code session itself stays on
	// the machine named in each document.
	CAssistantConversations = "assistant_conversations"
	// CMotion holds project motion graphics: one document per workspace and
	// component hash (what the check found, what people decided).
	CMotion = "motion_components"
	// CResources holds resources (images, SVGs, PDFs, fonts): one document per
	// resource with its content-addressed versions (D79).
	CResources = "resources"
	// CLinkedFolders holds linked folders (the per-machine path is in that
	// machine's config.yaml; D81).
	CLinkedFolders = "linked_folders"
	// CProjectLinks holds project links: a project using another's footage,
	// resources, motion graphics and cuts (D83).
	CProjectLinks = "project_links"
	// CCollections holds collections: named shelves of footage people create
	// (D96); membership is on the asset (collectionIds).
	CCollections = "collections"
	// CConversations holds conversations: recordings of one talk aligned on
	// one clock (D99). Cuts pin what they render with in their spec.
	CConversations = "conversations"
	// CGenerations holds generation records (D125): one per request to a
	// generative-video provider, written once and never changed.
	CGenerations = "generations"
	// CLooks holds looks: how a person or a character appears in generated
	// video (D126).
	CLooks = "looks"
	// CConsents holds consent records for making someone's likeness (D126).
	CConsents = "consents"
	// CGenSpend holds money spent and set aside for generative video, per
	// project and per UTC day (D124).
	CGenSpend = "generation_spend"
	// CRemotionSources holds Remotion sources: whole Remotion projects kept in
	// a project (D131).
	CRemotionSources = "remotion_sources"
	// CRemotionVersions holds their versions: one document per workspace and
	// manifest hash (what MetaRush read and checked, what people decided).
	CRemotionVersions = "remotion_versions"
	// CRemotionRenders holds the render cache: one document per workspace and
	// render key (D135).
	CRemotionRenders = "remotion_renders"
	// CRemotionRefreshes holds refreshes of Remotion sources (D173–D175): the
	// project's own steps a person started, what they made and printed.
	CRemotionRefreshes = "remotion_refreshes"
	// CRemotionPushes holds pushes back to a source's repository (D176).
	CRemotionPushes = "remotion_pushes"
	// CAbilities holds abilities (D230–D235): one document per workspace and
	// version (the SHA-256 of the folder's canonical archive), with what its
	// check found and what owners decided.
	CAbilities = "abilities"
	// CAbilitySetups holds what a person set up for an ability on each
	// computer: the programs it runs, pinned by SHA-256 (D231).
	CAbilitySetups = "ability_setups"
	// CAbilityRuns holds abilities' runs (D234): inputs, the requests they
	// made through MetaRush, what they made and what they cost.
	CAbilityRuns = "ability_runs"
	// CUserCredentials holds each account's password hash (D183): never in
	// users, never in an answer, a log or the audit.
	CUserCredentials = "user_credentials"
	// CAuthTokens holds invite and password-reset links by their SHA-256 (D183).
	CAuthTokens = "auth_tokens"
	// CAuthFailures counts wrong passwords per account for throttling (D183).
	CAuthFailures = "auth_failures"
	// CTranscriptFixes holds people's fixes to transcripts (D145): one per
	// changed stretch, never deleted (undo marks them).
	CTranscriptFixes = "transcript_fixes"
	// CTranscriptOriginals keeps each clip's transcript as the machine wrote
	// it, from just before the first fix (one per machine transcript).
	CTranscriptOriginals = "transcript_originals"
	// CTranscriptSnapshots holds the words saved versions of cuts were made
	// with, by content hash (D146).
	CTranscriptSnapshots = "transcript_snapshots"
	// CSpelling holds spelling lists: how names are always written, per
	// project or for the whole workspace (D147).
	CSpelling = "spelling_entries"
	// CCaptionTranslations holds caption lines in other languages, per
	// project and language (D238).
	CCaptionTranslations = "caption_translations"
	// CAutoSpending holds what Auto spent by itself: one record per change
	// that cost money and ran by itself (D241).
	CAutoSpending = "auto_spending"
	// CKeep holds what conversations offered to keep (D166): one record per
	// project and content, asked once, then kept or declined for good.
	CKeep = "keep_items"
	// CSourceCopies holds fixed copies (D152): one document per original and
	// fix steps (<workspace>/<original sha>:<fingerprint>), made once on any
	// machine.
	CSourceCopies = "source_copies"
	// CRecipes holds recipes (D201): one per recipe of the workspace (rcp_…),
	// with its current version, newest draft, example and whether it is archived.
	CRecipes = "recipes"
	// CRecipeVersions holds recipe versions: one document per workspace and
	// document hash (<workspace>/<sha256>), numbered with a parent.
	CRecipeVersions = "recipe_versions"
	// CRecipeRuns holds runs of recipes (rrn_…, D202–D204): the inputs as they
	// were, the agreed estimate, each step with what it made and cost.
	CRecipeRuns = "recipe_runs"
	// CDrafts holds what AI apps proposed (drf_…, D248): a version not in the
	// cut, or a change not yet made, awaiting a person's review.
	CDrafts = "drafts"
	// CPresenters holds people's own presenters kept at AI services
	// (prs_…, D273): made from a look, a training take and recorded consent.
	CPresenters = "presenters"
	// CTrainingOptOuts records that an AI service's training opt-out is set
	// for an account (D276), before a person's presenter is made there.
	CTrainingOptOuts = "training_optouts"
	// CRecordings holds screen recordings made in the dashboard (rcd_…,
	// D258–D260): the tracks (screen, camera, voice) on one clock, what each
	// became in the Library and the projects it is linked into.
	CRecordings = "recordings"
	// CSpokenChanges holds words changed in the speaker's own voice (D308).
	CSpokenChanges = "spoken_changes"
	// CReviewLinks holds links people sent to someone outside the workspace
	// (rvl_…, D314): one version's review copy, kept as a private page in
	// the media store until it expires or is taken back.
	CReviewLinks = "review_links"
)

// ErrNotFound is returned when a document does not exist in the workspace.
var ErrNotFound = errors.New("store: not found")

// DB wraps a connected database.
type DB struct {
	Client *mongo.Client
	DB     *mongo.Database
	Name   string
}

// Connect opens a MongoDB connection and pings it.
func Connect(ctx context.Context, uri, name string) (*DB, error) {
	if uri == "" {
		return nil, errors.New("MetaRush has no MongoDB database to use on this machine: set MetaRush up first, or set METARUSH_MONGODB_URI")
	}
	if name == "" {
		return nil, errors.New("MetaRush has no database name to use: set MetaRush up again")
	}
	opts := options.Client().ApplyURI(uri).SetAppName("metarush").SetServerSelectionTimeout(8 * time.Second).SetConnectTimeout(8 * time.Second)
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pctx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("MetaRush can't reach its MongoDB database: %w", err)
	}
	return &DB{Client: client, DB: client.Database(name), Name: name}, nil
}

// Close disconnects.
func (d *DB) Close(ctx context.Context) error { return d.Client.Disconnect(ctx) }

// C returns a collection.
func (d *DB) C(name string) *mongo.Collection { return d.DB.Collection(name) }

// ServerInfo reports server version and topology for doctor.
func (d *DB) ServerInfo(ctx context.Context) (version string, replicaSet bool, err error) {
	var res bson.M
	if err = d.DB.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&res); err != nil {
		return
	}
	_, replicaSet = res["setName"]
	var bi bson.M
	if err = d.Client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&bi); err == nil {
		version, _ = bi["version"].(string)
	} else {
		err = nil
	}
	return
}

// IsDuplicateKey reports a unique-index violation.
func IsDuplicateKey(err error) bool { return mongo.IsDuplicateKeyError(err) }

// GetByID loads one workspace-scoped document.
func (d *DB) GetByID(ctx context.Context, coll, workspaceID, id string, out any) error {
	err := d.C(coll).FindOne(ctx, bson.M{"_id": id, "workspaceId": workspaceID}).Decode(out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ErrNotFound
	}
	return err
}

// GetByIDAnyMember loads one workspace-scoped document like GetByID, from the
// primary or, while the replica set has none (an election, a maintenance
// step), from another member. The dashboard's media endpoints read this way
// (D180): a moment-old copy of a clip's record is fine for playing it.
func (d *DB) GetByIDAnyMember(ctx context.Context, coll, workspaceID, id string, out any) error {
	return d.FindOneAnyMember(ctx, coll, workspaceID, bson.M{"_id": id}, out)
}

// FindOneAnyMember is GetByIDAnyMember for the first document a
// workspace-scoped filter matches.
func (d *DB) FindOneAnyMember(ctx context.Context, coll, workspaceID string, filter bson.M, out any) error {
	f := bson.M{"workspaceId": workspaceID}
	for k, v := range filter {
		f[k] = v
	}
	c := d.DB.Collection(coll, options.Collection().SetReadPreference(readpref.PrimaryPreferred()))
	err := c.FindOne(ctx, f).Decode(out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ErrNotFound
	}
	return err
}

// Find runs a workspace-scoped query.
func (d *DB) Find(ctx context.Context, coll, workspaceID string, filter bson.M, out any, opts ...options.Lister[options.FindOptions]) error {
	if filter == nil {
		filter = bson.M{}
	}
	filter["workspaceId"] = workspaceID
	cur, err := d.C(coll).Find(ctx, filter, opts...)
	if err != nil {
		return err
	}
	return cur.All(ctx, out)
}

// Insert inserts a document.
func (d *DB) Insert(ctx context.Context, coll string, doc any) error {
	_, err := d.C(coll).InsertOne(ctx, doc)
	return err
}

// Update applies an update to one workspace-scoped document.
func (d *DB) Update(ctx context.Context, coll, workspaceID, id string, update bson.M) error {
	stamp(ctx, coll, update)
	res, err := d.C(coll).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": workspaceID}, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

type changerKey struct{}

// WithChanger says a person's request is changing things (D183): updates of
// projects and footage through Update record them as the last to change
// them. System work (workers, jobs) never sets it.
func WithChanger(ctx context.Context, actor any) context.Context {
	return context.WithValue(ctx, changerKey{}, actor)
}

// stamp adds updatedBy to a project's or a footage's change made for a person.
func stamp(ctx context.Context, coll string, update bson.M) {
	if coll != CProjects && coll != CAssets {
		return
	}
	who := ctx.Value(changerKey{})
	if who == nil {
		return
	}
	if set, ok := update["$set"].(bson.M); ok {
		if _, touched := set["updatedAt"]; touched {
			if _, given := set["updatedBy"]; !given {
				set["updatedBy"] = who
			}
		}
	}
}

// TestDBPrefix names databases the test suite may create and drop. Nothing else is
// ever dropped by MetaRush.
const TestDBPrefix = "metarush-test-"

// CTestRun holds a test database's one marker: the process that made it, on
// which computer and when (scripts/sweeptestdbs drops the ones whose process
// is gone).
const CTestRun = "test_run"

// TestRun is a test database's marker.
type TestRun struct {
	PID       int       `bson:"pid"`
	Host      string    `bson:"host"`
	StartedAt time.Time `bson:"startedAt"`
}

// MarkTestRun records which process made a test database.
func (d *DB) MarkTestRun(ctx context.Context) error {
	if !strings.HasPrefix(d.Name, TestDBPrefix) {
		return fmt.Errorf("refusing to mark %q: not a test database", d.Name)
	}
	host, _ := os.Hostname()
	_, err := d.C(CTestRun).InsertOne(ctx, TestRun{PID: os.Getpid(), Host: host, StartedAt: time.Now().UTC()})
	return err
}

// DropTestDatabase drops a test database, refusing anything that is not a local
// test database. This is the only drop in the codebase.
func (d *DB) DropTestDatabase(ctx context.Context, uri string) error {
	if !strings.HasPrefix(d.Name, TestDBPrefix) {
		return fmt.Errorf("refusing to drop %q: not a test database", d.Name)
	}
	if !localURI(uri) {
		return fmt.Errorf("refusing to drop %q: test databases are only dropped on localhost", d.Name)
	}
	return d.DB.Drop(ctx)
}

// IsLocalURI reports whether a MongoDB URI names only this computer
// (127.0.0.1 or localhost): the only place test databases are made and dropped.
func IsLocalURI(uri string) bool { return localURI(uri) }

func localURI(uri string) bool {
	rest := uri
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.Index(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[:i]
	}
	for _, h := range strings.Split(rest, ",") {
		host := h
		if i := strings.LastIndex(h, ":"); i >= 0 {
			host = h[:i]
		}
		if host != "127.0.0.1" && host != "localhost" {
			return false
		}
	}
	return rest != ""
}
