package mcp

import (
	"slices"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
)

// The tool lists a server offers (D222). A client's model chooses better
// from a few tools than from hundreds, so the server offers a small core by
// default: find, describe, run and run_change reach every operation (and any
// catalogue that joins find), and a few everyday tools are offered directly.
// A toolset offers one kind of work as direct tools, related operations
// merged into one tool that takes a mode. "all" is one tool per operation, as
// before (tests, and clients that want every operation as a tool).
//
// No tool both reads and changes: clients ask a person per tool (Claude
// Code's permission rules and Claude Desktop's "always allow" are by tool
// name), so a person can allow every reading tool and keep each change on
// ask. run only reads and run_change only changes; a group of operations is
// split into a tool for its reads (the group's name) and one for its changes
// (<name>_change, offered only with changes allowed).

// Toolset names.
const (
	ToolsetCore    = "core"
	ToolsetReview  = "review"
	ToolsetEdit    = "edit"
	ToolsetLibrary = "library"
	ToolsetAdmin   = "admin"
	ToolsetAll     = "all"
)

// Limits the guards hold every list to (D222).
const (
	// MaxCoreTools is the most tools the core offers, changes allowed.
	MaxCoreTools = 12
	// MaxToolsetTools is the most tools a toolset offers, changes allowed.
	MaxToolsetTools = 25
)

// The core's own tools: they reach every entry of the catalogue.
const (
	toolFind      = "find"
	toolDescribe  = "describe"
	toolRun       = "run"
	toolRunChange = "run_change"
)

// changeSuffix names the half of a group that changes things.
const changeSuffix = "_change"

// toolDef is one tool of a list: an operation ("project.show"), a group of
// operations offered as the modes of a tool for its reads and one for its
// changes, or one of the core's own tools.
type toolDef struct {
	name string
	// about says what the group's reads are for, changeAbout what its
	// changes are for (for one operation, about is a shorter description
	// than its summary).
	about, changeAbout string
	// ops are its operations: one for a direct tool; for a group,
	// "mode=operation", or the operation alone (its mode is its name after
	// the group's name: "footage.moments" in footage is "moments").
	ops []string
	// special is find, describe, run or run_change.
	special string
	// only: offered only on connections of this kind (draft_list when
	// proposing).
	only Access
}

func one(op string) toolDef { return toolDef{ops: []string{op}} }

// shorter is an operation as a tool with a shorter description than its
// summary.
func shorter(op, about string) toolDef { return toolDef{ops: []string{op}, about: about} }

// group is operations offered as modes: the reads in a tool named name, the
// changes in name_change.
func group(name, about, changeAbout string, ops ...string) toolDef {
	return toolDef{name: name, about: about, changeAbout: changeAbout, ops: ops}
}

var reach = []toolDef{{special: toolFind}, {special: toolDescribe}, {special: toolRun}, {special: toolRunChange},
	// A propose-only connection's own drafts and what people decided (D248).
	{ops: []string{"draft.list"}, about: "Your drafts and what a person decided: approved, Not quite with what should change (make a new draft with replaces), or turned down", only: Propose}}

