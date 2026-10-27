package ops

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"sync"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/store"
)

// Clearing a project out for good (D196) deletes its renders' stored files —
// each render's video and picture, and the frames and sheets of its quality
// checks — only when nothing else uses the same bytes. Stored files are
// content-addressed (a file is its SHA-256; a key names it), so another
// record can hold the very same file: footage imported from a render, a
// picture kept from a check, another project's identical render, a file
// someone dropped into a conversation. Every collection MetaRush keeps is in
// purgeCatalogue with what clearing out a project does to it and how it can
// use stored files (a test keeps the catalogue whole); every one that can is
// checked, and any doubt — a query that fails, a key that doesn't name its
// file — keeps the file.

// How a collection uses stored files.
const (
	mediaNone    = ""        // it never holds stored files
	mediaPaths   = "paths"   // the fields in mediaFields hold them (large collections, read through indexes)
	mediaScan    = "scan"    // every document's bytes are searched for the files (any field, nested anywhere)
	mediaHistory = "history" // it names files only as a record of what happened (the audit trail, the log, jobs)
)

// purgeEntry is what clearing out a project does to a collection, and how
// the collection uses stored files.
type purgeEntry struct {
	// Purge says it in words (for the docs and the test that keeps the
	// catalogue whole).
	Purge string
	Media string
	// Project is the field naming a document's project (or projects): the
	// project's own documents go with it, so they don't keep its files.
	Project string
}

