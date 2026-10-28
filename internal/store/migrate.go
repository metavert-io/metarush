package store

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Migration is one versioned, idempotent schema step. Migrations only ever add
// (indexes, fields) or rename fields in place (37); they never drop data.
type Migration struct {
	Version int
	Name    string
	Up      func(ctx context.Context, d *DB) error
}

func idx(keys bson.D, opts ...*options.IndexOptionsBuilder) mongo.IndexModel {
	m := mongo.IndexModel{Keys: keys}
	if len(opts) > 0 {
		m.Options = opts[0]
	}
	return m
}

func createIndexes(ctx context.Context, d *DB, coll string, models ...mongo.IndexModel) error {
	_, err := d.C(coll).Indexes().CreateMany(ctx, models)
	return err
}

// Migrations is the ordered list of schema versions.
var Migrations = []Migration{
	{Version: 1, Name: "core indexes", Up: func(ctx context.Context, d *DB) error {
		steps := []struct {
			coll   string
			models []mongo.IndexModel
		}{
			{CInstallations, []mongo.IndexModel{idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "lastSeenAt", Value: -1}})}},
			{CUsers, []mongo.IndexModel{idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "email", Value: 1}})}},
			{CProjects, []mongo.IndexModel{idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "updatedAt", Value: -1}})}},
			{CAssets, []mongo.IndexModel{
				// One asset per content hash per workspace: race-safe dedupe.
				idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "sha256", Value: 1}}, options.Index().SetUnique(true)),
				idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectIds", Value: 1}, {Key: "createdAt", Value: -1}}),
				idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}}),
			}},
			{CIngest, []mongo.IndexModel{idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}, {Key: "updatedAt", Value: -1}})}},
			{CSegments, []mongo.IndexModel{
				idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}, {Key: "idx", Value: 1}}),
				idx(bson.D{{Key: "text", Value: "text"}}),
			}},
			{CInsights, []mongo.IndexModel{idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}, {Key: "kind", Value: 1}}), idx(bson.D{{Key: "digest", Value: 1}})}},
			{CRevisions, []mongo.IndexModel{
				idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "number", Value: -1}}),
				idx(bson.D{{Key: "projectId", Value: 1}, {Key: "number", Value: 1}}, options.Index().SetUnique(true)),
				idx(bson.D{{Key: "parentId", Value: 1}}),
			}},
			{CJobs, []mongo.IndexModel{
				idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "idempotencyKey", Value: 1}}, options.Index().SetUnique(true)),
				// Queue scheduling: lease query scans by state + time.
				idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}, {Key: "nextRunAt", Value: 1}, {Key: "priority", Value: -1}}),
				idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}, {Key: "lease.expiresAt", Value: 1}}),
				idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "createdAt", Value: -1}}),
			}},
			{CWorkers, []mongo.IndexModel{idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "lastHeartbeat", Value: -1}})}},
			{CRenders, []mongo.IndexModel{idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "createdAt", Value: -1}}), idx(bson.D{{Key: "revisionId", Value: 1}})}},
			{CComments, []mongo.IndexModel{idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "createdAt", Value: -1}})}},
			{CAudit, []mongo.IndexModel{idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "at", Value: -1}}), idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "targetId", Value: 1}})}},
		}
		for _, s := range steps {
			if err := createIndexes(ctx, d, s.coll, s.models...); err != nil {
				return fmt.Errorf("%s indexes: %w", s.coll, err)
			}
		}
		return nil
	}},
	{Version: 2, Name: "qa reports", Up: func(ctx context.Context, d *DB) error {
		return createIndexes(ctx, d, CQAReports,
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "renderId", Value: 1}, {Key: "createdAt", Value: -1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "revisionId", Value: 1}, {Key: "createdAt", Value: -1}}))
	}},
	{Version: 3, Name: "notifications, duplicate precheck, job activity", Up: func(ctx context.Context, d *DB) error {
		if err := createIndexes(ctx, d, CNotifications,
			// One notification per outcome per person: the group key deduplicates.
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "userId", Value: 1}, {Key: "groupKey", Value: 1}}, options.Index().SetUnique(true)),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "userId", Value: 1}, {Key: "read", Value: 1}})); err != nil {
			return fmt.Errorf("%s indexes: %w", CNotifications, err)
		}
		// Duplicate precheck matches by size first, then fingerprint.
		if err := createIndexes(ctx, d, CAssets, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "size", Value: 1}})); err != nil {
			return fmt.Errorf("%s indexes: %w", CAssets, err)
		}
		// "File ready": all derivative jobs of an asset; activity: recent terminal jobs.
		return createIndexes(ctx, d, CJobs,
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}, {Key: "state", Value: 1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "finishedAt", Value: -1}}))
	}},
	{Version: 4, Name: "speakers, people and to-dos", Up: migrateSpeakersTodos},
	{Version: 5, Name: "assistant conversations", Up: func(ctx context.Context, d *DB) error {
		return createIndexes(ctx, d, CAssistantConversations,
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "archived", Value: 1}, {Key: "updatedAt", Value: -1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "updatedAt", Value: -1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "installationId", Value: 1}, {Key: "state", Value: 1}}))
	}},
	{Version: 6, Name: "voiceover takes", Up: func(ctx context.Context, d *DB) error {
		return createIndexes(ctx, d, CVoiceTakes,
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "beatId", Value: 1}, {Key: "createdAt", Value: -1}}),
			// TTS cache: unchanged lines are never billed twice.
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "cacheKey", Value: 1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}}))
	}},
	{Version: 7, Name: "activity log and improvements", Up: migrateLog},
	// v8–v11 belong to builds merged in parallel; migrations run every version
	// not yet recorded, whatever the merge order.
	{Version: 12, Name: "link imports", Up: func(ctx context.Context, d *DB) error {
		// "Is this video already a clip?" before downloading it again.
		return createIndexes(ctx, d, CAssets, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "source.platform", Value: 1}, {Key: "source.videoId", Value: 1}},
			options.Index().SetPartialFilterExpression(bson.M{"source.videoId": bson.M{"$exists": true}})))
	}},
	{Version: 13, Name: "on-screen index and faces", Up: func(ctx context.Context, d *DB) error {
		// Search finds clips by what is on screen (labels, text) and written scene
		// descriptions through their search words.
		if err := createIndexes(ctx, d, CInsights, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "kind", Value: 1}, {Key: "data.terms", Value: 1}})); err != nil {
			return fmt.Errorf("%s indexes: %w", CInsights, err)
		}
		return createIndexes(ctx, d, CFaces,
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}, {Key: "track", Value: 1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "personId", Value: 1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "candidate.personId", Value: 1}}))
	}},
	// v14 is reserved for a build merged in parallel.
	{Version: 15, Name: "resources, linked folders and project links", Up: migrateResources},
	// v16–v18 belong to builds merged in parallel.
	{Version: 19, Name: "collections, the Archive and the Trash", Up: migrateLibrary},
	{Version: 20, Name: "conversations", Up: func(ctx context.Context, d *DB) error {
		return createIndexes(ctx, d, CConversations,
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectIds", Value: 1}, {Key: "updatedAt", Value: -1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "members.assetId", Value: 1}}))
	}},
	// v21–v24 belong to builds merged in parallel.
	{Version: 25, Name: "generative video: generations, looks, consents and spending", Up: migrateGenerations},
	{Version: 26, Name: "Remotion sources: sources, versions and renders", Up: migrateRemotion},
	// v27–v28 belong to builds merged in parallel.
	{Version: 29, Name: "transcript fixes, kept machine transcripts, version snapshots and spelling lists", Up: migrateTranscriptFixes},
	{Version: 30, Name: "sources: descriptive names, fixed copies and analysis per media", Up: migrateSources},
	// v31 belongs to a build merged in parallel.
	{Version: 32, Name: "things a project keeps: what conversations offered to keep", Up: migrateKeep},
	// v33 belongs to a build merged in parallel.
	{Version: 34, Name: "Remotion sources: refreshes and pushes back", Up: migrateRemotionTier3},
	{Version: 35, Name: "accounts and sign-in: emails, credentials, invite and reset links, throttling", Up: migrateAccounts},
	{Version: 36, Name: "Content Settings: a project's brand, spelling list, narration voice and house rules, and the default for new projects", Up: migrateContentSettings},
	{Version: 37, Name: "clearer names: a fixed copy's fix steps and fingerprint, how a generated picture was made, a refresh's steps", Up: migrateClearerNames},
	{Version: 38, Name: "Recipes: recipes, their versions and runs", Up: migrateRecipes},
	{Version: 39, Name: "Recipes: runs found by the projects of their shorts", Up: migrateRecipePacks},
	{Version: 40, Name: "pictures kept from a repository's folders", Up: migrateRepoPictures},
	// v41 belongs to a build merged in parallel.
	{Version: 42, Name: "pictures fetched from Google Drive folders; versions of Recipes brought in, found by their lineage", Up: migrateDriveAndImports},
	{Version: 43, Name: "AI work of every kind in the generation records: listed by kind, found by the voice or picture it made", Up: migrateAIWork},
	{Version: 44, Name: "abilities: their versions, what each computer set up for them, their runs", Up: migrateAbilities},
	{Version: 45, Name: "captions in other languages; what Auto spent", Up: migrateTranslations},
	// v46 belongs to a build merged in parallel.
	{Version: 47, Name: "screen recordings made in the dashboard", Up: func(ctx context.Context, d *DB) error {
		return createIndexes(ctx, d, CRecordings,
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "createdAt", Value: -1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "createdAt", Value: -1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "links.projectId", Value: 1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "tracks.assetId", Value: 1}}))
	}},
	{Version: 48, Name: "drafts from AI apps awaiting review", Up: migrateDrafts},
	// v49–v50 belong to builds merged in parallel.
	{Version: 51, Name: "presenters: people's own presenters at AI services, and the services' training opt-outs", Up: migratePresenters},
	// v52–v57 belong to builds merged in parallel.
	{Version: 58, Name: "words changed in the speaker's own voice", Up: func(ctx context.Context, d *DB) error {
		return createIndexes(ctx, d, CSpokenChanges,
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "createdAt", Value: -1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}, {Key: "state", Value: 1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "generationId", Value: 1}}, options.Index().SetSparse(true)),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "consentId", Value: 1}}),
			// The sweep finds changes (and lip sync) left being made, by how long ago they moved.
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}, {Key: "updatedAt", Value: 1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "mouth.state", Value: 1}, {Key: "updatedAt", Value: 1}},
				options.Index().SetPartialFilterExpression(bson.M{"mouth.state": bson.M{"$exists": true}})))
	}},
	{Version: 59, Name: "review links for people outside the workspace, and the comments they leave", Up: migrateReviewLinks},
	{Version: 60, Name: "posts to video platforms still sending, found without reading every version", Up: func(ctx context.Context, d *DB) error {
		// The daemon looks every minute for posts whose sender stopped (D305):
		// only versions with one sending are in this index.
		return createIndexes(ctx, d, CRevisions,
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "publications.state", Value: 1}},
				options.Index().SetName("publications_sending").SetPartialFilterExpression(bson.M{"publications.state": "sending"})))
	}},
	{Version: 61, Name: "handing a computer's role to another computer", Up: func(ctx context.Context, d *DB) error {
		// One handover in progress per computer (D346): a second is refused
		// by the database, whichever computer starts it.
		return createIndexes(ctx, d, CHandovers,
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "from", Value: 1}},
				options.Index().SetName("handover_in_progress").SetUnique(true).SetPartialFilterExpression(bson.M{"active": true})),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "settledAt", Value: -1}}))
	}},
}

