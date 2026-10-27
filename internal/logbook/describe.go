package logbook

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Names are the current display names of the objects entries refer to, so a
// row reads "Saved version 3 of Spring promo" even for old entries.
type Names struct {
	Projects         map[string]string
	Assets           map[string]string
	Revisions        map[string]int
	RevisionProjects map[string]string // revision → its project
	Renders          map[string]RenderName
	Conversations    map[string]string
	Machines         map[string]string
}

// RenderName is what a render row needs.
type RenderName struct {
	ProjectID      string
	Quality        string
	RevisionNumber int
}

// LoadNames looks up the names for a page of entries (one query per collection).
func LoadNames(ctx context.Context, db *store.DB, ws string, entries []Entry) *Names {
	n := &Names{Projects: map[string]string{}, Assets: map[string]string{}, Revisions: map[string]int{}, RevisionProjects: map[string]string{},
		Renders: map[string]RenderName{}, Conversations: map[string]string{}, Machines: map[string]string{}}
	if db == nil {
		return n
	}
	set := func(m map[string]bool, id string) {
		if id != "" {
			m[id] = true
		}
	}
	prj, ast, rev, rnd, conv := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		set(prj, e.Refs.ProjectID)
		set(ast, e.Refs.AssetID)
		set(rev, e.Refs.RevisionID)
		set(rnd, e.Refs.RenderID)
		set(conv, e.Refs.ConversationID)
	}
	keys := func(m map[string]bool) bson.A {
		out := bson.A{}
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	find := func(coll string, ids map[string]bool, proj bson.M, each func(raw bson.M)) {
		if len(ids) == 0 {
			return
		}
		cur, err := db.C(coll).Find(ctx, bson.M{"workspaceId": ws, "_id": bson.M{"$in": keys(ids)}}, options.Find().SetProjection(proj))
		if err != nil {
			return
		}
		var docs []bson.M
		if cur.All(ctx, &docs) != nil {
			return
		}
		for _, d := range docs {
			each(d)
		}
	}
	str := func(d bson.M, k string) string { s, _ := d[k].(string); return s }
	num := func(d bson.M, k string) int {
		switch v := d[k].(type) {
		case int32:
			return int(v)
		case int64:
			return int(v)
		case float64:
			return int(v)
		}
		return 0
	}
	find(store.CRenders, rnd, bson.M{"projectId": 1, "quality": 1, "revisionNumber": 1}, func(d bson.M) {
		r := RenderName{ProjectID: str(d, "projectId"), Quality: str(d, "quality"), RevisionNumber: num(d, "revisionNumber")}
		n.Renders[str(d, "_id")] = r
		set(prj, r.ProjectID)
	})
	find(store.CRevisions, rev, bson.M{"number": 1, "projectId": 1}, func(d bson.M) {
		n.Revisions[str(d, "_id")] = num(d, "number")
		n.RevisionProjects[str(d, "_id")] = str(d, "projectId")
		set(prj, str(d, "projectId"))
	})
	find(store.CProjects, prj, bson.M{"name": 1}, func(d bson.M) { n.Projects[str(d, "_id")] = str(d, "name") })
	find(store.CAssets, ast, bson.M{"name": 1, "title": 1}, func(d bson.M) {
		t := str(d, "title")
		if t == "" {
			t = str(d, "name")
		}
		n.Assets[str(d, "_id")] = t
	})
	find(store.CAssistantConversations, conv, bson.M{"title": 1}, func(d bson.M) { n.Conversations[str(d, "_id")] = str(d, "title") })
	var insts []bson.M
	if cur, err := db.C(store.CInstallations).Find(ctx, bson.M{"workspaceId": ws}, options.Find().SetProjection(bson.M{"name": 1})); err == nil && cur.All(ctx, &insts) == nil {
		for _, d := range insts {
			n.Machines[str(d, "_id")] = str(d, "name")
		}
	}
	return n
}

func (n *Names) project(id string) string {
	if n != nil && n.Projects[id] != "" {
		return n.Projects[id]
	}
	return "a project"
}

func (n *Names) asset(id string) string {
	if n != nil && n.Assets[id] != "" {
		return "“" + n.Assets[id] + "”"
	}
	return "a clip"
}

// Describe is an entry's row text, in plain words (no IDs, flags or paths).
func Describe(e *Entry, n *Names, opSummary string) string {
	if n == nil {
		n = &Names{}
	}
	var s string
	switch e.Kind {
	case KindOp:
		s = describeOp(e, n, opSummary)
	case KindOpSummary:
		c := e.Counts
		if c == nil {
			return e.Summary
		}
		s = fmt.Sprintf("%s %s from the dashboard", opPhrase(e, n, opSummary, true), times(c.Count))
	case KindJob:
		s = describeJob(e, n)
	case KindTool:
		s = describeTool(e)
	case KindClaude:
		s = describeClaude(e, n)
	case KindAction:
		s = describeAction(e)
	case KindEvent:
		s = describeEvent(e, n)
	case KindProvider:
		s = describeProvider(e)
	default:
		s = e.Summary
	}
	return capFirst(s)
}

// numOf reads a whole number decoded from BSON or JSON.
func numOf(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return fmt.Sprintf("%d times", n)
}

// FormatDuration says how long something took, for people.
func FormatDuration(ms float64) string {
	switch {
	case ms <= 0:
		return ""
	case ms < 1:
		return "under 1 ms"
	case ms < 1000:
		return fmt.Sprintf("%.0f ms", ms)
	case ms < 10_000:
		return fmt.Sprintf("%.1f s", ms/1000)
	case ms < 60_000:
		return fmt.Sprintf("%.0f s", ms/1000)
	case ms < 3_600_000:
		m := int(ms / 60_000)
		sec := int(math.Mod(ms, 60_000) / 1000)
		if sec == 0 {
			return fmt.Sprintf("%d min", m)
		}
		return fmt.Sprintf("%d min %d s", m, sec)
	}
	return fmt.Sprintf("%.1f h", ms/3_600_000)
}