// toolsets are the lists by name, "all" aside.
var toolsets = map[string][]toolDef{
	ToolsetCore: append(slices.Clone(reach),
		one("activity.summary"), one("project.list"), one("project.show"),
		shorter("search", "Search footage and the workspace: what was said (times, who), what's on screen, people, clips, projects, versions, comments and resources; best first"),
		one("cut.show"), one("cut.check"),
		shorter("render.submit", "Make a review copy or the final video of a version: queues a render (not done until its state is available)"),
		one("qa.show")),

	ToolsetReview: append(slices.Clone(reach), one("activity.summary"), one("search"),
		group("project", "Projects: list them, open one, what happened so far and the projects it uses", "",
			"project.list", "project.show", "project.sofar", "project.links"),
		group("footage", "Look at and listen to footage: transcripts, moments, pauses and scene changes, contact sheets, frames, what's on screen, voices, faces and picture switches", "",
			"footage.transcript", "footage.moments", "footage.evidence", "footage.sheet", "footage.frames", "footage.visuals",
			"footage.keyframes", "footage.speakers", "footage.faces", "footage.switches"),
		group("transcript", "Words in transcripts: find them, a clip's fixes, and fixes waiting for a new version", "",
			"transcript.find", "transcript.history", "transcript.pending"),
		group("asset", "Clips: list them, one clip, its info card and where it is used", "",
			"asset.list", "asset.show", "asset.card", "asset.where"),
		group("revision", "Versions of a cut: list, open and compare them, and those awaiting review", "Ask for a review of a version, or change a version's note",
			"revision.list", "revision.show", "revision.compare", "waiting=review.waiting", "revision.askReview", "revision.note"),
		group("cut", "Cuts as cut sheets: read one, compare two versions, compare an edited sheet, check it, cuts that play this one, its sign-offs", "",
			"cut.show", "cut.compare", "cut.diff", "cut.check", "cut.where", "signoffs=cut.signoffs.list"),
		group("render", "Renders and their quality checks: list them, one render's progress, read its check", "Make a review copy or the final video, cancel a render, run its quality check again",
			"render.list", "render.status", "check=qa.show", "render.submit", "render.cancel", "checkAgain=qa.run"),
		group("comment", "Review comments: read them", "Add a review comment", "comment.list", "comment.add"),
		group("captions", "Captions: check them before a video goes out, the caption at a frame, a captions file", "",
			"captions.check", "captions.at", "captions.file"),
		group("people", "People MetaRush knows: list them, one person", "", "people.list", "people.show"),
		group("todos", "Questions MetaRush needs a person to answer: list them, one question, the questions about faces", "",
			"todos.list", "todos.show", "todos.groups"),
		group("sound", "A cut's sound and narration in plain words: the sound, the mix, clearer voices compared, the narration and whether it fits", "",
			"sound.show", "mix=mix.show", "compare=sound.compare", "narration=voiceover.show", "fit=voiceover.fit"),
		group("conversation", "Conversations from online calls: list them, one conversation, its best moments for shorts", "",
			"conversation.list", "conversation.show", "conversation.moments"),
	),

	ToolsetEdit: append(slices.Clone(reach), one("activity.summary"), one("search"),
		group("project", "Projects: list them, open one", "", "project.list", "project.show"),
		group("cut", "A cut as a cut sheet (text): read it, check an edited sheet, compare it",
			"Change the latest cut, each change checked first (dryRun, or a sheet's check): save a checked sheet or edit plan, add, move, trim, split or delete clips, take out a stretch or words, transitions, framing, flashes, name bars, nested cuts, a version's note",
			"cut.show", "cut.check", "cut.diff", "cut.compare", "cut.apply", "editApply=edit.apply",
			"cut.add", "cut.move", "cut.trim", "cut.split", "cut.delete", "cut.deleteRange", "cut.removeWords", "cut.transition",
			"cut.framing", "cut.flashes", "cut.names", "cut.nest", "cut.update", "cut.useFixed", "cut.rename", "note=revision.note"),
		group("edit", "Edit plans: check one (it returns the confirm token), preview it, the graphics templates and transitions", "",
			"edit.check", "edit.preview", "edit.templates", "edit.transitions"),
		group("revision", "Versions: list, open and compare them", "", "revision.list", "revision.show", "revision.compare"),
		group("footage", "Footage to edit with: transcripts, moments, frames, contact sheets and what's on screen", "",
			"footage.transcript", "footage.moments", "footage.frames", "footage.sheet", "footage.visuals"),
		group("transcript", "Words in transcripts: find them, a clip's fix history, fixes waiting for a new version",
			"Fix words in transcripts: retype, replace everywhere, who says a stretch, undo, the new version fixes wait for",
			"transcript.find", "transcript.history", "transcript.pending",
			"transcript.fix", "transcript.replace", "transcript.speaker", "transcript.undo", "pendingApply=transcript.pending.apply"),
		group("voiceover", "Narration: show it, check it fits, the AI voice's price and voices, a script as lines, a take on a line",
			"Change the narration: add or change a line, free scratch audio, fit a script, choose the fitted or original script",
			"voiceover.show", "voiceover.fit", "voiceover.estimate", "voiceover.voices", "voiceover.parse", "voiceover.choose",
			"voiceover.line", "voiceover.scratch", "voiceover.askFit", "voiceover.fitted", "voiceover.use"),
		group("sound", "A cut's sound and mix: show them, hear a change first, clearer voices compared",
			"Change a cut's sound: levels, music added or changed, clearer voices, the mix",
			"sound.show", "sound.preview", "sound.compare", "mix=mix.show", "mixPreview=mix.preview",
			"sound.levels", "musicAdd=sound.music.add", "musicSet=sound.music.set", "sound.enhance", "mixSet=mix.set"),
		group("render", "Renders and their checks: a render's progress, list them, read its quality check",
			"Make a review copy or the final video, or run a render's quality check again",
			"render.status", "render.list", "check=qa.show", "render.submit", "checkAgain=qa.run"),
		group("brand", "The brand: show it, suggest one from a website, the house style",
			"Change the brand, use it in the latest cut, make the latest cut follow the house style",
			"brand.show", "brand.suggest", "style=content.style.show", "brand.set", "brand.apply", "styleApply=content.style.apply"),
		group("motion", "The project's own motion graphics: list, one version, start one", "Check a motion graphic (records its version for a person to approve)",
			"motion.list", "motion.show", "motion.new", "motion.check"),
	),

	ToolsetLibrary: append(slices.Clone(reach), one("activity.summary"), one("search"), one("library.list"),
		group("asset", "Clips in the Library: list, one clip, its info card, where it is used, better names, fixes",
			"Change clips: move or add to a project, archive, trash, restore, rename, take better names, tick fixes, music rights, a copy's name",
			"asset.list", "asset.show", "asset.card", "asset.where", "asset.names", "asset.fixes",
			"asset.move", "asset.link", "asset.archive", "asset.unarchive", "asset.trash", "asset.restore", "asset.update",
			"namesAccept=asset.names.accept", "fixesSet=asset.fixes.set", "asset.rights", "asset.duplicate"),
		group("collection", "Collections (named shelves for footage): list them, one", "Make a collection, put clips on it or take them off, rename or delete it",
			"collection.list", "collection.show", "collection.create", "collection.add", "collection.remove", "collection.rename", "collection.delete"),
		group("add", "Before adding: where files and links would go, copies already here, uploads in progress",
			"Add files, folders and links the way the Add box does, or import files and videos from links",
			"route=add.route", "precheck=asset.precheck", "sessions=ingest.sessions",
			"paths=add.paths", "links=add.links", "files=ingest.files", "url=ingest.url"),
		group("footage", "Footage: transcripts, voices and faces", "Analyze footage again",
			"footage.transcript", "footage.speakers", "footage.faces", "footage.analyze"),
		group("resources", "Pictures, logos, PDFs, fonts, repositories and web pages: list, one, where used, its file here, linked folders",
			"Add, bring in, make, change, remove, refresh or move resources to the library",
			"resources.list", "resources.show", "resources.where", "resources.path", "resources.folders",
			"resources.add", "resources.fetch", "resources.make", "resources.update", "resources.remove", "resources.promote", "resources.refresh"),
		group("people", "People: list them, one person", "Rename people, combine two, change notes, separate a voice, say two are different",
			"people.list", "people.show", "people.rename", "people.merge", "people.update", "people.split", "people.distinct"),
		group("todos", "Questions MetaRush needs a person to answer: list, one, counts", "Answer or skip a question",
			"todos.list", "todos.show", "todos.counts", "todos.answer", "todos.dismiss"),
		group("project", "Projects: list, open, the projects it uses", "Create a project, rename it or change its brief, use another project's things or stop",
			"project.list", "project.show", "project.links", "project.create", "project.update", "project.link", "project.use", "project.unlink"),
		group("generate", "Generated video: what was made, how one was made, price a shot, the settings", "",
			"generate.list", "generate.show", "generate.estimate", "generate.settings"),
	),

	ToolsetAdmin: append(slices.Clone(reach), one("activity.summary"),
		group("workspace", "The setup: the workspace, versions, what is on this computer, remote access, sign-in, people, the audit trail, machines, workers, storage, the database", "",
			"show=workspace.show", "version=version.show", "sync=sync.status", "remote=remote.status", "auth=auth.status",
			"users=users.list", "audit=audit.list", "machines=machine.list", "rekeyPlan=machine.rekeyPlan",
			"workers=worker.list", "battery=worker.settings", "storage=storage.status", "storagePlan=storage.plan",
			"database=database.move.status", "databasePlan=database.plan", "databaseLate=database.late.writes"),
		group("job", "Jobs: list them, one job and why it waits", "Cancel or retry a job, or run queued jobs now",
			"job.list", "job.show", "job.cancel", "job.retry", "job.expedite"),
		group("cache", "This computer's copies of footage: how much room they take, download for offline work, clear the least used, repair", "",
			"cache.status", "cache.prefetch", "cache.prune", "cache.repair"),
		group("log", "The activity log: read it, search it, one entry, export it, its settings, whether Claude Code sessions are recorded", "",
			"log.list", "log.search", "log.show", "log.export", "log.settings", "hooks=log.hooks.status"),
		group("notification", "Your notifications", "Mark notifications read", "notification.list", "notification.read"),
		group("provider", "Outside services and their keys on this computer: list, one, test a key", "Save or remove a key (owners)",
			"provider.list", "provider.status", "provider.test", "provider.set", "provider.remove"),
		group("assistant", "Claude Code on this computer: ready or not, its settings and models, the conversations", "",
			"assistant.status", "assistant.settings", "assistant.models", "assistant.list", "assistant.show"),
		group("improve", "What MetaRush should do better: signals in the log, the ideas, one idea, export them", "",
			"improve.signals", "improve.list", "improve.show", "improve.export"),
		group("watch", "The watched folder: how it stands", "Set the watched folder or stop watching (owners)", "watch.status", "watch.set", "watch.disable"),
		group("ingest", "Uploads and link imports: sessions, the downloader on this computer", "Clean up unfinished uploads",
			"ingest.sessions", "urlTools=ingest.url.tools", "ingest.reconcile"),
	),
}

// Toolsets lists the names --toolset takes.
func Toolsets() []string {
	names := []string{ToolsetAll}
	for n := range toolsets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// toolsetDefs is a toolset's list, or an error naming the ones there are.
func toolsetDefs(name string) ([]toolDef, error) {
	if name == "" {
		name = ToolsetCore
	}
	defs, ok := toolsets[name]
	if !ok {
		return nil, apperr.New(apperr.InvalidInput, "There is no toolset %q: the toolsets are %s.", name, strings.Join(Toolsets(), ", ")).
			WithDetails(map[string]any{"toolset": name})
	}
	return defs, nil
}

// modeOf splits "mode=operation", or names the mode after the tool's name.
func modeOf(tool, spec string) (mode, op string) {
	if m, o, ok := strings.Cut(spec, "="); ok {
		return m, o
	}
	return strings.TrimPrefix(spec, tool+"."), spec
}