// migratePresenters indexes people's own presenters (D273: one per short
// name, a person's listed together, found by the service's ID when it is
// deleted) and the training opt-outs recorded per service (D276).
func migratePresenters(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CPresenters,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "name", Value: 1}}, options.Index().SetUnique(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "personId", Value: 1}, {Key: "createdAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "consentId", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "providerId", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "trainingId", Value: 1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CPresenters, err)
	}
	return createIndexes(ctx, d, CTrainingOptOuts,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "provider", Value: 1}, {Key: "recordedAt", Value: -1}}))
}

// migrateReviewLinks indexes review links (D314) per version and by state and
// expiry (the daemon looks at live ones), and comments by the link they came
// through (one comment per reviewer's browser and number, so bringing them
// in twice never doubles one).
func migrateReviewLinks(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CReviewLinks,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "createdAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "revisionId", Value: 1}, {Key: "createdAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projects", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}, {Key: "expiresAt", Value: 1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CReviewLinks, err)
	}
	if err := createIndexes(ctx, d, CComments,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "outside.linkId", Value: 1}, {Key: "outside.writer", Value: 1}, {Key: "outside.n", Value: 1}},
			options.Index().SetUnique(true).SetPartialFilterExpression(bson.M{"outside.linkId": bson.M{"$exists": true}}))); err != nil {
		return fmt.Errorf("%s indexes: %w", CComments, err)
	}
	// Every look asks what was made with consents that were withdrawn (D318):
	// the work that relied on one, what its presenters and voice copies made,
	// the takes in its voices and the pictures made from what it made.
	if err := createIndexes(ctx, d, CConsents, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "revokedAt", Value: 1}}, options.Index().SetSparse(true))); err != nil {
		return fmt.Errorf("%s indexes: %w", CConsents, err)
	}
	if err := createIndexes(ctx, d, CGenerations,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "consentIds", Value: 1}}, options.Index().SetSparse(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "options.presenter", Value: 1}}, options.Index().SetSparse(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "options.voice", Value: 1}}, options.Index().SetSparse(true))); err != nil {
		return fmt.Errorf("%s indexes: %w", CGenerations, err)
	}
	if err := createIndexes(ctx, d, CVoiceTakes, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "voiceId", Value: 1}}, options.Index().SetSparse(true))); err != nil {
		return fmt.Errorf("%s indexes: %w", CVoiceTakes, err)
	}
	return createIndexes(ctx, d, CResources, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "versions.source.lineage.resourceId", Value: 1}}, options.Index().SetSparse(true)))
}