// opBase are operations in plain words, in the base form ("save version 3 of
// Spring promo"); {p} is the project, {a} the clip, {r} the version number, {q}
// the render quality, {n} a count.
var opBase = map[string]string{
	"project.create": "create the project {p}", "project.list": "list the projects", "project.show": "open {p}", "project.update": "change {p}",
	"project.delete": "delete {p}", "project.restore": "restore {p}", "project.purge": "clear archived projects out for good",
	"asset.list": "list clips", "asset.show": "look at {a}", "asset.update": "rename {a}", "asset.link": "add {a} to {p}",
	"asset.duplicate": "sort out a copy of {a}", "asset.rights": "record where the music in {a} comes from", "asset.precheck": "check for copies before uploading",
	"ingest.files": "import footage into {p}", "ingest.sessions": "list uploads", "ingest.reconcile": "clean up unfinished uploads",
	"footage.analyze": "start analyzing the footage", "footage.transcript": "read the transcript of {a}", "footage.evidence": "read the scene analysis of {a}",
	"footage.moments": "look for strong moments", "footage.frames": "take stills from {a}", "footage.sheet": "make a frame grid of {a}",
	"footage.speakers": "look at who speaks in {a}", "footage.speakers.rename": "name a voice in {a}", "footage.speakers.hint": "say how many people speak in {a}", "footage.language": "look at which language {a} is spoken in", "footage.captions": "look at the captions kept with {a}", "captions.looks": "look at the caption looks", "footage.short": "make a short in {p}", "cut.captions": "change how the captions of {p} look", "footage.language.set": "set the language {a} is spoken in and write its words down again",
	"footage.speakers.reject": "correct a voice in {a}", "footage.speakers.samples": "save listening samples of {a}",
	"footage.speakers.assign": "fix who said a line in {a}", "footage.speakers.unmerge": "separate two voices in {a}",
	"footage.speakers.detect": "listen for the voices in the footage",
	"footage.keyframes":       "choose frames to show from {a}", "footage.visuals": "read what's on screen in {a}",
	"footage.visuals.status": "check whether clips get looked at for what's on screen",
	"footage.faces":          "look at the faces in {a}", "footage.faces.name": "name a face in {a}", "footage.faces.reject": "correct a face in {a}",
	"footage.describe": "describe the scenes of {a}", "footage.describe.settings": "check the scene description settings",
	"footage.describe.configure": "change the scene description settings",
	"edit.check":                 "check a change to {p}", "edit.apply": "save a new version of {p}", "edit.templates": "list the motion-graphics templates",
	"revision.list": "list the versions of {p}", "revision.show": "open version {r} of {p}", "revision.compare": "compare two versions of {p}",
	"revision.plan": "prepare version {r} of {p} for playback", "revision.approve": "approve version {r} of {p}",
	"render.submit": "start a {q} render of {p}, version {r}", "render.status": "check a render of {p}", "render.list": "list renders",
	"render.cancel": "cancel a render of {p}", "render.fetch": "download a render of {p}", "qa.run": "start a quality check of {p}, version {r}",
	"qa.show": "read a quality check of {p}", "deliver.export": "export finished videos of {p}",
	"job.list": "list jobs", "job.show": "look at a job", "job.cancel": "cancel a job", "job.retry": "retry a job", "job.expedite": "ask jobs to run now",
	"ops.find": "find what does a task", "ops.describe": "read how to use an operation",
	"catalogue.list": "list what MetaRush can do and what works on this computer", "catalogue.show": "look at what an ability needs",
	"comment.add": "add a comment to {p}", "comment.list": "read the comments on {p}", "search": "search the footage", "activity.summary": "check what's running",
	"notification.list": "read notifications", "notification.read": "mark notifications read", "todos.list": "list questions", "todos.show": "look at a question",
	"todos.answer": "answer a question", "todos.dismiss": "skip a question", "todos.counts": "count open questions", "todos.sync": "look for new questions",
	"todos.groups": "list who is on screen to name", "todos.acceptShown": "use the names shown in {a}",
	"todos.notPerson": "say the faces asked about aren't someone", "todos.skipFaces": "skip the questions about faces",
	"footage.faces.notHere": "say someone isn't on screen in {a}", "people.evidence": "check how often the hints about people were right",
	"people.learned": "look at what {who}'s voice and face are learned from", "people.unlearn": "take back something {who}'s name was learned from",
	"people.list": "list people", "people.show": "look at a person", "people.rename": "rename a person", "people.merge": "combine two people", "people.split": "separate two voices",
	"people.update": "update notes for {who}", "people.distinct": "say two people are different", "people.compare": "compare two people's voices",
	"people.rebuild":  "rebuild {who}'s voice from confirmed clips",
	"preview.prepare": "get a preview ready", "watch.status": "check the watched folder", "watch.set": "set up the watched folder", "watch.disable": "turn off the watched folder",
	"watch.import": "import files already in the watched folder", "watch.browse": "browse folders", "watch.mkdir": "create a folder", "watch.choose": "choose a folder",
	"watch.reveal": "show a folder in Finder", "cache.status": "check local copies", "cache.prune": "free up space used by local copies", "cache.prefetch": "download footage for offline work",
	"cache.repair": "check local copies for damage", "sync.status": "check what's on this machine", "workspace.show": "look at the workspace", "worker.list": "list machines",
	"version.show":      "check which version of MetaRush each computer runs",
	"setup.status":      "check what works on this computer",
	"components.list":   "list the components on this computer",
	"components.add":    "add a component to this computer",
	"components.remove": "remove a component from this computer",
	"workspace.export":  "export the workspace into an archive",
	"workspace.exports": "look at the workspace's exports",
	"workspace.restore": "restore the workspace from an export",
	"worker.settings":   "check the battery settings", "worker.configure": "change the battery settings", "machine.list": "list machines", "machine.invite": "invite a machine",
	"machine.revoke": "remove a machine", "machine.rekeyPlan": "check what giving this machine its own storage key would do",
	"machine.rekey": "give this machine its own storage key", "audit.list": "read the audit trail",
	"assistant.ask": "start a conversation with Claude Code", "assistant.continue": "send Claude Code a message", "assistant.interrupt": "interrupt Claude Code",
	"assistant.stop": "end a Claude Code session", "assistant.act": "decide on a change Claude asked for", "assistant.apply": "apply a plan Claude wrote",
	"assistant.list": "list Claude Code conversations", "assistant.show": "open a Claude Code conversation", "assistant.status": "check Claude Code",
	"assistant.proposals": "look at Claude's proposals", "assistant.rename": "rename a conversation", "assistant.archive": "archive a conversation",
	"assistant.configure": "change a conversation's settings", "assistant.dequeue": "take back a waiting message", "assistant.models": "list Claude models",
	"assistant.settings": "check the Claude Code settings", "assistant.settings.set": "change the Claude Code settings", "assistant.suggestions": "get prompt ideas",
	"assistant.mode": "check whether Claude's changes run by themselves", "assistant.mode.set": "choose whether Claude's changes run by themselves",
	"assistant.spending": "check what Auto may spend and has spent today", "project.sofar": "read what happened in {p} so far",
	"log.list": "read the log", "log.show": "open a log entry", "log.search": "search the log", "log.export": "export the log", "log.purge": "delete old log entries",
	"log.settings": "check the log settings", "log.configure": "change the log settings", "log.hooks.status": "check the Claude Code recording",
	"log.hooks.enable": "turn on recording of Claude Code sessions", "log.hooks.disable": "turn off recording of Claude Code sessions",
	"improve.signals": "look for problems in the log", "improve.save": "save an improvement report", "improve.list": "list improvement ideas",
	"improve.show": "open an improvement idea", "improve.keep": "keep an improvement idea", "improve.dismiss": "dismiss an improvement idea", "improve.export": "export improvement ideas",
	"improve.report": "make an improvement report", "improve.schedule": "check the weekly improvement report", "improve.schedule.set": "change the weekly improvement report",
	"voiceover.parse": "turn a script into narration lines", "voiceover.show": "look at the narration of {p}, version {r}",
	"voiceover.fit": "check that the narration of {p} fits", "voiceover.scratch": "make scratch narration for {p}, version {r}",
	"voiceover.estimate": "price the AI voice for {p}, version {r}", "voiceover.tts": "make the AI voice for {p}, version {r}",
	"voiceover.voices": "list voices", "voiceover.models": "list AI voice models", "voiceover.preview": "download a voice sample",
	"voiceover.takes": "list the narration takes of {p}", "voiceover.choose": "choose a narration take for {p}", "voiceover.addTake": "add a recording to the narration of {p}",
	"voiceover.align": "place your recording in {p}, version {r}", "voiceover.cleanup": "clean up the narration of {p}, version {r}",
	"voiceover.clone": "copy your voice for the AI voice", "mix.show": "look at the sound mix of {p}, version {r}",
	"voiceover.line": "change a narration line of {p}, version {r}", "mix.set": "change the sound mix of {p}, version {r}",
	"mix.preview": "try a sound mix change for {p}, version {r}",
	"sound.show":  "look at the sound of {p}, version {r}", "sound.levels": "change how loud things are in {p}, version {r}",
	"sound.preview": "try a sound change for {p}, version {r}", "sound.enhance": "make voices clearer in {p}, version {r}",
	"sound.compare": "compare voices before and after in {p}", "sound.music.add": "add music to {p}, version {r}",
	"sound.music.set": "change the music of {p}, version {r}", "sound.setup": "set up clearer voices on this computer",
	"voiceover.place": "place a live take in {p}, version {r}", "voiceover.fitted": "use Claude's fitted script in {p}, version {r}",
	"voiceover.use": "switch the script version of {p}, version {r}", "voiceover.askFit": "ask Claude Code to fit the script of {p} to the video",
	"provider.list": "list the integrations", "provider.status": "check an integration", "provider.set": "save this machine's integration key",
	"provider.test": "check an integration key", "provider.remove": "remove this machine's integration key", "provider.settings": "change the voice settings",
	"cut.show": "read the cut sheet of {p}", "cut.check": "check a cut sheet for {p}", "cut.diff": "compare a cut sheet with the saved versions of {p}",
	"cut.apply": "save a new version of {p} from a cut sheet", "cut.compare": "compare two versions of {p} as cut sheets",
	"project.checkout": "put {p} in a folder", "project.status": "check the folder copy of {p}", "project.pull": "update the folder copy of {p}",
	"project.push": "save a new version of {p} from its folder", "edit.transitions": "list the transitions", "cut.transition": "change a transition in {p}", "cut.framing": "change how crops are framed in {p}",
	"ingest.url": "import videos from links into {p}", "ingest.url.tools": "check the downloader for link imports", "ingest.url.update": "update the downloader for link imports",
	"ingest.url.configure": "change the link import settings", "extension.status": "check the browser extension", "extension.pair": "pair a browser with this machine",
	"extension.revoke": "unpair a browser", "edit.preview": "preview a change to {p}", "cut.add": "add a clip to {p}", "cut.move": "move a clip in {p}",
	"cut.trim": "trim a clip in {p}", "cut.split": "split a clip in {p}", "cut.delete": "remove clips from {p}", "cut.deleteRange": "cut out part of {p}",
	"cut.removeWords": "take words out of {p}", "cut.pauses": "shorten long pauses in {p}", "motion.new": "start a motion graphic", "motion.check": "check a motion graphic",
	"motion.list": "list the motion graphics", "motion.show": "look at a motion graphic", "motion.approve": "approve a motion graphic",
	"motion.revoke":  "withdraw approval of a motion graphic",
	"resources.list": "list resources", "resources.show": "look at a resource", "resources.where": "look at where a resource is used",
	"resources.path": "find a resource's file", "resources.update": "change a resource", "resources.refresh": "look at a resource's file again",
	"resources.remove": "remove a resource", "resources.promote": "move a resource to the workspace library", "resources.add": "add resources",
	"resources.link": "link a folder", "resources.folders": "list linked folders", "resources.unlink": "stop keeping a folder in step",
	"resources.folder": "change a linked folder", "resources.fetch": "bring in a repository or a web page", "resources.make": "make a slide picture from a repository", "brand.show": "look at the brand kit", "brand.set": "change the brand kit",
	"resources.pictures": "look at the pictures a repository keeps", "resources.pictures.set": "change which of a repository's folders it keeps pictures from",
	"brand.apply": "put the brand kit on {p}", "brand.suggest": "suggest a brand kit from a website", "asset.where": "look at where {a} is used", "asset.move": "move {a} to another project",
	"asset.delete": "delete {a} permanently", "project.link": "use another project's things in {p}", "project.use": "use more of another project in {p}",
	"project.unlink": "stop using another project in {p}", "project.links": "list the projects {p} uses",
	// The Library (D96–D98): collections, the Archive and the Trash.
	"asset.archive": "archive clips", "asset.unarchive": "take clips out of the Archive", "asset.trash": "move clips to the Trash",
	"asset.restore": "restore clips from the Trash", "library.list": "look at the Library", "collection.create": "create a collection",
	"collection.rename": "rename a collection", "collection.add": "add clips to a collection", "collection.remove": "take clips off a collection",
	"collection.list": "list collections", "collection.show": "look at a collection", "collection.delete": "delete a collection",
	"trash.list": "look at the Trash", "trash.empty": "empty the Trash",
	"trash.settings": "look at when the Trash empties itself", "trash.configure": "change when the Trash empties itself",
	"cut.update": "update what {p} uses to the latest versions", "cut.nest": "play another project's cut in {p}",
	"cut.copyIn": "copy another project's cut into {p}", "cut.where": "look at where {p}'s cut is used",
	"add.route": "check where files would go", "add.links": "bring in links", "add.paths": "add files",
	"export.timeline": "export version {r} of {p} as a timeline for another editing app",
	"storage.plan":    "check what moving the media to another storage would copy", "storage.migrate": "move the media to another storage",
	"storage.status": "look at which storage this computer uses",
	"database.plan":  "check what moving the workspace's database would do", "database.move": "move the workspace's database",
	"database.switch": "switch this computer to the workspace's new database", "database.abandon": "abandon a move of the database",
	"database.move.status": "look at whether the workspace's database is moving",
	// Screen recordings made in the dashboard (D258–D260).
	"recording.list": "list screen recordings", "recording.show": "look at a screen recording",
	"recording.finish": "add a screen recording to the Library", "recording.link": "link a screen recording into {p}",
	"recording.cut": "start a cut from a screen recording", "recording.discard": "throw away a screen recording",
	// Conversations from online calls (D99–D103).
	"conversation.create": "line up the recordings of a conversation", "conversation.list": "list conversations",
	"conversation.show": "look at a conversation", "conversation.sync": "line up a conversation's recordings again",
	"conversation.nudge": "move a recording of a conversation", "conversation.update": "change a conversation",
	"conversation.pin": "use a conversation's current alignment in {p}", "conversation.cut": "cut a conversation automatically",
	"conversation.angle": "change what a conversation clip shows", "conversation.styles": "list the conversation styles",
	"conversation.cleanup": "look for things to clean up in a conversation", "conversation.decide": "decide about a clean-up suggestion",
	"conversation.match": "match the webcams of a conversation", "conversation.import": "import a call's recordings as a conversation",
	"conversation.detect": "check whether recordings are one call", "conversation.moments": "find the best moments of a conversation",
	"conversation.short": "make a short from a conversation", "conversation.settings": "change how {p} plays a conversation",
	// Remote access over Tailscale (D128, D129).
	"remote.status": "check remote access", "remote.enable": "turn on remote access", "remote.disable": "turn off remote access",
	"remote.allow": "change who signs in from other computers", "remote.signout": "sign out browsers on other computers",
	// Generative video (D123–D127).
	"generate.estimate": "price a generated shot for {p}", "generate.shot": "ask for a generated shot for {p}",
	"generate.again": "ask for new takes of a generated shot", "generate.extend": "ask to continue a clip with generated video",
	"generate.edit": "ask to change a clip with generated video", "generate.list": "list generated videos", "generate.show": "look at how a generated video was made",
	"generate.cancel": "stop a generated video", "generate.settings": "check the generated video settings",
	"generate.configure": "change the generated video settings", "looks.list": "list looks", "looks.show": "look at a look",
	"looks.create": "make a look", "looks.update": "change a look", "looks.approve": "approve a look", "looks.revoke": "withdraw a look",
	"looks.test": "make test shots of a look", "consent.list": "list consent records", "consent.show": "look at a consent record",
	"consent.record": "record consent for someone's likeness", "consent.revoke": "withdraw consent for someone's likeness",
	"ai.models": "list the AI services' models", "ai.estimate": "price AI work for {p}", "ai.make": "ask an AI service for something for {p}",
	"ai.list": "list AI work", "ai.show": "look at how AI work was made", "ai.cancel": "stop AI work", "ai.voices": "list voice copies",
	"ai.voices.copy": "ask for a copy of someone's voice", "ai.voices.remove": "withdraw a voice copy", "ai.rules": "check which AI services a project may use",
	"ai.rules.set": "change which AI services a project may use", "ai.local": "list the AI models on this computer", "ai.local.setup": "set up an AI model on this computer",
	// Presenters (D273–D277).
	"presenter.list": "list presenters", "presenter.show": "look at a presenter", "presenter.start": "begin someone's presenter",
	"presenter.make": "ask for someone's presenter", "presenter.test": "ask for a test clip of a presenter", "presenter.approve": "approve a presenter",
	"presenter.retire": "retire a presenter", "presenter.stock": "list stock presenters", "presenter.optout": "check a service's training opt-out",
	"presenter.optout.set": "record a service's training opt-out",
	// Remotion sources (D131–D138).
	"remotion.import": "bring a Remotion project into {p}", "remotion.list": "list the Remotion projects", "remotion.show": "look at a Remotion project",
	"remotion.render": "make a clip from a Remotion project", "remotion.use": "put a Remotion composition into {p}",
	"remotion.stills": "draw frames of a Remotion composition", "remotion.checkout": "write a Remotion project into a folder to edit",
	"remotion.check": "check a version of a Remotion project", "remotion.approve": "approve a version of a Remotion project",
	"remotion.revoke": "withdraw approval of a version of a Remotion project", "remotion.allow": "allow the packages of a Remotion project's version",
	"remotion.update": "move {p} to the newest approved version of a Remotion project",
	// Fixing transcripts (D145–D149).
	"transcript.find": "look for words in transcripts", "transcript.replace": "change words in transcripts", "transcript.fix": "fix words in the transcript of {a}",
	"transcript.speaker": "fix who speaks in {a}", "transcript.history": "read the transcript fixes of {a}", "transcript.undo": "undo a transcript fix",
	"transcript.spelling.list": "read the spelling lists", "transcript.spelling.add": "add a word to a spelling list",
	"transcript.spelling.remove": "take a word off a spelling list", "transcript.spelling.apply": "correct transcripts by the spelling lists",
	"transcript.spelling.suggestions": "look for words to put on the spelling list", "transcript.pending": "check the text fixes waiting for {p}",
	"transcript.pending.apply": "make a new version of {p} with the text fixes", "captions.check": "check the captions of {p}", "captions.at": "look at a caption of {p}",
	"captions.file": "make a captions file for {p}", "deliver.captions": "update the captions files of delivered videos",
	// Call recordings (D160–D162) and what a project keeps (D166).
	"footage.switches": "read where the picture of {a} switches", "footage.switches.find": "find where the picture of {a} switches",
	"cut.flashes": "trim split-second flashes in {p}", "cut.names": "cover the printed names in {p} with a name bar",
	"project.rules.list": "read the rules of {p}", "project.rules.add": "add a rule to {p}", "project.rules.remove": "take a rule out of {p}",
	// Content Settings (D192, D193).
	"content.show": "look at the settings {p} uses", "content.follow": "choose whose settings {p} uses", "content.own": "give {p} settings of its own",
	"content.setDefault": "use the settings of {p} for new projects", "content.rules.list": "read the house rules", "content.rules.add": "add a house rule",
	"content.rules.remove": "take a house rule out",
	"content.logo.set":     "choose one of the brand's logos", "content.logo.remove": "take one of the brand's logos away",
	"content.style.show": "look at the house style", "content.style.set": "change the house style", "content.style.apply": "use the house style on the latest cut of {p}",
	"content.signoffs.list": "read the house sign-offs", "content.signoffs.add": "add a house sign-off", "content.signoffs.remove": "take a house sign-off out",
	"content.pack.show": "look at the layout pack", "content.pack.set": "change the layout pack", "content.pack.propose": "propose a layout pack",
	"content.pack.accept": "accept a layout pack", "content.pack.decline": "set a proposed layout pack aside", "content.pack.apply": "use the layout pack on the latest cut of {p}",
	"content.pack.preview": "preview the layout pack",
	// Descriptive names, info cards and fixes (D150–D153).
	"asset.names": "look at better names for clips", "asset.names.accept": "use better names for clips",
	"asset.card": "look at the info card of {a}", "asset.fixes": "look at the fixes for {a}",
	"asset.fixes.set": "change the fixes of {a}", "asset.fixes.look": "measure {a} for its fixes",
	"cut.rename": "rename the cut of {p}", "cut.useFixed": "switch footage in {p} to a fixed copy",
	"revision.note":  "write a note on a version of {p}",
	"delivery.notes": "read the delivery notes of {p}", "delivery.notes.set": "write the delivery notes of {p}",
	"captions.languages": "check which languages the captions of {p} are in", "captions.lines": "read the captions of {p} to translate them",
	"captions.translate": "keep translated captions for {p}", "captions.translation.remove": "take translated captions off {p}",
	"project.receipt": "read the receipt of {p}",
	// The ship gate and promises made to the client (D298–D300).
	"delivery.ready": "check whether a version of {p} can go out", "delivery.override": "send a version of {p} out anyway",
	"delivery.gate": "read whether versions wait for the quality check", "delivery.gate.set": "change whether versions wait for the quality check",
	"content.gate.set": "change whether the settings' videos wait for the quality check",
	// A review copy follows the latest version; what waits for review (D187, D188).
	"render.follow.show": "check whether review copies follow the latest versions", "render.follow.set": "turn review copies of the latest versions on or off",
	"review.waiting": "list the versions awaiting review", "revision.askReview": "ask for a review of version {r} of {p}",
	// Drafts from AI apps and the review desk (D248–D250).
	"draft.save": "propose a change as a draft", "draft.list": "list the drafts AI apps proposed",
	"draft.decide": "decide about a draft an AI app proposed", "review.packet": "put together the review page of a draft or version",
	"remotion.map": "look at the map of a Remotion project", "remotion.words": "look at the words of a Remotion project",
	"remotion.words.setup": "make the words of a Remotion project settings", "remotion.words.set": "change words of a Remotion project",
	"remotion.pictures": "look at the pictures of a Remotion project", "remotion.pictures.setup": "make the pictures of a Remotion project settings",
	"remotion.pictures.set": "replace pictures of a Remotion project",
	"remotion.lift":         "turn a scene of a Remotion project into a motion graphic",
	// Refreshing a Remotion project from its own steps and sending changes back (D173–D176).
	"remotion.refresh.plan": "look at what refreshing a Remotion project would do", "remotion.refresh.start": "start refreshing a Remotion project from its own steps",
	"remotion.refresh.runs": "look at the refreshes of a Remotion project", "remotion.refresh.log": "read what a refresh of a Remotion project printed",
	"remotion.push.plan": "look at what sending a Remotion project's changes back would do", "remotion.push.start": "send a Remotion project's changes back to its repository",
	"remotion.push.list": "look at the changes sent back from a Remotion project",
}

