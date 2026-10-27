package catalog

import (
	"strings"
	"sync"
)

// opWords are the words people use for an operation that its name, command
// and summary don't have: how a person asks for it, never how the code says
// it. They are written for the requests people make, not copied from the
// eval; the held-out part of the eval says whether they generalize.
var opWords = map[string][]string{
	// Projects.
	"project.list":     {"my video projects", "what projects I have", "what am I working on", "all my videos"},
	"project.create":   {"start a new project", "new video", "new project for", "begin a video"},
	"project.show":     {"open a project", "what's in a project", "project overview"},
	"project.sofar":    {"what happened so far", "catch me up", "history of the project", "what was decided"},
	"project.checkout": {"keep the cut in git", "the cut as files in a folder", "check out the project"},
	"project.link":     {"use another project's footage and graphics", "share between projects"},
	"project.delete":   {"get rid of a project", "remove a project"},
	"project.restore":  {"bring back a deleted project", "undelete a project"},

	// Footage: looking and listening.
	"footage.transcript":      {"what is said", "what they say", "the words spoken", "timed transcript", "subtitles of a clip"},
	"footage.moments":         {"strongest moments", "best moments", "highlights", "best takes", "hooks", "best opening", "best quotes"},
	"footage.evidence":        {"pauses", "silences", "black frames", "frozen frames", "scene changes", "loudness", "dead air"},
	"footage.visuals":         {"what's on screen", "what can be seen", "shot type", "text on screen", "words written on screen"},
	"footage.speakers":        {"who is speaking", "who talks", "the voices", "how long each person speaks"},
	"footage.sheet":           {"contact sheet", "frame grid", "every shot at a glance", "thumbnails"},
	"footage.frames":          {"still frame", "grab a frame", "screenshot of the video", "a picture at a time"},
	"footage.faces":           {"faces show up", "who appears", "who is on screen", "faces seen"},
	"footage.faces.name":      {"that face is", "whose face", "name a face", "tag a face", "this person is"},
	"footage.switches":        {"picture switches", "camera switch", "who is on screen in each shot", "call recording switches"},
	"footage.analyze":         {"analyze again", "process the footage", "transcribe the footage", "look at new footage"},
	"footage.speakers.rename": {"name a voice", "who this voice is", "call the speaker"},
	"search":                  {"find the part where", "when does", "shows up", "appears", "find a moment", "where someone says", "find a clip"},
	"transcript.find":         {"find every place someone says", "find words", "where is it said"},

	// The Library.
	"library.list":      {"my inbox", "the library", "new footage not in a project"},
	"asset.list":        {"clips in a project", "list the clips", "my footage", "what footage"},
	"asset.card":        {"about a clip", "where a clip came from", "info about a clip"},
	"asset.trash":       {"throw away clips", "put in the trash", "discard clips"},
	"asset.restore":     {"bring back clips", "undo the trash", "clips I threw away", "take out of the trash"},
	"asset.delete":      {"delete a clip for good", "delete forever", "remove the file"},
	"asset.archive":     {"put away old takes", "keep for later", "out of my inbox"},
	"asset.move":        {"move clips to a project", "from the inbox into a project"},
	"asset.update":      {"rename a clip", "give a clip a name"},
	"trash.list":        {"what's in the trash", "thrown away clips"},
	"trash.empty":       {"empty the trash", "delete everything in the trash"},
	"collection.create": {"new collection", "make a collection", "a shelf for clips"},
	"collection.add":    {"put clips on a collection", "add to a collection"},
	"ingest.files":      {"import files", "import a folder", "upload clips", "bring in footage", "add my recordings"},
	"ingest.url":        {"import a video from a link", "YouTube video", "TikTok", "download a video", "bring in a link"},
	"asset.precheck":    {"already uploaded", "do I have these files", "duplicates before upload"},
	"watch.set":         {"watch a folder", "import new files by themselves", "watched folder"},

	// The menu of abilities (D272).
	"catalogue.list": {"what can MetaRush do", "what works on this computer", "all the abilities", "built in abilities", "the menu"},
	"catalogue.show": {"what an ability needs", "does it work here"},

	// Cuts: reading and checking.
	"cut.show":         {"the cut as text", "cut sheet", "read the cut", "print the cut"},
	"cut.check":        {"check my cut sheet", "check before saving", "is my edit ok"},
	"cut.apply":        {"save the cut sheet", "save my edit as a new version"},
	"cut.diff":         {"what did I change", "my changes to the cut sheet", "edited cut sheet against the latest"},
	"cut.compare":      {"compare two versions as cut sheets", "what changed between versions"},
	"revision.compare": {"compare two versions", "differences between versions"},
	"revision.list":    {"versions of a project", "all versions", "version history"},
	"revision.show":    {"open a version", "a version in full", "everything in a version"},
	"edit.check":       {"dry run an edit plan", "check an edit plan", "get a confirm token"},
	"edit.apply":       {"apply an edit plan", "save a checked plan"},
	"edit.preview":     {"try a change without saving", "preview a change", "what would this change do"},

	// Cuts: small edits.
	"cut.removeWords": {"cut out words", "the bit where I say", "remove the ums", "filler words", "false starts", "repeats", "take out what someone says"},
	"cut.move":        {"move a clip", "reorder clips", "put a clip at the start", "swap clips"},
	"cut.split":       {"split a clip", "cut a clip in two", "split at a time"},
	"cut.trim":        {"trim a clip", "shorten a shot", "start later", "end earlier", "end the shot after those words"},
	"cut.delete":      {"take a clip out", "remove a clip from the cut", "drop a shot", "delete a clip from the cut"},
	"cut.deleteRange": {"take out everything between two times", "cut a stretch", "remove from one time to another"},
	"cut.add":         {"add a clip to the cut", "put a clip at the end", "insert footage"},
	"cut.transition":  {"dissolve", "crossfade", "fade out", "wipe", "transition between scenes", "split edit", "sound starts before the picture", "start before we see them", "smoother cuts"},
	"cut.names":       {"cover the names the call app printed", "name bar over the call names"},
	"cut.nest":        {"a cut inside another cut", "nested cut", "put another project's cut in this one"},
	"cut.framing":     {"follow the subject", "keep faces in frame", "crop for vertical"},
	// Words changed in the speaker's own voice (D308).
	"cut.say.estimate": {"what would changing a word they said cost", "price saying it differently"},
	"cut.say.make":     {"change a word someone said", "say it differently", "they said the wrong number", "replace a spoken word in their voice", "misspoke"},
	"cut.say.place":    {"put the new words in the cut", "use the changed words"},
	"cut.say.undo":     {"put back what they really said", "undo the changed words"},
	"cut.say.list":     {"which words were changed in their voice", "changed words"},

	// Words, captions, people.
	"transcript.fix":          {"retype what is said", "the transcript is wrong", "fix a word", "correct the transcript"},
	"transcript.replace":      {"fix a word everywhere", "replace a misheard name", "spelled wrong everywhere"},
	"transcript.undo":         {"undo a transcript fix", "take back my fix"},
	"transcript.spelling.add": {"always spell", "spelling of a name", "how to write our company name"},
	"transcript.pending":      {"fixes waiting for a new version", "text fixes not in the cut yet"},
	"captions.file":           {"captions file", "SRT", "VTT", "subtitles file"},
	"captions.check":          {"check the captions", "names spelled right in the captions"},
	"people.list":             {"who MetaRush knows", "known people", "everyone named", "the people"},
	"people.show":             {"every video someone speaks in", "about a person"},
	"people.merge":            {"same person", "combine two people", "merge people"},
	"todos.answer":            {"answer the question", "reply to a question"},
	"todos.dismiss":           {"skip a question", "ignore that question", "doesn't matter"},
	"todos.list":              {"open questions", "what MetaRush is asking me", "my to-dos"},

	// Sound and narration.
	"sound.levels":       {"too loud", "too quiet", "turn down", "turn up", "volume", "camera sound off", "mute the original sound", "even out the voices"},
	"sound.music.add":    {"add music", "add a song", "music under the video", "background music"},
	"sound.music.set":    {"music ends on a beat", "fit the music to the video", "music ending", "music level", "music fade"},
	"sound.enhance":      {"clearer voice", "clean up the voice", "remove background noise", "hard to hear"},
	"voiceover.line":     {"add a voiceover line", "add a narration line", "change a line of the narration"},
	"voiceover.tts":      {"AI voice", "generate the narration", "have an AI voice read it", "ElevenLabs narration"},
	"voiceover.scratch":  {"scratch voice", "free voice to check timing", "temporary narration"},
	"voiceover.askFit":   {"fit my script to the video", "make my lines fit the shots", "keep my words"},
	"voiceover.voices":   {"which voices", "voices that can read", "narrator voices", "AI voices"},
	"voiceover.estimate": {"what the AI voice costs", "price of the narration"},

	// Renders, checks, delivery.
	"render.submit":      {"review copy", "render it", "make a video file", "watch it", "final version", "vertical version", "make an MP4"},
	"render.list":        {"renders of a project", "finished videos", "all renders"},
	"render.cancel":      {"stop a render", "cancel a render"},
	"render.status":      {"is the render done", "how far is the render"},
	"qa.show":            {"what's wrong with the render", "problems in the render", "quality check report", "is anything broken"},
	"qa.run":             {"run the quality check", "check the render again"},
	"revision.approve":   {"approve a version", "sign off on a version"},
	"revision.askReview": {"ask for a review", "ask the team to review", "send for review"},
	"deliver.export":     {"export the approved cut", "deliver the finals", "copy finished videos to a folder", "hand off", "client folder"},
	"export.timeline":    {"DaVinci Resolve", "Final Cut Pro", "Premiere", "open in another editor", "XML timeline"},
	"cut.signoffs.list":  {"sign-offs a cut needs", "who still has to OK it"},

	// Graphics, brand, resources.
	"edit.templates":     {"graphics templates", "lower thirds", "title cards", "end cards"},
	"motion.check":       {"check my graphic", "check an animation", "test the animated graphic"},
	"motion.approve":     {"approve a graphic", "approve the animation"},
	"motion.new":         {"make an animated graphic", "custom animation", "counter that counts up"},
	"brand.show":         {"brand colours", "our fonts and logo", "brand kit"},
	"brand.suggest":      {"brand from our website", "take the colours from the site"},
	"resources.add":      {"add a logo", "add a picture", "add an image file", "add a font", "upload a logo", "add a PDF"},
	"resources.fetch":    {"GitHub repository", "a repo", "web page", "website snapshot"},
	"resources.list":     {"pictures and logos", "what pictures can I use", "our logos"},
	"resources.where":    {"where is it used", "which cuts use this picture", "where the logo is used"},
	"content.style.show": {"house style", "how new cuts start"},

	// Generated video.
	"generate.shot":     {"generate a shot", "AI video of", "make a shot of"},
	"generate.estimate": {"how much a generated shot costs", "price a generated shot"},
	"generate.list":     {"generated shots we made", "what generated video cost", "generated clips"},
	"generate.again":    {"more takes", "variations of a generated shot"},
	"generate.extend":   {"extend a clip", "a few more seconds", "continue the shot"},

	// Recipes and Remotion.
	"recipe.again":        {"make it again", "do it again with", "again with the new art"},
	"recipe.plan":         {"what a run costs", "estimate a run", "plan a run"},
	"recipe.list":         {"our recipes", "list recipes"},
	"recipe.starters":     {"starter recipes", "ready-made recipes", "templates to start from"},
	"recipe.starters.add": {"add the starter recipes", "start from a ready-made recipe"},
	"recipe.sample":       {"approve the sample", "the sample sounds right", "sample first"},
	"remotion.map":        {"which scene says", "scenes of a Remotion project", "where the words are written"},
	"remotion.words.set":  {"change the headline", "change the words on screen", "change the text", "edit the copy"},
	"remotion.lift":       {"a graphic I can reuse", "turn a scene into a graphic"},

	// Calls.
	"conversation.import":  {"Zoom recordings", "a call's recordings", "Riverside", "podcast recording"},
	"conversation.moments": {"best moments of the call", "moments for shorts"},
	"conversation.short":   {"make a short", "a short from the call", "clip for social"},

	// Screen recordings made in the dashboard.
	"recording.cut":    {"cut from my screen recording", "screen and camera in a corner", "picture in picture of the demo", "edit the demo I recorded"},
	"recording.list":   {"my screen recordings", "recordings I made", "demos I recorded"},
	"recording.finish": {"keep what was recorded", "the tab closed while recording", "recording didn't finish"},
	"recording.link":   {"put my screen recording in a project"},

	// The setup.
	"activity.summary":  {"what is MetaRush doing", "what's happening right now", "is anything running", "progress"},
	"machine.list":      {"computers in the workspace", "which machines", "my other computers"},
	"job.show":          {"why is my job stuck", "what a job is waiting for"},
	"job.retry":         {"retry a failed job", "try again"},
	"cache.prune":       {"free up space", "disk space", "clear the cache"},
	"cache.prefetch":    {"work offline", "download everything for offline"},
	"notification.list": {"my notifications", "the bell", "what's new for me"},
	"log.list":          {"activity log", "what happened", "errors in the log"},
	"provider.set":      {"save an API key", "ElevenLabs key", "add a key", "set up a key"},
	"provider.status":   {"is the key set up", "ElevenLabs key status"},
	"remote.status":     {"open the dashboard from another computer", "remote access", "use MetaRush from my other computers"},
	"improve.signals":   {"what should MetaRush add", "what's missing", "what keeps going wrong", "feature gaps", "more useful"},
	"improve.list":      {"improvement ideas", "feature ideas"},
	"assistant.status":  {"is Claude Code ready", "Claude Code set up"},
	"users.list":        {"who has an account", "people in the workspace", "accounts"},
	"users.invite":      {"invite a colleague", "invite someone by email", "add a teammate"},
	"version.show":      {"which version of MetaRush", "what's new in this version"},
	"storage.migrate":   {"move the media", "another storage bucket", "switch storage"},
}