// migrateAIWork indexes generation records by their kind of work (D226) and
// by the voice copy or picture one made (a voice in use is checked against
// the consent behind it; a picture's details find the work that made it).
// Records made before have no kind: they are video, and nothing is rewritten.
func migrateAIWork(ctx context.Context, d *DB) error {
	return createIndexes(ctx, d, CGenerations,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "kind", Value: 1}, {Key: "projectId", Value: 1}, {Key: "createdAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "outcome.outputs.voiceId", Value: 1}}, options.Index().SetSparse(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "outcome.outputs.resourceId", Value: 1}}, options.Index().SetSparse(true)))
}

// migrateAbilities adds abilities (D230–D235): versions numbered per name and
// listed newest first, one setup per ability and computer, runs listed per
// ability and found by their job.
func migrateAbilities(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CAbilities,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "name", Value: 1}, {Key: "number", Value: -1}}, options.Index().SetUnique(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "check.at", Value: -1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CAbilities, err)
	}
	if err := createIndexes(ctx, d, CAbilitySetups,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "name", Value: 1}, {Key: "installation", Value: 1}}, options.Index().SetUnique(true))); err != nil {
		return fmt.Errorf("%s indexes: %w", CAbilitySetups, err)
	}
	return createIndexes(ctx, d, CAbilityRuns,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "name", Value: 1}, {Key: "number", Value: -1}}, options.Index().SetUnique(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "jobId", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "createdAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}}, options.Index().SetSparse(true)))
}