// cutVerbChecked is how a dry run of an editing verb reads ("checked a trim of a clip in Spring promo").
var cutVerbChecked = map[string]string{"cut.add": "checked adding a clip to {p}", "cut.move": "checked moving a clip in {p}", "cut.trim": "checked a trim in {p}",
	"cut.split": "checked a split in {p}", "cut.delete": "checked removing clips from {p}", "cut.deleteRange": "checked cutting out part of {p}",
	"cut.removeWords": "checked taking words out of {p}", "cut.pauses": "checked shortening long pauses in {p}", "cut.flashes": "checked trimming flashes in {p}", "cut.names": "checked a name bar for {p}"}

// pastOf turns a phrase's first verb into the past tense.
var pastOf = map[string]string{"clear": "cleared", "create": "created", "list": "listed", "open": "opened", "change": "changed", "look": "looked", "rename": "renamed",
	"add": "added", "sort": "sorted", "record": "recorded", "check": "checked", "import": "imported", "clean": "cleaned", "start": "started",
	"read": "read", "take": "took", "make": "made", "name": "named", "say": "said", "correct": "corrected", "save": "saved", "compare": "compared",
	"prepare": "prepared", "approve": "approved", "queue": "queued", "cancel": "canceled", "download": "downloaded", "export": "exported",
	"retry": "retried", "ask": "asked", "search": "searched", "mark": "marked", "answer": "answered", "skip": "skipped", "count": "counted",
	"fix": "fixed", "listen": "listened", "rebuild": "rebuilt",
	"combine": "combined", "separate": "separated", "get": "got", "set": "set", "turn": "turned", "browse": "browsed", "choose": "chose",
	"show": "showed", "free": "freed", "invite": "invited", "remove": "removed", "send": "sent", "interrupt": "interrupted", "end": "ended",
	"decide": "decided", "apply": "applied", "archive": "archived", "delete": "deleted", "keep": "kept", "dismiss": "dismissed", "find": "found",
	"price": "priced", "place": "placed", "copy": "copied", "put": "put", "update": "updated", "pair": "paired", "unpair": "unpaired",
	"preview": "previewed", "try": "tried", "move": "moved", "trim": "trimmed", "split": "split", "cut": "cut", "withdraw": "withdrew",
	"use": "used", "stop": "stopped", "link": "linked", "bring": "brought", "play": "played", "describe": "described",
	"line": "lined", "match": "matched", "sign": "signed",
	"restore": "restored", "empty": "emptied", "draw": "drew", "write": "wrote", "allow": "allowed", "cover": "covered", "tick": "ticked",
	"untick": "unticked", "decline": "declined", "suggest": "suggested", "propose": "proposed", "attach": "attached", "measure": "measured", "switch": "switched"}