// purgeCatalogue is every collection MetaRush keeps (D196).
var purgeCatalogue = map[string]purgeEntry{
	store.CWorkspaces:             {Purge: "kept", Media: mediaScan},
	store.CInstallations:          {Purge: "kept", Media: mediaNone},
	store.CUsers:                  {Purge: "kept", Media: mediaScan},
	store.CProjects:               {Purge: "the project itself goes, last; the other projects stay", Media: mediaScan, Project: "_id"},
	store.CAssets:                 {Purge: "footage is never deleted: it leaves the project (back to the Inbox when nothing else has it; into a project whose cut uses it through a link)", Media: mediaPaths},
	store.CIngest:                 {Purge: "kept (upload records)", Media: mediaScan},
	store.CSegments:               {Purge: "kept (footage's words)", Media: mediaNone},
	store.CInsights:               {Purge: "kept (footage's analysis)", Media: mediaNone},
	store.CRevisions:              {Purge: "its versions go", Media: mediaNone, Project: "projectId"}, // they pin files other records hold (footage, resources, fixed copies)
	store.CJobs:                   {Purge: "its waiting work is called off; finished work stays as history", Media: mediaHistory, Project: "projectId"},
	store.CWorkers:                {Purge: "kept", Media: mediaNone},
	store.CRenders:                {Purge: "its renders go, and their files when nothing else uses them", Media: mediaPaths, Project: "projectId"},
	store.CComments:               {Purge: "its comments go", Media: mediaNone, Project: "projectId"},
	store.CAudit:                  {Purge: "kept (the audit trail)", Media: mediaHistory},
	store.CMigrations:             {Purge: "kept", Media: mediaNone},
	store.CQAReports:              {Purge: "its renders' quality checks go, and their frames when nothing else uses them", Media: mediaPaths, Project: "projectId"},
	store.CVoiceTakes:             {Purge: "kept (narration takes are footage and what was spent on them)", Media: mediaScan},
	store.CNotifications:          {Purge: "notifications about it go", Media: mediaNone, Project: "projectId"},
	store.CAssistantConversations: {Purge: "its Claude Code conversations go, with their saved proposals", Media: mediaPaths, Project: "projectId"},
	store.CMotion:                 {Purge: "motion graphics stay in the Library (no longer marked as the project's)", Media: mediaScan},
	store.CResources:              {Purge: "its pictures and files move to the workspace library", Media: mediaPaths},
	store.CLinkedFolders:          {Purge: "its linked folders become the workspace library's", Media: mediaNone},
	store.CProjectLinks:           {Purge: "its links go, both ways (what other projects' cuts use stays theirs)", Media: mediaNone},
	store.CCollections:            {Purge: "kept", Media: mediaNone},
	store.CConversations:          {Purge: "call recordings stay in the Library (no longer in the project)", Media: mediaScan},
	store.CRecordings:             {Purge: "screen recordings stay in the Library (no longer linked into the project)", Media: mediaNone},
	store.CGenerations:            {Purge: "kept (what was spent)", Media: mediaScan},
	store.CLooks:                  {Purge: "kept", Media: mediaScan},
	store.CConsents:               {Purge: "kept", Media: mediaScan},
	store.CPresenters:             {Purge: "kept (a person's presenter is the workspace's, like their look)", Media: mediaScan},
	store.CTrainingOptOuts:        {Purge: "kept", Media: mediaNone},
	store.CGenSpend:               {Purge: "kept (spending)", Media: mediaNone},
	store.CRemotionSources:        {Purge: "its Remotion projects go (their stored files stay)", Media: mediaScan, Project: "projectId"},
	store.CRemotionVersions:       {Purge: "its Remotion projects' versions go (their stored files stay)", Media: mediaScan, Project: "projectId"},
	store.CRemotionRenders:        {Purge: "its Remotion renders go (the clips they made stay as footage)", Media: mediaScan, Project: "projectId"},
	store.CRemotionRefreshes:      {Purge: "its Remotion refreshes go", Media: mediaScan, Project: "projectId"},
	store.CRemotionPushes:         {Purge: "its Remotion pushes go", Media: mediaScan, Project: "projectId"},
	store.CAbilities:              {Purge: "kept (abilities are the workspace's)", Media: mediaScan},
	store.CAbilitySetups:          {Purge: "kept (what each computer set up)", Media: mediaNone},
	store.CAbilityRuns:            {Purge: "kept (what each run sent, made and cost); what it made stays like any footage or picture", Media: mediaScan},
	store.CUserCredentials:        {Purge: "kept", Media: mediaNone},
	store.CAuthTokens:             {Purge: "kept", Media: mediaNone},
	store.CAuthFailures:           {Purge: "kept", Media: mediaNone},
	store.CTranscriptFixes:        {Purge: "kept (fixes are footage's words)", Media: mediaNone},
	store.CTranscriptOriginals:    {Purge: "kept", Media: mediaNone},
	store.CTranscriptSnapshots:    {Purge: "kept (shared by versions with the same words)", Media: mediaNone},
	store.CSpelling:               {Purge: "its Content Settings' spelling list goes", Media: mediaNone, Project: "projectId"},
	store.CCaptionTranslations:    {Purge: "its captions in other languages go", Media: mediaNone, Project: "projectId"},
	store.CAutoSpending:           {Purge: "kept (what Auto spent)", Media: mediaNone},
	store.CKeep:                   {Purge: "what it kept goes", Media: mediaScan, Project: "projectId"},
	store.CSourceCopies:           {Purge: "kept (footage's fixed copies)", Media: mediaScan},
	store.CContentSettings:        {Purge: "its Content Settings go; projects that followed them use the default and are told", Media: mediaScan, Project: "projectId"},
	store.CContentDefaults:        {Purge: "cleared when its settings were the default", Media: mediaNone},
	store.CRecipes:                {Purge: "kept (recipes are the workspace's); one that adds cuts to it says so when checked or planned, and its example goes", Media: mediaScan},
	store.CRecipeVersions:         {Purge: "kept (a recipe's history)", Media: mediaScan},
	store.CRecipeRuns:             {Purge: "kept (what each run was, made and spent); its runs are marked as cleared out, and one still going is stopped", Media: mediaScan},
	store.CDrafts:                 {Purge: "the drafts AI apps proposed for it go", Media: mediaNone, Project: "projectId"},
	store.CSpokenChanges:          {Purge: "its records of words changed in a speaker's own voice go (the new sounds are clips: they stay like footage)", Media: mediaNone, Project: "projectId"},
	store.CReviewLinks:            {Purge: "its review links end and their pages and copies are deleted from the media store; the records of what was shared stay", Media: mediaNone},
	store.CLogEntries:             {Purge: "kept (the activity log)", Media: mediaHistory},
	store.CImprovementItems:       {Purge: "kept", Media: mediaNone},
	store.CImprovementReports:     {Purge: "kept", Media: mediaScan},
	store.CSpeakers:               {Purge: "kept (footage's voices)", Media: mediaNone},
	store.CPeople:                 {Purge: "kept", Media: mediaScan},
	store.CTodos:                  {Purge: "its own to-dos go; questions about footage stay", Media: mediaNone, Project: "projectId"},
	store.CFaces:                  {Purge: "kept (footage's faces)", Media: mediaNone},
	store.CTestRun:                {Purge: "kept", Media: mediaNone},
	"catch_ups":                   {Purge: "kept", Media: mediaNone},
	"sound_cache":                 {Purge: "kept (clearer voices are footage's)", Media: mediaScan},
}