// migrateTranslations indexes caption lines in other languages (D238): found
// by their words and by where they are said, per project and language; and
// what Auto spent by itself (D241): per project for the receipt, per person
// and day for the bell, and the generations its work made, by their job (what
// they settled at).
func migrateTranslations(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CCaptionTranslations,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "language", Value: 1}, {Key: "place", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "language", Value: 1}, {Key: "at", Value: -1}})); err != nil {
		return err
	}
	if err := createIndexes(ctx, d, CAutoSpending,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "at", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "forId", Value: 1}, {Key: "day", Value: 1}})); err != nil {
		return err
	}
	return createIndexes(ctx, d, CGenerations, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "jobId", Value: 1}}))

}

// migrateDrafts indexes what AI apps proposed (D248): the review desk lists
// the waiting ones newest first, an agent its own, a project its own.
func migrateDrafts(ctx context.Context, d *DB) error {
	return createIndexes(ctx, d, CDrafts,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}, {Key: "createdAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "agent", Value: 1}, {Key: "createdAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "createdAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "revisionId", Value: 1}},
			options.Index().SetPartialFilterExpression(bson.M{"revisionId": bson.M{"$exists": true}})))
}

// migrateDriveAndImports finds the picture a Google Drive file was fetched as
// (D220: one picture per Drive file, even when two runs fetch it at once) and
// the versions of Recipes a file from another workspace came from (D219: an
// import finds the one whose lineage it shares).
func migrateDriveAndImports(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CResources, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "driveFile.id", Value: 1}},
		options.Index().SetUnique(true).SetPartialFilterExpression(bson.M{"driveFile.id": bson.M{"$exists": true}}))); err != nil {
		return fmt.Errorf("%s indexes: %w", CResources, err)
	}
	return createIndexes(ctx, d, CRecipeVersions,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "imported.from", Value: 1}}, options.Index().SetSparse(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "imported.lineage", Value: 1}}, options.Index().SetSparse(true)))
}