// synonyms are words a request may use for the catalogue's own words. Both
// sides are plain words; they are stemmed when the ranking stems. A key of two
// words or more is a phrase: it counts only when the request has its words
// next to each other, in that order ("lower third", not "third" alone).
var synonyms = map[string][]string{
	// What people call things, and what the catalogue calls them.
	"clip": {"asset", "footage"}, "footage": {"asset"}, "shot": {"clip"}, "recording": {"footage"},
	"version": {"revision"}, "computer": {"machine"}, "mac": {"machine"}, "laptop": {"machine"},
	"narration": {"voiceover"}, "narrator": {"voiceover"}, "subtitle": {"caption"},
	"animation": {"motion graphic"}, "animated": {"motion graphic"}, "graphic": {"motion", "template"},
	"cost": {"estimate", "price"}, "price": {"estimate", "cost"}, "how much": {"estimate", "cost", "price"},
	"image": {"picture"}, "photo": {"picture"}, "repo": {"repository"},
	"headline": {"words"}, "audio": {"sound"}, "volume": {"level"}, "loud": {"level"}, "quiet": {"level"},
	"song": {"music"}, "track": {"music"}, "colleague": {"person", "user"}, "teammate": {"person", "user"},
	"find": {"search"}, "wrong": {"problem"}, "broken": {"problem"}, "stuck": {"wait"},
	"said": {"transcript", "words"}, "say": {"transcript", "words"}, "talking": {"speaker"}, "speaking": {"speaker"},
	"call": {"conversation"}, "zoom": {"conversation"}, "podcast": {"conversation"},
	// Phrases.
	"review copy": {"render", "review"}, "quality check": {"qa"}, "sign off": {"signoff"},
	"for good": {"delete", "forever"}, "threw away": {"trash"}, "thrown away": {"trash"},
	"bring back": {"restore"}, "put back": {"restore"}, "cut out": {"remove"}, "take out": {"remove"},
	"leave out": {"remove"}, "get rid": {"remove"}, "shows up": {"on screen"}, "show up": {"on screen"},
	"lower third": {"name", "title", "template"}, "end card": {"template"}, "title card": {"template"},
	"api key": {"provider", "key"}, "right now": {"activity"}, "free up": {"prune", "cache"},
	"new project": {"create"}, "new collection": {"create"},
}