func past(base string) string {
	verb, rest, _ := strings.Cut(base, " ")
	if p, ok := pastOf[verb]; ok {
		verb = p
	}
	if rest == "" {
		return verb
	}
	return verb + " " + rest
}

func fill(tpl string, e *Entry, n *Names) string {
	r := e.Refs
	rev := ""
	if num, ok := n.Revisions[r.RevisionID]; ok && num > 0 {
		rev = fmt.Sprint(num)
	} else if rn, ok := n.Renders[r.RenderID]; ok && rn.RevisionNumber > 0 {
		rev = fmt.Sprint(rn.RevisionNumber)
	} else if f := factInt(e, "revision.number"); f > 0 {
		rev = fmt.Sprint(f)
	}
	projectID := r.ProjectID
	if projectID == "" {
		projectID = n.Renders[r.RenderID].ProjectID
	}
	if projectID == "" {
		projectID = n.RevisionProjects[r.RevisionID]
	}
	q := n.Renders[r.RenderID].Quality
	if q == "" {
		q, _ = inputString(e, "quality")
	}
	if q == "" {
		q = "review"
	}
	who := "someone"
	if e.Op != nil && e.Op.Output != nil {
		if v, ok := e.Op.Output.Facts["person.name"].(string); ok && v != "" {
			who = v
		}
	}
	if who == "someone" && strings.Contains(tpl, "{who}'s") {
		tpl = strings.ReplaceAll(tpl, "{who}'s", "a person's")
	}
	proj := n.project(projectID)
	if name, _ := inputString(e, "name"); projectID == "" && name != "" && e.Op != nil && e.Op.Name == "project.create" {
		proj = "“" + Clip(name, 60) + "”" // a create that did not happen has no project to name
	}
	if q == "review" {
		tpl = strings.ReplaceAll(tpl, "{q} render", "review copy") // D188: a review render is a review copy
	}
	repl := strings.NewReplacer("{p}", proj, "{a}", n.asset(r.AssetID), "{q}", q, "{r}", firstNonEmpty(rev, "?"), "{who}", who)
	s := repl.Replace(tpl)
	s = strings.ReplaceAll(s, "version ? of", "a version of")
	s = strings.ReplaceAll(s, ", version ?", "")
	return s
}