// migrateRepoPictures finds the picture a repository's file was kept as
// (D216): one lookup per file every time the repository is fetched, and one
// picture per file even when two fetches keep it at once.
func migrateRepoPictures(ctx context.Context, d *DB) error {
	return createIndexes(ctx, d, CResources, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "repoFile.repoId", Value: 1}, {Key: "repoFile.path", Value: 1}},
		options.Index().SetUnique(true).SetPartialFilterExpression(bson.M{"repoFile": bson.M{"$exists": true}})))
}

// migrateAccounts adds accounts and sign-in (D183): one account per email in
// a workspace (people without an email are left out of the unique index),
// links found by their hash and purged a month after they expire, one failure
// count per account. The filter is a bson.D: two keys in a map come out in either
// order, and a template copy then differs from a fresh migration.
func migrateAccounts(ctx context.Context, d *DB) error {
	emailKeyed := options.Index().SetUnique(true).SetPartialFilterExpression(bson.M{"emailKey": bson.D{{Key: "$type", Value: "string"}, {Key: "$gt", Value: ""}}})
	if err := createIndexes(ctx, d, CUsers, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "emailKey", Value: 1}}, emailKeyed)); err != nil {
		return err
	}
	if err := createIndexes(ctx, d, CAuthTokens,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "userId", Value: 1}}),
		idx(bson.D{{Key: "purgeAt", Value: 1}}, options.Index().SetExpireAfterSeconds(0))); err != nil {
		return err
	}
	return createIndexes(ctx, d, CAuthFailures, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "emailKey", Value: 1}}, options.Index().SetUnique(true)))
}

// migrateKeep adds the record of what conversations offered to keep (D166):
// one per project and content key, so nothing is asked twice.
func migrateKeep(ctx context.Context, d *DB) error {
	return createIndexes(ctx, d, CKeep,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "key", Value: 1}}, options.Index().SetUnique(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "conversationId", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "askedAt", Value: -1}}))
}