// synonymSets are the synonyms split into single words and phrases, as terms.
type synonymSets struct {
	words   map[string][]string // a request's term → the catalogue's terms
	phrases []phrase
}

type phrase struct {
	terms []string // the phrase's terms, in order
	to    []string // the catalogue's terms it stands for
}

func buildSynonyms(stem bool) synonymSets {
	s := synonymSets{words: map[string][]string{}}
	for k, vs := range synonyms {
		var to []string
		for _, v := range vs {
			to = append(to, Terms(v, stem)...)
		}
		if len(splitWords(k)) > 1 {
			// Every word of a phrase counts, small ones too ("how much").
			s.phrases = append(s.phrases, phrase{terms: allTerms(k, stem), to: to})
			continue
		}
		for _, kt := range Terms(k, stem) {
			s.words[kt] = append(s.words[kt], to...)
		}
	}
	return s
}

var (
	stemmedSynonyms = sync.OnceValue(func() synonymSets { return buildSynonyms(true) })
	plainSynonyms   = sync.OnceValue(func() synonymSets { return buildSynonyms(false) })
)

func synonymSet(stem bool) synonymSets {
	if stem {
		return stemmedSynonyms()
	}
	return plainSynonyms()
}

// synonymsOf are the catalogue's terms for one term of a request.
func synonymsOf(term string, stem bool) []string {
	return synonymSet(stem).words[term]
}

// phraseTerms are the catalogue's terms for the phrases a request contains
// ("lower third" → name bar).
func phraseTerms(request string, stem bool) []string {
	ts := allTerms(request, stem)
	var out []string
	for _, p := range synonymSet(stem).phrases {
		if hasRun(ts, p.terms) {
			out = append(out, p.to...)
		}
	}
	return out
}

// hasRun says whether ts holds run's terms next to each other, in order.
func hasRun(ts, run []string) bool {
	for i := 0; i+len(run) <= len(ts); i++ {
		ok := true
		for j, r := range run {
			if ts[i+j] != r {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// allTerms are a text's words as Terms makes them, stop words kept: what
// phrases are matched on.
func allTerms(text string, stem bool) []string {
	var out []string
	for _, w := range splitWords(text) {
		w = strings.ToLower(w)
		if canonical, ok := spelling[w]; ok {
			w = canonical
		}
		if stem {
			w = Stem(w)
		}
		out = append(out, w)
	}
	return out
}