// mediaFields are the fields of the large collections that hold stored
// files: a SHA-256 (sha), a key (keys) or a map of variants keyed by kind
// (maps, each {kind: {sha256}}).
var mediaFields = map[string]struct{ shas, keys, maps []string }{
	store.CAssets: {shas: []string{"sha256", "fixes.copy.sha256", "captions.sha256"}, keys: []string{"storageKey", "fixes.copy.storageKey", "captions.storageKey"},
		maps: []string{"variants", "fixes.originalVariants"}},
	store.CResources: {shas: []string{"versions.sha256", "versions.originalSha256", "versions.thumbnail.sha256", "versions.pages.sha256",
		"versions.site.preview.sha256", "versions.site.icon.sha256", "versions.site.textObject.sha256", "versions.site.brand.logos.sha256", "fixes.copy.sha256"},
		keys: []string{"versions.storageKey", "fixes.copy.storageKey"}, maps: []string{"fixes.originalVariants"}},
	store.CRenders:                {shas: []string{"output.sha256"}, keys: []string{"output.storageKey", "output.thumbnailKey"}},
	store.CQAReports:              {shas: []string{"renderSha256", "frames.sha256", "contactSheet.sha256", "momentsSheet.sha256"}, keys: []string{"frames.key", "contactSheet.key", "momentsSheet.key"}},
	store.CAssistantConversations: {shas: []string{"turns.attachments.sha256", "queue.attachments.sha256", "events.attachments.sha256"}},
}

// MediaUsers reports which stored files (by SHA-256) something outside the
// given projects still uses, where MetaRush's database can't show it (a
// Remotion project's files are listed in a stored manifest). A feature that
// keeps files that way registers one (RegisterMediaUsers).
type MediaUsers func(ctx context.Context, a *app.App, shas []string, exceptProjects []string) (map[string]bool, error)

var (
	mediaUsersMu sync.Mutex
	mediaUsers   = map[string]MediaUsers{}
)

// RegisterMediaUsers adds a check of stored files used outside the database.
func RegisterMediaUsers(name string, f MediaUsers) {
	mediaUsersMu.Lock()
	defer mediaUsersMu.Unlock()
	mediaUsers[name] = f
}

// purgeObject is a stored file a cleared-out project's renders hold.
type purgeObject struct {
	Key  string
	SHA  string
	Size int64
}

// purgeRenderObjects lists the stored files of a project's renders and of their
// quality checks (frames and sheets), by SHA-256.
func purgeRenderObjects(ctx context.Context, a *app.App, projectID string) (map[string]purgeObject, error) {
	out := map[string]purgeObject{}
	add := func(key, sha string, size int64) {
		if key == "" {
			return
		}
		if sha == "" {
			sha = shaOfKey(key)
		}
		if sha == "" || media.ObjectKey(a.WorkspaceID(), sha) != key {
			return // a key that doesn't name its file: kept
		}
		if o, ok := out[sha]; !ok || o.Size == 0 {
			out[sha] = purgeObject{Key: key, SHA: sha, Size: size}
		}
	}
	var renders []struct {
		Output *struct {
			StorageKey   string `bson:"storageKey"`
			SHA256       string `bson:"sha256"`
			Size         int64  `bson:"size"`
			ThumbnailKey string `bson:"thumbnailKey"`
		} `bson:"output"`
	}
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"projectId": projectID}, &renders, options.Find().SetProjection(bson.M{"output": 1})); err != nil {
		return nil, err
	}
	for _, r := range renders {
		if r.Output != nil {
			add(r.Output.StorageKey, r.Output.SHA256, r.Output.Size)
			add(r.Output.ThumbnailKey, "", 0)
		}
	}
	type frame struct {
		Key    string `bson:"key"`
		SHA256 string `bson:"sha256"`
		Size   int64  `bson:"size"`
	}
	var reports []struct {
		Frames       []frame `bson:"frames"`
		ContactSheet *frame  `bson:"contactSheet"`
		MomentsSheet *frame  `bson:"momentsSheet"`
	}
	if err := a.DB.Find(ctx, store.CQAReports, a.WorkspaceID(), bson.M{"projectId": projectID}, &reports,
		options.Find().SetProjection(bson.M{"frames.key": 1, "frames.sha256": 1, "frames.size": 1, "contactSheet": 1, "momentsSheet": 1})); err != nil {
		return nil, err
	}
	for _, r := range reports {
		for _, f := range r.Frames {
			add(f.Key, f.SHA256, f.Size)
		}
		for _, f := range []*frame{r.ContactSheet, r.MomentsSheet} {
			if f != nil {
				add(f.Key, f.SHA256, f.Size)
			}
		}
	}
	return out, nil
}

// shaOfKey is the SHA-256 a content-addressed key names ("" when it isn't one).
func shaOfKey(key string) string {
	s := path.Base(key)
	if isHex64(s) {
		return s
	}
	return ""
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHexByte(s[i]) {
			return false
		}
	}
	return true
}