// migrateSources adds fixed copies (D152) and the index behind the Library's
// "Better names for N clips" (D150).
func migrateSources(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CSourceCopies,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}, {Key: "createdAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "resourceId", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "sha256", Value: 1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CSourceCopies, err)
	}
	return createIndexes(ctx, d, CAssets, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "nameSuggestion.at", Value: -1}},
		options.Index().SetPartialFilterExpression(bson.M{"nameSuggestion": bson.M{"$exists": true}})))
}

// migrateRemotionTier3 adds the records of refreshes (D173–D175) and pushes
// back (D176): listed per source, newest first, and found by their job.
func migrateRemotionTier3(ctx context.Context, d *DB) error {
	for _, c := range []string{CRemotionRefreshes, CRemotionPushes} {
		if err := createIndexes(ctx, d, c,
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "sourceId", Value: 1}, {Key: "number", Value: -1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "jobId", Value: 1}})); err != nil {
			return fmt.Errorf("%s indexes: %w", c, err)
		}
	}
	return nil
}

// migrateRemotion adds Remotion sources (D131), their versions and the render
// cache (D135). Clips made from them are found through their provenance.
func migrateRemotion(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CRemotionSources,
		// One short name per project.
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "name", Value: 1}}, options.Index().SetUnique(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "updatedAt", Value: -1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CRemotionSources, err)
	}
	if err := createIndexes(ctx, d, CRemotionVersions,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "sourceId", Value: 1}, {Key: "number", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}, {Key: "updatedAt", Value: -1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CRemotionVersions, err)
	}
	if err := createIndexes(ctx, d, CRemotionRenders,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "sourceId", Value: 1}, {Key: "version", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CRemotionRenders, err)
	}
	// Clips made from a source, by their provenance.
	return createIndexes(ctx, d, CAssets, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "source.remotion.sourceId", Value: 1}},
		options.Index().SetPartialFilterExpression(bson.M{"source.remotion.sourceId": bson.M{"$exists": true}})))
}

// migrateTranscriptFixes adds people's fixes to transcripts (D145), the kept
// machine transcripts, the snapshots saved versions use (D146) and spelling
// lists (D147). Existing transcripts and versions are left as they are: a
// version saved before has no pins and uses each clip's machine transcript.
func migrateTranscriptFixes(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CTranscriptFixes,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}, {Key: "rev", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "batchId", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "at", Value: -1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CTranscriptFixes, err)
	}
	if err := createIndexes(ctx, d, CTranscriptOriginals,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}, {Key: "base", Value: 1}}, options.Index().SetUnique(true))); err != nil {
		return fmt.Errorf("%s indexes: %w", CTranscriptOriginals, err)
	}
	if err := createIndexes(ctx, d, CTranscriptSnapshots,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "hash", Value: 1}}, options.Index().SetUnique(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CTranscriptSnapshots, err)
	}
	return createIndexes(ctx, d, CSpelling,
		// One entry per word and list (the workspace's list has no project).
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "key", Value: 1}}, options.Index().SetUnique(true)))
}

// migrateGenerations adds generation records (D125), looks and consents
// (D126). The spending ledger (D124) is keyed by its own IDs.
func migrateGenerations(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CGenerations,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "createdAt", Value: -1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "groupId", Value: 1}, {Key: "take", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "outcome.outputs.assetId", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "look.id", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "requestHash", Value: 1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CGenerations, err)
	}
	if err := createIndexes(ctx, d, CLooks,
		// One look per short name.
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "name", Value: 1}}, options.Index().SetUnique(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "personId", Value: 1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CLooks, err)
	}
	return createIndexes(ctx, d, CConsents,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "personId", Value: 1}, {Key: "recordedAt", Value: -1}}))
}

