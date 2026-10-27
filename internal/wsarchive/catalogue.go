// Package wsarchive exports a workspace into an archive and restores one into
// an empty database (D329–D333, docs/workspace-export.md): one file per
// collection in canonical MongoDB Extended JSON v2, one document per line,
// streamed both ways, with a manifest that names the MetaRush that made it,
// the database's migration level, the workspace, the counts and a SHA-256 for
// every file. The media is either referenced (content addresses only) or
// included (each object copied in by its content address and checked).
//
// Export only reads: it never migrates, creates an index, starts a job or
// writes anything to the database it reads, so it works on a database at an
// older migration level and with a read-only database user. Restore writes
// only into an empty database, after every checksum is verified, then sets
// the migration level from the manifest and runs the migrations forward.
//
// The pieces are a library: Source reads a database at any level, the copy
// engine (copy.go) streams a collection's structure and documents and checks
// them by hash, so moving a database (copying it to another one and checking
// the copy before anything switches) uses the same code.
package wsarchive

import (
	"sort"

	"github.com/metavert-io/metarush/internal/store"
)

// How a collection goes into an archive.
const (
	// AsIs: every document of the workspace, exactly as stored.
	AsIs = "as-is"
	// Transformed: every document of the workspace, with the changes its
	// entry says (a credential taken out, work that was running put back as
	// waiting).
	Transformed = "transformed"
	// LeftOut: nothing of it goes in (credentials, one computer's own state,
	// test markers).
	LeftOut = "left-out"
	// InManifest: what it says is in manifest.json (the migration level).
	InManifest = "in-manifest"
)

// Entry is what an archive does with one collection, and why, in plain words.
type Entry struct {
	How string `json:"how" jsonschema:"enum=as-is,enum=transformed,enum=left-out,enum=in-manifest"`
	Why string `json:"why"`
	// transform changes one document (only for Transformed).
	transform transformFunc
}