func factInt(e *Entry, k string) int {
	if e.Op == nil || e.Op.Output == nil {
		return 0
	}
	if v, ok := e.Op.Output.Facts[k].(float64); ok {
		return int(v)
	}
	return 0
}

func inputString(e *Entry, k string) (string, bool) {
	if e.Op == nil {
		return "", false
	}
	s, ok := e.Op.Input[k].(string)
	return s, ok
}

// OpWords is an operation in plain words, without the names of what it worked
// on: "start a render of a project", "name a voice in a clip". An operation
// without a phrase keeps its name.
func OpWords(name string) string {
	base, ok := opBase[name]
	if !ok {
		return strings.ReplaceAll(name, ".", " ")
	}
	return fill(strings.ReplaceAll(base, "{q} ", ""), &Entry{Op: &OpInfo{Name: name}}, &Names{})
}

func opPhrase(e *Entry, n *Names, summary string, done bool) string {
	name := ""
	if e.Op != nil {
		name = e.Op.Name
	}
	if e.Counts != nil {
		name = e.Counts.Op
	}
	base, ok := opBase[name]
	if !ok {
		s := summary
		if s == "" {
			s = strings.ReplaceAll(name, ".", " ")
		}
		if i := strings.IndexAny(s, ":(;"); i > 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
		if r, size := utf8.DecodeRuneInString(s); size > 0 {
			s = string(unicode.ToLower(r)) + s[size:]
		}
		base = s
	}
	base = fill(base, e, n)
	if done {
		return past(base)
	}
	return base
}