// migrateLibrary adds collections (D96) and the indexes behind the Library's
// views: collections' items, the Archive and the Trash (D97, D98).
func migrateLibrary(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CCollections,
		// One collection per name (in any case).
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "key", Value: 1}}, options.Index().SetUnique(true))); err != nil {
		return fmt.Errorf("%s indexes: %w", CCollections, err)
	}
	return createIndexes(ctx, d, CAssets,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "collectionIds", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "archivedAt", Value: -1}}, options.Index().SetPartialFilterExpression(bson.M{"archivedAt": bson.M{"$exists": true}})),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "trash.at", Value: 1}}, options.Index().SetPartialFilterExpression(bson.M{"trash": bson.M{"$exists": true}})))
}

// migrateResources adds the resources library (D79), linked folders (D81)
// and project links (D83).
func migrateResources(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CResources,
		// One name per scope: a project's "logo" and the workspace's "logo" can coexist.
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "scope", Value: 1}, {Key: "projectId", Value: 1}, {Key: "name", Value: 1}}, options.Index().SetUnique(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "versions.sha256", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "folder.folderId", Value: 1}, {Key: "folder.relPath", Value: 1}}),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "updatedAt", Value: -1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CResources, err)
	}
	if err := createIndexes(ctx, d, CLinkedFolders, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}})); err != nil {
		return fmt.Errorf("%s indexes: %w", CLinkedFolders, err)
	}
	return createIndexes(ctx, d, CProjectLinks,
		// One link per pair of projects.
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}, {Key: "sourceId", Value: 1}}, options.Index().SetUnique(true)),
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "sourceId", Value: 1}}))
}

// SchemaVersion is the latest migration version.
func SchemaVersion() int { return Migrations[len(Migrations)-1].Version }

// AppliedVersion returns the highest applied migration.
func (d *DB) AppliedVersion(ctx context.Context) (int, error) {
	var doc struct {
		ID int `bson:"_id"`
	}
	err := d.C(CMigrations).FindOne(ctx, bson.M{}, options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return 0, nil
	}
	return doc.ID, err
}

// appliedVersions returns every recorded migration version.
func (d *DB) appliedVersions(ctx context.Context) (map[int]bool, error) {
	cur, err := d.C(CMigrations).Find(ctx, bson.M{}, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		ID int `bson:"_id"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for _, d := range docs {
		out[d.ID] = true
	}
	return out, nil
}

// PendingMigrations lists the versions that are not recorded yet.
func (d *DB) PendingMigrations(ctx context.Context) ([]int, error) {
	done, err := d.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, m := range Migrations {
		if !done[m.Version] {
			out = append(out, m.Version)
		}
	}
	return out, nil
}

// Migrate applies pending migrations in order. It is safe to run concurrently from
// several installations: each step is idempotent and recorded once. Every
// version that is not recorded yet runs — not only versions above the highest —
// so a database that already has a later version still gets an earlier one
// that arrived with a later merge.
func (d *DB) Migrate(ctx context.Context) (applied []string, err error) {
	// A database a restore still holds is never brought up to date by
	// anyone but that restore (D333).
	if err := d.CheckNoUnfinishedRestore(ctx); err != nil {
		return nil, err
	}
	// Nor a database a move keeps changes out of: being copied, or moved
	// (D335).
	if err := d.CheckNotMoving(ctx); err != nil {
		return nil, err
	}
	return d.migrate(ctx)
}

// MigrateRestoring is Migrate for the restore that holds the database: it
// brings the copy up to date once every record is in and checked.
func (d *DB) MigrateRestoring(ctx context.Context) ([]string, error) { return d.migrate(ctx) }

func (d *DB) migrate(ctx context.Context) (applied []string, err error) {
	done, err := d.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range Migrations {
		if done[m.Version] {
			continue
		}
		if err := m.Up(ctx, d); err != nil {
			return applied, fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
		}
		_, err := d.C(CMigrations).InsertOne(ctx, bson.M{"_id": m.Version, "name": m.Name, "appliedAt": time.Now().UTC()})
		if err != nil && !IsDuplicateKey(err) {
			return applied, err
		}
		applied = append(applied, fmt.Sprintf("%d %s", m.Version, m.Name))
	}
	return applied, nil
}