func isHexByte(c byte) bool { return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') }

// mediaInUse says which of the files (by SHA-256) something other than the
// projects being cleared out uses. An error means it couldn't tell: keep them all.
func mediaInUse(ctx context.Context, a *app.App, objs map[string]purgeObject, except []string) (map[string]bool, error) {
	used := map[string]bool{}
	if len(objs) == 0 {
		return used, nil
	}
	shas := make([]string, 0, len(objs))
	keys := make([]string, 0, len(objs))
	for sha, o := range objs {
		shas = append(shas, sha)
		keys = append(keys, o.Key)
	}
	sort.Strings(shas)
	names := make([]string, 0, len(purgeCatalogue))
	for c := range purgeCatalogue {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, coll := range names {
		e := purgeCatalogue[coll]
		var err error
		switch e.Media {
		case mediaPaths:
			err = usedThroughFields(ctx, a, coll, e.Project, shas, keys, except, used)
		case mediaScan:
			err = usedAnywhere(ctx, a, coll, e.Project, objs, except, used)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", coll, err)
		}
	}
	mediaUsersMu.Lock()
	fs := make([]MediaUsers, 0, len(mediaUsers))
	for _, f := range mediaUsers {
		fs = append(fs, f)
	}
	mediaUsersMu.Unlock()
	for _, f := range fs {
		got, err := f(ctx, a, shas, except)
		if err != nil {
			return nil, err
		}
		for sha := range got {
			used[sha] = true
		}
	}
	return used, nil
}

// notTheirs is the filter for documents that aren't the projects' being cleared out.
func notTheirs(field string, except []string) bson.M {
	if field == "" || len(except) == 0 {
		return bson.M{}
	}
	return bson.M{field: bson.M{"$nin": except}}
}

// usedThroughFields reads a large collection through its file fields.
func usedThroughFields(ctx context.Context, a *app.App, coll, projectField string, shas, keys, except []string, used map[string]bool) error {
	f := mediaFields[coll]
	var or bson.A
	for _, p := range f.shas {
		or = append(or, bson.M{p: bson.M{"$in": shas}})
	}
	for _, p := range f.keys {
		or = append(or, bson.M{p: bson.M{"$in": keys}})
	}
	for _, m := range f.maps {
		// {kind: {sha256}}: MongoDB can't index a map's values, so the map is
		// turned into an array of its values first.
		vals := bson.M{"$map": bson.M{"input": bson.M{"$objectToArray": bson.M{"$ifNull": bson.A{"$" + m, bson.M{}}}}, "as": "v", "in": "$$v.v.sha256"}}
		or = append(or, bson.M{"$expr": bson.M{"$gt": bson.A{bson.M{"$size": bson.M{"$setIntersection": bson.A{vals, shas}}}, 0}}})
	}
	if len(or) == 0 {
		return errors.New("no fields to read")
	}
	filter := notTheirs(projectField, except)
	filter["workspaceId"] = a.WorkspaceID()
	filter["$or"] = or
	cur, err := a.DB.C(coll).Find(ctx, filter)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	want := map[string]bool{}
	for _, s := range shas {
		want[s] = true
	}
	for cur.Next(ctx) {
		markHashes(cur.Current, want, used) // the matching document holds one or more of them
	}
	return cur.Err()
}

// usedAnywhere searches every document of a collection (but the projects'
// own) for the files' SHA-256s, in any field: a key names its file's hash
// too. Every workspace's documents count (a document without a workspace
// field is never missed; another workspace's use only keeps a file).
func usedAnywhere(ctx context.Context, a *app.App, coll, projectField string, objs map[string]purgeObject, except []string, used map[string]bool) error {
	filter := notTheirs(projectField, except)
	cur, err := a.DB.C(coll).Find(ctx, filter, options.Find().SetBatchSize(200))
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	want := map[string]bool{}
	for s := range objs {
		want[s] = true
	}
	for cur.Next(ctx) {
		markHashes(cur.Current, want, used)
	}
	return cur.Err()
}

// markHashes marks every wanted SHA-256 that appears in a document's bytes
// (any run of 64 or more lower-case hex characters is looked at in each
// 64-character window).
func markHashes(doc []byte, want, used map[string]bool) {
	run := 0
	for i := 0; i <= len(doc); i++ {
		if i < len(doc) && isHexByte(doc[i]) {
			run++
			continue
		}
		if run >= 64 {
			start := i - run
			for j := start; j+64 <= i; j++ {
				if s := string(doc[j : j+64]); want[s] {
					used[s] = true
				}
			}
		}
		run = 0
	}
}