func describeOp(e *Entry, n *Names, summary string) string {
	ok := e.Op != nil && e.Op.OK
	if !ok {
		msg := ""
		if e.Error != nil {
			msg = plainError(e.Error)
		}
		s := "couldn't " + opPhrase(e, n, summary, false)
		if msg != "" {
			s += ": " + msg
		}
		return s
	}
	s := opPhrase(e, n, summary, true)
	switch e.Op.Name {
	case "ingest.files":
		in, dup := factInt(e, "ingested"), factInt(e, "deduped")+factInt(e, "duplicates")
		parts := []string{}
		if in > 0 {
			parts = append(parts, plural(in, "new clip"))
		}
		if dup > 0 {
			parts = append(parts, plural(dup, "clip")+" already there")
		}
		if f := factInt(e, "failed"); f > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", f))
		}
		if len(parts) > 0 {
			s += " (" + strings.Join(parts, ", ") + ")"
		}
	case "edit.check", "cut.check":
		if v, ok := e.Op.Output.factBool("valid"); ok && !v {
			s += " (it has problems to fix)"
		}
	case "cut.transition":
		// "Changed the transition into Sam's clip in Spring promo"; a dry run only checked it.
		if e.Op.Output == nil {
			break
		}
		into, _ := e.Op.Output.Facts["into"].(string)
		if into == "" {
			break
		}
		saved, _ := e.Op.Output.Facts["saved"].(bool)
		same, _ := e.Op.Output.Facts["noChange"].(bool)
		switch {
		case saved:
			s = fill("changed the transition into "+into+" in {p}", e, n)
		case same:
			s = fill("looked at the transition into "+into+" in {p} (nothing to change)", e, n)
		default:
			s = fill("checked a change to the transition into "+into+" in {p}", e, n)
		}
	case "voiceover.line":
		switch act, _ := inputString(e, "action"); act {
		case "add":
			s = fill("added a narration line to {p}, version {r}", e, n)
		case "move":
			s = fill("moved a narration line in {p}, version {r}", e, n)
		case "remove":
			s = fill("removed a narration line from {p}, version {r}", e, n)
		}
	case "cut.add", "cut.move", "cut.trim", "cut.split", "cut.delete", "cut.deleteRange", "cut.removeWords", "cut.pauses", "cut.say.place", "cut.say.undo":
		// Saved: "Trimmed a clip in Spring promo"; a dry run only checked it.
		if e.Op.Output == nil {
			break
		}
		saved, _ := e.Op.Output.Facts["saved"].(bool)
		same, _ := e.Op.Output.Facts["noChange"].(bool)
		switch {
		case same:
			s = fill(strings.Replace(cutVerbChecked[e.Op.Name], "checked", "looked at", 1)+" (nothing to change)", e, n)
		case !saved:
			s = fill(cutVerbChecked[e.Op.Name], e, n)
		}
	case "revision.approve":
		if b, _ := e.Op.Input["reject"].(bool); b {
			s = strings.Replace(s, "approved", "rejected", 1)
		}
	case "trash.empty", "asset.delete":
		// The warning before deleting for good is a dry run: nothing was deleted.
		if b, _ := e.Op.Input["dryRun"].(bool); b {
			if e.Op.Name == "trash.empty" {
				s = "checked what emptying the Trash would delete"
			} else {
				s = fill("checked whether {a} can be deleted", e, n)
			}
		}
	case "motion.check", "motion.approve", "motion.revoke", "motion.show", "motion.new":
		// "checked the motion graphic route-map", "approved the motion graphic route-map@3fa9c2d1"
		ref, _ := inputString(e, "ref")
		if ref == "" {
			ref, _ = inputString(e, "name")
		}
		if ref == "" {
			if d, ok := inputString(e, "dir"); ok && d != "" {
				ref = d[strings.LastIndexAny(d, "/\\")+1:]
			}
		}
		if ref != "" {
			s = strings.Replace(s, "a motion graphic", "the motion graphic "+Clip(ref, 60), 1)
		}
		if b, _ := e.Op.Input["reject"].(bool); b && e.Op.Name == "motion.approve" {
			s = strings.Replace(s, "approved", "turned down", 1)
		}
		if e.Op.Name == "motion.check" {
			if v, ok := e.Op.Output.factBool("ok"); ok && !v {
				s += " (it has problems to fix)"
			}
		}
	case "search":
		if q, ok := inputString(e, "query"); ok && q != "" {
			s = "searched the footage for “" + Clip(q, 60) + "”"
		}
	case "voiceover.tts":
		made, reused := factInt(e, "generated"), factInt(e, "reused")
		var parts []string
		if made > 0 {
			p := plural(made, "line")
			if c := factFloat(e, "credits"); c > 0 {
				p += fmt.Sprintf(", about %.0f credits", c)
			}
			parts = append(parts, p)
		}
		if reused > 0 {
			parts = append(parts, plural(reused, "unchanged line")+" reused for free")
		}
		if len(parts) > 0 {
			s += " (" + strings.Join(parts, "; ") + ")"
		}
	}
	return s
}

func factFloat(e *Entry, k string) float64 {
	if e.Op == nil || e.Op.Output == nil {
		return 0
	}
	v, _ := e.Op.Output.Facts[k].(float64)
	return v
}