// Catalogue is every collection MetaRush keeps, with what an archive does with
// it (D330). A test keeps it whole: a new collection must be classified here,
// so none is ever left out without a reason. Collections a database holds that
// MetaRush doesn't know are left out and named in the manifest (never silently).
var Catalogue = map[string]Entry{
	store.CWorkspaces:             {How: AsIs, Why: "the workspace itself: its name, its settings and where its footage is kept (never a password or a key)"},
	store.CInstallations:          {How: AsIs, Why: "the workspace's computers as history: their names, systems and MetaRush versions, and the names (never the secrets) of their storage keys and database users; a computer that restores the archive joins as a new one"},
	store.CUsers:                  {How: Transformed, Why: "people: names, emails, roles, pictures and preferences; passwords are not in the archive, so “password set” is cleared and people set one again", transform: clearPasswordSet},
	store.CProjects:               {How: AsIs, Why: "projects: names, briefs, members and settings"},
	store.CAssets:                 {How: AsIs, Why: "footage: each clip's record, where its bytes are kept and what was learned from it"},
	store.CIngest:                 {How: Transformed, Why: "upload records; an unfinished upload's resumable address is taken out (it allows writing into the storage)", transform: dropUploadSession},
	store.CSegments:               {How: AsIs, Why: "transcripts: every clip's words with their times"},
	store.CInsights:               {How: AsIs, Why: "what MetaRush learned from the footage (scenes, pauses, what is on screen)"},
	store.CRevisions:              {How: AsIs, Why: "every version of every cut"},
	store.CJobs:                   {How: Transformed, Why: "work: what finished is history; what was running, and paid work that was waiting, is held for a person (a copy beside a running original never does it twice); other work asked for on one computer still waits for that computer", transform: holdJob},
	store.CWorkers:                {How: LeftOut, Why: "each computer's live record of what MetaRush can do there right now (its programs, its battery): written again when MetaRush starts on a computer"},
	store.CRenders:                {How: Transformed, Why: "renders and where their files are kept; one being made is held with its work", transform: holdRender},
	store.CComments:               {How: AsIs, Why: "comments on versions, outside reviewers' included"},
	store.CAudit:                  {How: AsIs, Why: "the change history: who changed what, and when"},
	store.CMigrations:             {How: InManifest, Why: "the database's migration level: written in manifest.json and set from it by the restore, which then brings the copy up to date"},
	store.CQAReports:              {How: AsIs, Why: "quality checks of renders"},
	store.CVoiceTakes:             {How: AsIs, Why: "narration takes and what was spent on them"},
	store.CNotifications:          {How: AsIs, Why: "each person's notifications"},
	store.CAssistantConversations: {How: Transformed, Why: "Claude Code conversations: their text, cards and decisions (the Claude Code session itself stays on the computer that held it); one answering at the time is marked as waiting", transform: idleConversation},
	store.CMotion:                 {How: AsIs, Why: "motion graphics, their checks and who approved them"},
	store.CResources:              {How: AsIs, Why: "pictures, logos, fonts, PDFs, repositories and web pages, with where each came from"},
	store.CLinkedFolders:          {How: AsIs, Why: "linked folders' shared records (where each folder is on a computer stays in that computer's settings)"},
	store.CProjectLinks:           {How: AsIs, Why: "projects using each other's things"},
	store.CCollections:            {How: AsIs, Why: "collections in the Library"},
	store.CConversations:          {How: AsIs, Why: "call recordings lined up on one clock"},
	store.CGenerations:            {How: AsIs, Why: "AI work that was asked for, what it made and cost (written once)"},
	store.CLooks:                  {How: AsIs, Why: "looks of people and characters"},
	store.CConsents:               {How: AsIs, Why: "consent records, whole, as the proof they are"},
	store.CGenSpend:               {How: AsIs, Why: "what was spent on AI work, per project and day"},
	store.CRemotionSources:        {How: AsIs, Why: "Remotion projects kept in projects"},
	store.CRemotionVersions:       {How: AsIs, Why: "Remotion projects' versions and who approved them"},
	store.CRemotionRenders:        {How: AsIs, Why: "Remotion renders already made"},
	store.CRemotionRefreshes:      {How: AsIs, Why: "Remotion refreshes people started"},
	store.CRemotionPushes:         {How: AsIs, Why: "Remotion changes pushed back to repositories"},
	store.CAbilities:              {How: AsIs, Why: "abilities, their checks and owners' approvals"},
	store.CAbilitySetups:          {How: LeftOut, Why: "what each computer set up for an ability (programs and files on that computer, pinned there): set up again on the new computer"},
	store.CAbilityRuns:            {How: AsIs, Why: "abilities' runs: what each sent, made and cost"},
	store.CUserCredentials:        {How: LeftOut, Why: "passwords (kept as hashes): credentials never go into an archive; people set a password again"},
	store.CAuthTokens:             {How: LeftOut, Why: "invitation and password-reset links: credentials; owners send new ones"},
	store.CAuthFailures:           {How: LeftOut, Why: "counts of wrong passwords, kept a short while to slow guessing: about sign-ins to this database only"},
	store.CTranscriptFixes:        {How: AsIs, Why: "people's fixes to transcripts"},
	store.CTranscriptOriginals:    {How: AsIs, Why: "transcripts as the machine wrote them, before the first fix"},
	store.CTranscriptSnapshots:    {How: AsIs, Why: "the words saved versions were made with"},
	store.CSpelling:               {How: AsIs, Why: "spelling lists"},
	store.CCaptionTranslations:    {How: AsIs, Why: "captions in other languages"},
	store.CAutoSpending:           {How: AsIs, Why: "what Auto spent by itself"},
	store.CKeep:                   {How: AsIs, Why: "what projects kept from conversations"},
	store.CSourceCopies:           {How: AsIs, Why: "fixed copies of footage"},
	store.CContentSettings:        {How: AsIs, Why: "Content Settings: brands, spelling lists, voices, house rules and what may leave"},
	store.CContentDefaults:        {How: AsIs, Why: "which Content Settings new projects use"},
	store.CRecipes:                {How: AsIs, Why: "recipes, with their triggers (a trigger still works only on the computer it was turned on at)"},
	store.CRecipeVersions:         {How: AsIs, Why: "recipes' versions"},
	store.CRecipeRuns:             {How: AsIs, Why: "recipes' runs: what each made and spent (a run goes on only on the computer that started it)"},
	store.CDrafts:                 {How: AsIs, Why: "what AI apps proposed, waiting for a person or decided"},
	store.CPresenters:             {How: AsIs, Why: "people's presenters at AI services and their states"},
	store.CTrainingOptOuts:        {How: AsIs, Why: "AI services' training opt-outs that were set"},
	store.CRecordings:             {How: AsIs, Why: "screen recordings and what each became (pieces not yet made into footage stay on the computer that received them)"},
	store.CSpokenChanges:          {How: AsIs, Why: "words changed in a speaker's own voice, as disclosed"},
	store.CReviewLinks:            {How: AsIs, Why: "links sent to outside reviewers, as the record of what was shared"},
	store.CLogEntries:             {How: AsIs, Why: "the activity log (written without keys or passwords)"},
	store.CImprovementItems:       {How: AsIs, Why: "gaps and friction the weekly report found"},
	store.CImprovementReports:     {How: AsIs, Why: "the weekly reports"},
	store.CSpeakers:               {How: AsIs, Why: "voices heard in each clip"},
	store.CPeople:                 {How: AsIs, Why: "named people and their voice and face profiles"},
	store.CTodos:                  {How: AsIs, Why: "questions for people and how they were answered"},
	store.CFaces:                  {How: AsIs, Why: "faces followed through clips"},
	store.CTestRun:                {How: LeftOut, Why: "a test database's marker"},
	store.CRestoreLock:            {How: LeftOut, Why: "the marker a restore keeps in the database it writes into: that copy's own state"},
	store.CDatabaseMove:           {How: LeftOut, Why: "the marker a move of the database leaves in the database it moved from: that database's own state"},
	"catch_ups":                   {How: AsIs, Why: "which catch-up steps already ran (so the copy doesn't run them again)"},
	"sound_cache":                 {How: AsIs, Why: "clearer voices already made (made once, kept by content address)"},
}

// Exported reports whether a collection's documents go into an archive.
func (e Entry) Exported() bool { return e.How == AsIs || e.How == Transformed }

// Names lists the catalogue's collections in order.
func Names() []string {
	out := make([]string, 0, len(Catalogue))
	for n := range Catalogue {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Classification is the catalogue as a list (for the docs, the plan and --json).
type Classification struct {
	Name string `json:"name"`
	How  string `json:"how" jsonschema:"enum=as-is,enum=transformed,enum=left-out,enum=in-manifest"`
	Why  string `json:"why"`
}

// Classify lists every collection with what an archive does with it.
func Classify() []Classification {
	out := make([]Classification, 0, len(Catalogue))
	for _, n := range Names() {
		e := Catalogue[n]
		out = append(out, Classification{Name: n, How: e.How, Why: e.Why})
	}
	return out
}