func (o *OutputInfo) factBool(k string) (bool, bool) {
	if o == nil {
		return false, false
	}
	b, ok := o.Facts[k].(bool)
	return b, ok
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// plainError is an error's message without technical noise.
func plainError(e *EntryError) string {
	msg := strings.TrimSpace(e.Message)
	switch e.Code {
	case "E_HUMAN_ONLY":
		return "only a person can do that (in the dashboard or their own terminal)"
	case "E_NOT_FOUND":
		return "it wasn't found"
	case "E_UNAUTHORIZED":
		if e.Reason == "assistant_read_only" {
			return "Claude in the dashboard can only look, not change things"
		}
	case "E_CONFIRM_REQUIRED":
		return "the change needs to be checked first"
	}
	return Clip(firstLineOf(msg), 140)
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

type jobWords struct{ ing, past, base string }

var jobPhrases = map[string]jobWords{
	"proxy":         {"making a preview copy of", "made a preview copy of", "make a preview copy of"},
	"thumbnail":     {"making a thumbnail for", "made a thumbnail for", "make a thumbnail for"},
	"contact_sheet": {"making a frame grid for", "made a frame grid for", "make a frame grid for"},
	"waveform":      {"drawing the waveform of", "drew the waveform of", "draw the waveform of"},
	"mezzanine":     {"making a steady copy of", "made a steady copy of", "make a steady copy of"},
	"evidence":      {"analyzing scenes and pauses in", "analyzed scenes and pauses in", "analyze scenes and pauses in"},
	"transcribe":    {"transcribing", "transcribed", "transcribe"},
	"diarize":       {"finding who's speaking in", "found who's speaking in", "find who's speaking in"},
	"voiceover":     {"making the voiceover for", "made the voiceover for", "make the voiceover for"},
	"visual":        {"looking at what's on screen in", "looked at what's on screen in", "look at what's on screen in"},
	"describe":      {"describing the scenes of", "described the scenes of", "describe the scenes of"},
}

// jobThing is what a job worked on: "a review copy of Spring promo, version 4".
func jobThing(e *Entry, n *Names) (words jobWords, thing string) {
	typ := ""
	if e.Job != nil {
		typ = e.Job.Type
	}
	quality := ""
	if e.Job != nil {
		quality, _ = e.Job.Detail["quality"].(string)
	}
	rn := n.Renders[e.Refs.RenderID]
	if quality == "" {
		quality = rn.Quality
	}
	version := ""
	if rn.RevisionNumber > 0 {
		version = fmt.Sprintf(", version %d", rn.RevisionNumber)
	}
	project := n.project(firstNonEmpty(e.Refs.ProjectID, rn.ProjectID))
	switch typ {
	case "render":
		if quality == "final" {
			return jobWords{"rendering", "made", "render"}, "the final video of " + project + version
		}
		return jobWords{"making", "made", "make"}, "a review copy of " + project + version
	case "qa":
		copyName := "the review copy"
		if quality == "final" {
			copyName = "the final video"
		}
		return jobWords{"checking", "checked", "check"}, copyName + " of " + project + version
	case "voiceover":
		var detail map[string]any
		if e.Job != nil {
			detail = e.Job.Detail
		}
		action, _ := detail["action"].(string)
		if num := numOf(detail["revisionNumber"]); num > 0 {
			version = fmt.Sprintf(", version %d", num)
		} else if num := n.Revisions[e.Refs.RevisionID]; num > 0 {
			version = fmt.Sprintf(", version %d", num)
		}
		thing := project + version
		if e.Refs.ProjectID == "" && e.Refs.RevisionID != "" {
			thing = n.project(n.RevisionProjects[e.Refs.RevisionID]) + version
		}
		switch action {
		case "tts":
			return jobWords{"making the AI voice for", "made the AI voice for", "make the AI voice for"}, thing
		case "scratch":
			return jobWords{"making scratch narration for", "made scratch narration for", "make scratch narration for"}, thing
		case "cleanup":
			return jobWords{"cleaning up the narration of", "cleaned up the narration of", "clean up the narration of"}, thing
		case "align":
			return jobWords{"placing your recording in", "placed your recording in", "place your recording in"}, thing
		case "clone":
			return jobWords{"making a copy of", "made a copy of", "make a copy of"}, "your voice"
		}
		return jobPhrases["voiceover"], thing
	case "import":
		var detail map[string]any
		if e.Job != nil {
			detail = e.Job.Detail
		}
		title, _ := detail["title"].(string)
		platform, _ := detail["platform"].(string)
		site := models.PlatformName(platform)
		thing := "a video from " + site
		if title != "" {
			thing = "“" + Clip(title, 60) + "” from " + site
		}
		return jobWords{"importing", "imported", "import"}, thing
	case "generate":
		// "Started making a 3-second shot with Google Gemini for Pond film".
		var detail map[string]any
		if e.Job != nil {
			detail = e.Job.Detail
		}
		thing, _ := detail["what"].(string)
		if thing == "" {
			thing = "a generated shot"
		}
		if take := numOf(detail["take"]); take > 1 {
			thing += fmt.Sprintf(" (take %d)", take)
		}
		if who, _ := detail["provider"].(string); who != "" {
			thing += " with " + who
		}
		if e.Refs.ProjectID != "" {
			thing += " for " + project
		}
		return jobWords{"making", "made", "make"}, thing
	case "remotion-import":
		return jobWords{"bringing in", "brought in", "bring in"}, "a Remotion project for " + project
	case "remotion-render":
		thing := "a clip from a Remotion project"
		if e.Job != nil {
			if c, _ := e.Job.Detail["composition"].(string); c != "" {
				thing = "a clip of “" + Clip(c, 60) + "”"
			}
		}
		return jobWords{"making", "made", "make"}, thing
	case "remotion-task":
		// The job says what it does: "making the map of “teaser”", "checking new words for “teaser”".
		thing := "work on a Remotion project for " + project
		if e.Job != nil {
			if t, _ := e.Job.Detail["what"].(string); t != "" {
				thing = Clip(t, 120)
			}
		}
		return jobWords{"doing", "did", "do"}, thing
	case "remotion-refresh":
		// "refreshing “Teaser” from its own steps" (D173).
		thing := "a Remotion project from its own steps for " + project
		if e.Job != nil {
			if t, _ := e.Job.Detail["what"].(string); t != "" {
				thing = Clip(t, 120)
			}
		}
		return jobWords{"refreshing", "refreshed", "refresh"}, thing
	case "ability-run":
		// "running “Title card” (run 3)" (D234).
		thing := "an ability"
		if e.Job != nil {
			if t, _ := e.Job.Detail["what"].(string); t != "" {
				thing = Clip(t, 120)
			}
		}
		return jobWords{"running", "ran", "run"}, thing
	case "remotion-push":
		// "sending version 4 of “Teaser” back to github.com/acme/teaser" (D176).
		thing := "a Remotion project's changes back to its repository"
		if e.Job != nil {
			if t, _ := e.Job.Detail["what"].(string); t != "" {
				thing = Clip(t, 120)
			}
		}
		return jobWords{"sending", "sent", "send"}, thing
	case "storage_move":
		to := "new storage"
		if e.Job != nil {
			if s, _ := e.Job.Detail["storage"].(string); s != "" {
				to = s
			}
		}
		return jobWords{"moving the media to", "moved the media to", "move the media to"}, to
	}
	if w, ok := jobPhrases[typ]; ok {
		return w, n.asset(e.Refs.AssetID)
	}
	label := strings.ToLower(strings.ReplaceAll(typ, "_", " "))
	if JobLabel != nil {
		label = strings.ToLower(JobLabel(typ))
	}
	return jobWords{"working on the " + label + " for", "finished the " + label + " for", "do the " + label + " for"}, n.asset(e.Refs.AssetID)
}

// JobLabel names a job type for people; the ops package sets it.
var JobLabel func(typ string) string

func describeJob(e *Entry, n *Names) string {
	w, thing := jobThing(e, n)
	j := e.Job
	if j == nil {
		return e.Summary
	}
	switch j.Event {
	case JobQueued:
		return "waiting to start: " + w.ing + " " + thing
	case JobLeased:
		return "started " + w.ing + " " + thing
	case JobProgress:
		pct := 0.0
		if j.Pct != nil {
			pct = *j.Pct
		}
		return fmt.Sprintf("%s %s: %.0f%% done", w.ing, thing, pct*100)
	case JobSucceeded:
		return w.past + " " + thing
	case JobFailed:
		return "couldn't " + w.base + " " + thing
	case JobRetry:
		s := w.ing + " " + thing + " hit a problem; it will try again"
		if j.MaxAttempts > 0 {
			s += fmt.Sprintf(" (attempt %d of %d)", j.Attempt+1, j.MaxAttempts)
		}
		return s
	case JobCanceled:
		return "stopped " + w.ing + " " + thing
	case JobCancelRequested:
		return "asked to stop " + w.ing + " " + thing
	case JobRequeued:
		return "trying " + w.ing + " " + thing + " again"
	case JobReleased:
		return "waiting again: " + w.ing + " " + thing + " (MetaRush stopped on the machine running it; it starts over on the next one free)"
	case JobExpedited:
		return "asked to start " + w.ing + " " + thing + " now"
	}
	return e.Summary
}

// toolNames explain the programs MetaRush runs.
var toolNames = map[string]string{"ffmpeg": "the video tool (ffmpeg)", "ffprobe": "the file inspector (ffprobe)", "node": "the renderer (Remotion)",
	"whisper-cli": "the transcriber (whisper.cpp)", "sherpa-onnx-offline": "the transcriber (Parakeet)",
	"sherpa-onnx-offline-speaker-diarization": "the tool that tells voices apart", "atlas": "the Atlas database tool", "claude": "Claude Code",
	"osascript": "the folder picker", "open": "Finder", "xdg-open": "the file manager", "yt-dlp": "the downloader (yt-dlp)", "deno": "the JavaScript runtime (Deno)",
	"brew": "Homebrew"}

// ToolName explains a program name for people.
func ToolName(p string) string {
	if s, ok := toolNames[p]; ok {
		return s
	}
	return p
}

func describeTool(e *Entry) string {
	t := e.Tool
	if t == nil {
		return e.Summary
	}
	name := ToolName(t.Program)
	switch {
	case t.OK:
		return "ran " + name
	case t.Canceled:
		return name + " was stopped"
	case t.ExitCode == -1:
		return name + " could not start"
	}
	return name + " stopped with an error"
}

// describeProvider is a request to an online service: "ElevenLabs made speech
// (35 characters)", "ElevenLabs couldn't make speech: did not accept the key".
func describeProvider(e *Entry) string {
	p := e.Provider
	if p == nil {
		return e.Summary
	}
	title := firstNonEmpty(p.Title, p.Name)
	if !p.OK {
		s := title + " couldn't " + p.What
		if e.Error != nil {
			if msg := strings.TrimPrefix(plainError(e.Error), title+" "); msg != "" {
				s += ": " + msg
			}
		}
		return s
	}
	s := title + " " + past(p.What)
	if p.Characters > 0 {
		s += " (" + plural(p.Characters, "character") + ")"
	}
	return s
}

func describeClaude(e *Entry, n *Names) string {
	c := e.Claude
	if c == nil {
		return e.Summary
	}
	who := "Claude Code"
	switch c.Source {
	case SourceSkillEval:
		verdict := ""
		if c.Pass != nil {
			verdict = map[bool]string{true: ": passed", false: ": failed"}[*c.Pass]
		}
		return fmt.Sprintf("skill test “%s”%s", firstNonEmpty(c.Case, "case"), verdict)
	case SourceClaudeTerminal:
		who = "Claude Code in a terminal"
	}
	var s string
	switch {
	case c.Prompt != "":
		s = "asked " + who + ": “" + Clip(oneLine(c.Prompt), 90) + "”"
	case c.Privacy == PrivacyLocal || (c.PromptRef != nil && c.PromptRef.Store == StoreLocal):
		where := "this machine"
		if m := n.Machines[e.InstallationID]; m != "" {
			where = m
		} else if e.Machine != "" {
			where = e.Machine
		}
		s = "asked " + who + " something (the words are kept only on " + where + ")"
	case c.Privacy == PrivacyOff:
		s = "asked " + who + " something (the words aren't recorded)"
	case c.PromptRef != nil:
		s = "asked " + who + " something long"
	default:
		s = "asked " + who + " something"
	}
	var extra []string
	if k := len(c.Actions); k > 0 {
		extra = append(extra, plural(k, "change")+" proposed")
	}
	switch c.State {
	case "", "succeeded":
	case "interrupted":
		extra = append(extra, "interrupted")
	case "stopped":
		extra = append(extra, "stopped")
	case "budget":
		extra = append(extra, "stopped at its budget")
	case "timed_out":
		extra = append(extra, "ran out of time")
	default:
		extra = append(extra, "it stopped with a problem")
	}
	if len(extra) > 0 {
		s += " (" + strings.Join(extra, ", ") + ")"
	}
	return s
}

func describeAction(e *Entry) string {
	a := e.Action
	if a == nil {
		return e.Summary
	}
	title := Clip(a.Title, 100)
	if a.Auto {
		switch a.Decision {
		case DecisionApplied:
			return "Claude Code started this by itself, as your setting for safe steps allows: " + title
		case DecisionFailed:
			return "Claude Code started this by itself, as your setting for safe steps allows, but it failed: " + title
		}
	}
	switch a.Decision {
	case DecisionApplied:
		return "did what Claude asked for: " + title
	case DecisionDismissed:
		return "said no to what Claude asked for: " + title
	case DecisionFailed:
		return "tried what Claude asked for, but it failed: " + title
	case DecisionUnavailable:
		return "couldn't do what Claude asked for from the dashboard: " + title
	}
	return "Claude asked for a change: " + title
}

var eventPhrases = map[string]string{
	"asset.ingest": "imported {a}", "asset.duplicate": "skipped a copy of {a}", "conversation.launch": "started Claude Code for a conversation",
	"conversation.resume": "picked up a Claude Code conversation again", "conversation.interrupt": "interrupted Claude Code",
	"conversation.stop": "ended Claude Code for a conversation", "conversation.budget": "a Claude Code conversation reached its spending limit",
	"assistant.turn": "Claude Code finished a reply", "assistant.action": "decided on a change Claude asked for",
	// Changes MetaRush makes by itself (outside a command or a dashboard click).
	"asset.alias": "kept another copy of {a} (stored once)", "resource.version": "saved a new version of a resource",
	"resource.upload": "copied a resource from a linked folder to MetaRush's storage", "trash.autoEmpty": "emptied the Trash, as set",
	"render.complete": "finished a render of {p}", "qa.complete": "finished a quality check of {p}",
	"speakers.name": "named a voice in {a}", "people.create": "added a person",
	"footage.faces.group": "named a person's faces in {a}", "footage.faces.group.reject": "said who isn't on screen in {a}",
	"footage.faces.group.skip": "skipped a question about faces in {a}", "footage.faces.group.join": "said two faces in {a} are one person",
	"footage.faces.takeback": "took back faces a person's face was learned from",
	"remote.signin":          "signed in from another computer with Tailscale",
}

func describeEvent(e *Entry, n *Names) string {
	if e.Event == nil {
		return e.Summary
	}
	tpl, ok := eventPhrases[e.Event.Action]
	switch {
	case ok:
	case strings.HasPrefix(e.Event.Action, "catchup."):
		// Catching up older footage after an update; its summary says what it did.
		s := "caught up older footage after an update"
		if sum, _ := e.Event.Details["summary"].(string); strings.TrimSpace(sum) != "" {
			s += ": " + strings.TrimSuffix(strings.TrimSpace(sum), ".")
		}
		return s
	case opBase[e.Event.Action] != "":
		// An audited change named like the operation that makes it.
		tpl = past(opBase[e.Event.Action])
	default:
		return e.Summary
	}
	s := fill(tpl, e, n)
	if e.Channel == ChannelWatch {
		s += " from the watched folder"
	}
	return s
}

func capFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// Target is where in the dashboard an entry's object lives.
type Target struct {
	Label string `json:"label"`
	Link  string `json:"link"`
}

// TargetOf picks the object an entry is about (render, report, version, clip,
// job, conversation or project) and links to it.
func TargetOf(e *Entry, n *Names) *Target {
	r := e.Refs
	projectID := r.ProjectID
	if rn, ok := n.Renders[r.RenderID]; ok && projectID == "" {
		projectID = rn.ProjectID
	}
	switch {
	case (e.Kind == KindClaude || e.Kind == KindAction || (e.Event != nil && strings.HasPrefix(e.Event.Action, "conversation."))) && strings.HasPrefix(r.ConversationID, "asc_"):
		// A Claude Code conversation (a recorded call's conversation, cnv_, links to its project below).
		return &Target{Label: "Open the conversation", Link: links.Conversation(projectID, r.ConversationID)}
	case r.QAReportID != "" && r.RenderID != "":
		return &Target{Label: "Open the quality check", Link: links.QA(projectID, r.RenderID, r.QAReportID)}
	case r.RenderID != "":
		return &Target{Label: "Open the render", Link: links.Render(projectID, r.RenderID)}
	case e.Kind == KindJob && r.JobID != "":
		return &Target{Label: "Open the job", Link: links.Job(r.JobID)}
	case r.RevisionID != "" && projectID != "":
		return &Target{Label: "Open the version", Link: links.Revision(projectID, r.RevisionID)}
	case r.AssetID != "":
		return &Target{Label: "Open the clip", Link: links.Asset(projectID, r.AssetID)}
	case r.JobID != "":
		return &Target{Label: "Open the job", Link: links.Job(r.JobID)}
	case projectID != "":
		return &Target{Label: "Open the project", Link: links.Project(projectID)}
	}
	return nil
}
