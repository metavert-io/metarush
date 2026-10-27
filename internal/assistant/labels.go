package assistant

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/resources"
)

// Readable one-line descriptions of what Claude Code did, in plain words for an
// amateur editor ("Read the transcript of the kitchen interview (0:11-0:18)",
// "Checked the change “tighten” → looks good, 2 notes"). The raw command stays in
// ToolCall.Summary/Input for the raw view.

var idRe = regexp.MustCompile(`^(prj|ast|rev|rnd|qar|job)_[0-9a-z]{26}$`)

// nameOf turns an ID into a short readable name when it can.
func (p *parser) nameOf(id string) string {
	if !idRe.MatchString(id) {
		return ""
	}
	return p.names(id)
}

func (p *parser) thing(id, fallback string) string {
	if n := p.nameOf(id); n != "" {
		return n
	}
	return fallback
}

func mmss(v string) string {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return v
	}
	m := int(f) / 60
	s := int(f) % 60
	return fmt.Sprintf("%d:%02d", m, s)
}

func rangeLabel(from, to string) string {
	switch {
	case from != "" && to != "":
		return " (" + mmss(from) + "-" + mmss(to) + ")"
	case from != "":
		return " (from " + mmss(from) + ")"
	case to != "":
		return " (up to " + mmss(to) + ")"
	}
	return ""
}

func first(xs []string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return ""
}

func planName(path string) string {
	b := filepath.Base(path)
	b = strings.TrimSuffix(strings.TrimSuffix(b, ".json"), ".plan")
	return strings.ReplaceAll(strings.ReplaceAll(b, "-", " "), "_", " ")
}

// quoted marks people's own words (a name Claude gave a change, a graphic, a
// conversation): theirs, so it never quotes twice.
func quoted(s string) string { return theirs(s) }

// upperFirst capitalizes the first letter ("fifteen second cut" → "Fifteen second cut").
func upperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// skillLabels say what a skill is for, in the person's terms.
var skillLabels = map[string]string{
	"video-footage-review":  "Got ready to review the footage",
	"video-story-editor":    "Got ready to build a cut",
	"video-edit-revise":     "Got ready to edit the cut",
	"video-render":          "Got ready to render",
	"video-qa":              "Got ready to check the render",
	"video-deliver":         "Got ready to export",
	"video-motion-design":   "Got ready to add graphics",
	"video-ingest":          "Got ready to bring in footage",
	"video-project":         "Looked at your projects",
	"video-workspace-admin": "Checked the setup",
}

// bashLabel describes a metarush command (other shell commands are rare: the
// allowlist only runs metarush).
func (p *parser) bashLabel(c *cliCall, raw string) string {
	if c == nil {
		// "metarush … --json 2>&1 | head": describe the metarush part.
		if head := leadingCommand(raw); head != "" {
			if words, err := splitCommand(head); err == nil {
				if call, ok := parseMetarush(words); ok {
					return p.bashLabel(&call, head)
				}
			}
		}
		return "Tried something that isn't allowed here"
	}
	if l := recipeLabel(c); l != "" { // Recipes (recipe_actions.go)
		return l
	}
	arg := first(c.Args)
	switch c.Path {
	case "footage transcript", "transcript", "transcript show":
		return "Read the transcript of " + p.thing(arg, "the footage") + rangeLabel(c.Flags["from"], c.Flags["to"])
	case "transcript find":
		return "Looked for " + quoted(clip(arg, 50)) + " in the transcripts"
	case "transcript history":
		return "Read the transcript fixes of " + p.thing(arg, "the footage")
	case "transcript spelling list":
		return "Read the spelling lists"
	case "transcript spelling suggestions":
		return "Looked for words to put on the spelling list"
	case "transcript pending list":
		return "Checked the text fixes waiting for " + p.thing(arg, "the cut")
	case "captions check":
		return "Checked the captions of " + p.thing(arg, "the cut")
	case "captions file":
		return "Read the captions file of " + p.thing(arg, "the cut")
	case "captions languages":
		return "Checked which languages the captions of " + p.thing(arg, "the cut") + " are in"
	case "captions lines":
		return "Read the captions of " + p.thing(arg, "the cut") + " to translate them"
	case "captions translate":
		return "Asked to keep translated captions for " + p.thing(arg, "the cut")
	case "captions remove-language":
		return "Asked to take translated captions off " + p.thing(arg, "the cut")
	case "footage evidence":
		return "Checked the pauses, scene changes and sound in " + p.thing(arg, "the footage")
	case "footage moments":
		l := "Looked for the best moments"
		if q := c.Flags["query"]; q != "" {
			l += " about " + quoted(clip(q, 50))
		}
		if n := p.thing(first(c.Args), ""); n != "" {
			l += " in " + n
		} else if n := p.thing(c.Flags["project"], ""); n != "" {
			l += " in " + n
		}
		return l
	case "footage sheet":
		return "Pulled stills from " + p.thing(arg, "the footage") + rangeLabel(c.Flags["from"], c.Flags["to"])
	case "footage keyframes":
		return "Chose stills to look at in " + p.thing(arg, "the footage") + rangeLabel(c.Flags["from"], c.Flags["to"])
	case "footage visuals":
		return "Read what's on screen in " + p.thing(arg, "the footage") + rangeLabel(c.Flags["from"], c.Flags["to"])
	case "footage visuals status":
		return "Checked whether clips get looked at"
	case "footage faces", "footage faces list":
		return "Looked at the faces in " + p.thing(arg, "the footage")
	case "footage faces name":
		return "Asked to name a face"
	case "footage faces reject":
		return "Asked to correct a face"
	case "footage describe":
		return "Asked to describe the scenes of " + p.thing(arg, "the footage")
	case "footage frames":
		if at := strings.TrimSpace(c.Flags["at"]); at != "" {
			return fmt.Sprintf("Pulled %s from %s", pluralize(len(strings.Split(at, ",")), "still"), p.thing(arg, "the footage"))
		}
		return "Pulled stills from " + p.thing(arg, "the footage")
	case "footage analyze":
		return "Asked to analyze the footage"
	case "edit check", "edit plan validate":
		path := c.Flags["plan"]
		if path == "" {
			path = arg
		}
		return "Checked the change " + quoted(clip(planName(path), 60))
	case "edit apply":
		return "Asked to save the change " + quoted(clip(planName(c.Flags["plan"]), 60))
	case "edit preview":
		return "Tried out a change"
	case "edit templates":
		return "Looked at the graphics templates"
	case "edit transitions":
		return "Looked at the transitions"
	case "cut captions":
		return "Asked to change how the captions look"
	case "footage short":
		return "Asked to make a short"
	case "cut transition":
		return "Asked to change a transition"
	case "cut add":
		return "Asked to add footage to the cut"
	case "cut move":
		return "Asked to move a clip"
	case "cut trim":
		return "Asked to trim a clip"
	case "cut split":
		return "Asked to split a clip"
	case "cut delete":
		return "Asked to remove clips"
	case "cut delete-range":
		return "Asked to cut out part of the video"
	case "cut remove-words":
		return "Asked to take words out"
	case "cut pauses":
		return "Asked to shorten long pauses"
	case "cut say estimate":
		return "Priced saying " + quoted(clip(c.Flags["as"], 40)) + " in a speaker's own voice"
	case "cut say make":
		return "Asked to say " + quoted(clip(c.Flags["as"], 40)) + " in a speaker's own voice"
	case "cut say place":
		return "Asked to put changed words into the cut"
	case "cut say undo":
		return "Asked to put back what was said"
	case "cut say mouth":
		return "Asked to match the mouth to new words"
	case "cut say list", "cut say show":
		return "Read the words changed in speakers' voices"
	case "revision show":
		return "Looked at " + p.thing(arg, "a version of the cut")
	case "revision list":
		return "Looked at the versions of the cut"
	case "revision compare":
		other := ""
		if len(c.Args) > 1 {
			other = c.Args[1]
		}
		return "Compared " + p.thing(arg, "one version") + " with " + p.thing(other, "another")
	case "revision plan":
		return "Checked the exact timing of " + p.thing(arg, "the cut")
	case "project list":
		return "Looked at your projects"
	case "project show":
		return "Opened " + p.thing(arg, "the project")
	case "asset list":
		return "Looked at the footage"
	case "asset show":
		return "Looked at " + p.thing(arg, "a clip")
	case "render list", "render status":
		return "Checked the renders"
	case "render submit":
		if c.Flags["quality"] == "final" {
			return "Asked for the final render of " + p.thing(c.Flags["revision"], "the cut")
		}
		return "Asked for a review copy of " + p.thing(c.Flags["revision"], "the cut")
	case "qa show":
		return "Read the quality check"
	case "qa run":
		return "Asked to check the render"
	case "schema show", "schema list":
		return "Looked up what a cut can contain"
	case "job list", "job show", "job wait":
		return "Checked what is running"
	case "footage speakers", "footage speakers list":
		return "Checked who speaks in " + p.thing(arg, "the footage")
	case "footage speakers rename":
		return "Asked to name a voice in " + p.thing(arg, "the footage")
	case "footage speakers reject":
		return "Asked to correct a voice in " + p.thing(arg, "the footage")
	case "footage speakers hint":
		return "Asked to find the voices in " + p.thing(arg, "the footage") + " again"
	case "footage language":
		if _, ok := c.Flags["set"]; ok {
			return "Asked to set the language " + p.thing(arg, "the footage") + " is spoken in"
		}
		return "Checked which language " + p.thing(arg, "the footage") + " is spoken in"
	case "people list", "people show":
		return "Looked at the people you named"
	case "people rename", "people merge", "people split":
		return "Asked to update the people you named"
	case "todos list", "todos show", "todos count":
		return "Looked at the open questions"
	case "todos answer":
		return "Asked to answer a question"
	case "todos dismiss":
		return "Asked to skip a question"
	case "workspace show":
		return "Looked at the workspace"
	case "doctor":
		return "Checked the setup"
	case "worker list", "machine list":
		return "Checked which machines run MetaRush"
	case "worker settings":
		return "Checked this machine's settings"
	case "cut show":
		return "Read the cut sheet"
	case "cut check":
		return "Checked a cut sheet"
	case "cut diff":
		return "Compared two cut sheets"
	case "cut where", "asset where":
		return "Checked where " + p.thing(arg, "it") + " is used"
	case "asset card":
		return "Read the info card of " + p.thing(arg, "a clip")
	case "asset fixes":
		if !readOnlyCall(*c) {
			return "Asked to fix " + p.thing(arg, "a clip")
		}
		return "Checked the fixes for " + p.thing(arg, "a clip")
	case "asset names":
		if !readOnlyCall(*c) {
			return "Asked to use better names for the clips"
		}
		return "Looked at the better names for the clips"
	case "asset update":
		return "Asked to rename " + p.thing(arg, "a clip")
	case "cut rename":
		return "Asked to rename the cut"
	case "cut use-fixed":
		return "Asked to use a fixed copy in the cut"
	case "revision note":
		return "Asked to change the note on " + p.thing(arg, "a version")
	case "project receipt":
		return "Read the receipt of " + p.thing(arg, "the project")
	case "deliver notes show":
		return "Read the delivery notes of " + p.thing(arg, "the cut")
	case "deliver ready":
		return "Checked whether " + p.thing(arg, "the cut") + " can go out"
	case "deliver notes set":
		return "Asked to keep delivery notes for " + p.thing(arg, "the cut")
	case "project status":
		return "Checked how " + p.thing(arg, "the project") + " is doing"
	case "project links":
		return "Looked at the linked projects"
	case "sound show":
		return "Looked at the sound"
	case "sound compare":
		return "Compared how it sounds"
	case "library":
		return "Looked in the Library"
	case "collection list", "collection show":
		return "Looked at the collections"
	case "trash list":
		return "Looked in the Trash"
	case "search":
		return "Searched your footage"
	case "activity":
		return "Checked what MetaRush is doing"
	case "notifications list":
		return "Read the notifications"
	case "watch status":
		return "Checked the watched folder"
	case "ingest precheck":
		return "Checked files before adding them"
	case "ingest sessions":
		return "Checked recent imports"
	case "audit", "log list", "log tail", "log search", "log settings", "log claude-hooks status":
		return "Read the activity log"
	case "cache status":
		return "Checked this machine's copies"
	case "sync status":
		return "Checked what is synced"
	case "renderer status":
		return "Checked that this machine can render"
	case "service status":
		return "Checked that MetaRush runs in the background"
	case "extension list", "extension path":
		return "Checked the browser extension"
	case "ops list":
		return "Looked up what MetaRush can do"
	case "version":
		return "Checked the MetaRush version"
	}
	if l := resourceLabel(c); l != "" {
		return l
	}
	if l := narrationLabel(p, c); l != "" {
		return l
	}
	if l := callSoundLabel(p, c); l != "" {
		return l
	}
	if l := keepLabel(c); l != "" {
		return l
	}
	if l := contentLabel(c); l != "" {
		return l
	}
	if l := aiLabel(c); l != "" { // AI services (ai_actions.go)
		return l
	}
	// Anything else: what kind of thing it was, never the command itself.
	if readOnlyCall(*c) {
		return "Looked something up in MetaRush"
	}
	return "Asked MetaRush to make a change"
}

// contentLabel describes the Content Settings commands (D192, D198–D200).
func contentLabel(c *cliCall) string {
	switch c.Path {
	case "content", "content show":
		return "Looked at the project's Content Settings"
	case "content rules", "content rules list":
		return "Read the house rules"
	case "content style", "content style show":
		return "Looked at the house style"
	case "content signoffs", "content signoffs list":
		return "Looked at the house sign-offs"
	case "content pack", "content pack show":
		return "Looked at the layout pack"
	case "content pack preview":
		return "Looked at previews of the layout pack"
	case "content pack propose":
		return "Proposed a layout pack"
	case "content pack set", "content pack accept", "content pack decline":
		return "Asked to change the layout pack"
	case "content pack apply":
		return "Asked to use the layout pack on the latest cut"
	case "content style set", "content style apply", "content signoffs add", "content signoffs remove", "content rules add", "content rules remove",
		"content logo set", "content logo remove", "content follow", "content own", "content default":
		return "Asked to change the Content Settings"
	}
	return ""
}

// resourceLabel describes the resources, brand kit and motion graphics commands.
func resourceLabel(c *cliCall) string {
	named := func(d string) string {
		if n := first(c.Args); resources.NamePattern.MatchString(n) {
			return "“" + n + "”"
		}
		return d
	}
	switch c.Path {
	case "resources list", "resources folders":
		return "Looked at the resources"
	case "resources show":
		return "Looked at " + named("a resource")
	case "resources path":
		return "Read " + named("a resource")
	case "resources where":
		return "Checked where " + named("a resource") + " is used"
	case "resources add", "add":
		return "Asked to bring in a repository or page"
	case "resources make":
		switch first(c.Args) {
		case "code":
			return "Asked to make a code slide"
		case "diagram":
			return "Asked to make a diagram"
		case "stats":
			return "Asked to make a repository card"
		}
		return "Asked to make a slide picture"
	case "resources refresh":
		return "Asked to look at " + named("a resource") + " again"
	case "resources pictures": // D216
		if len(c.Multi["keep"])+len(c.Multi["drop"]) > 0 {
			return "Asked to change which folders of " + named("a repository") + " it keeps pictures from"
		}
		return "Looked at the pictures " + named("a repository") + " keeps"
	case "brand show":
		return "Looked at the brand kit"
	case "motion list", "motion show":
		return "Looked at the graphics"
	case "remotion list", "remotion show":
		return "Looked at the Remotion projects"
	case "remotion map":
		return "Looked at the scenes of a Remotion project"
	case "remotion words":
		return "Looked at the words of a Remotion project"
	case "remotion pictures list":
		return "Looked at the pictures of a Remotion project"
	case "remotion words set", "remotion words setup", "remotion pictures set", "remotion pictures setup", "remotion lift":
		return "Asked to change a Remotion project"
	case "remotion refresh plan", "remotion refresh runs", "remotion refresh log":
		return "Looked at refreshing a Remotion project from its own steps"
	case "remotion push plan", "remotion push list":
		return "Looked at sending a Remotion project's changes back"
	case "remotion refresh start", "remotion push start":
		return "Asked you to start it yourself"
	case "motion check":
		return "Asked to check a graphic it made"
	case "motion approve", "motion revoke":
		return "Asked you to decide on a graphic"
	}
	return ""
}

// callSoundLabel describes the conversation and sound commands.
func callSoundLabel(p *parser, c *cliCall) string {
	conv := "the conversation"
	if n := strings.TrimSpace(first(c.Args)); n != "" && !strings.HasPrefix(n, "cnv_") && utf8.RuneCountInString(n) <= 60 {
		conv = quoted(n)
	}
	of := ""
	if n := p.thing(c.Flags["revision"], p.thing(c.Flags["project"], "")); n != "" {
		of = " of " + n
	}
	switch c.Path {
	case "conversation", "conversation list":
		return "Looked at the conversations"
	case "conversation show":
		return "Looked at " + conv
	case "conversation styles":
		return "Looked at the styles a conversation's cut can have"
	case "conversation moments":
		return "Looked for the best moments in " + conv
	case "conversation detect":
		return "Checked what a call's files are"
	case "conversation cut":
		return "Asked to make the cut of " + conv
	case "conversation decide":
		switch {
		case hasBool(c, "skip"):
			return "Asked to skip clean-up suggestions"
		case hasBool(c, "reopen"):
			return "Asked to bring back clean-up suggestions"
		}
		return "Asked to accept clean-up suggestions"
	case "conversation angle":
		return "Asked to change what the conversation shows"
	case "conversation short":
		return "Asked to make a short from " + conv
	case "conversation cleanup":
		return "Asked to look " + conv + " over"
	case "conversation import", "conversation create", "conversation sync", "conversation nudge", "conversation update", "conversation pin",
		"conversation match", "conversation settings":
		return "Asked to change a conversation"
	case "sound", "sound show":
		return "Read the sound" + of
	case "sound compare":
		return "Compared the voices before and after they were made clearer"
	case "sound levels":
		return "Asked to change how loud things are"
	case "sound enhance":
		return "Asked to make a voice clearer"
	case "sound music add":
		return "Asked to put music under the cut"
	case "sound music", "sound music set":
		return "Asked to change the music"
	case "sound setup":
		return "Asked to set up clearer voices"
	}
	return ""
}

// narrationLabel describes the narration, mix and AI voice commands.
func narrationLabel(p *parser, c *cliCall) string {
	of := ""
	if n := p.thing(c.Flags["revision"], ""); n != "" {
		of = " of " + n
	}
	switch c.Path {
	case "voiceover script", "voiceover script show":
		return "Read the narration script" + of
	case "voiceover script set", "voiceover script import":
		return "Asked to change the narration script"
	case "voiceover beat add":
		return "Asked to add a narration line"
	case "voiceover beat update":
		return "Asked to change a narration line"
	case "voiceover beat move":
		return "Asked to move a narration line"
	case "voiceover beat remove":
		return "Asked to remove a narration line"
	case "voiceover fit":
		if _, apply := c.Flags["apply"]; apply {
			return "Asked to make the narration fit"
		}
		return "Checked that every narration line fits"
	case "voiceover estimate":
		return "Priced the AI voice"
	case "voiceover tts":
		if hasBool(c, "estimate") {
			return "Priced the AI voice"
		}
		return "Asked to make the AI voice"
	case "voiceover scratch":
		return "Asked to make a free scratch voice"
	case "voiceover voices":
		switch {
		case c.Flags["preview"] != "":
			return "Got a sample of a voice"
		case hasBool(c, "library"):
			return "Looked through the voice library"
		}
		return "Looked at the voices"
	case "voiceover models":
		return "Looked at the AI voice models"
	case "voiceover takes", "voiceover takes list":
		return "Looked at the narration takes"
	case "voiceover takes choose":
		return "Asked to switch a narration take"
	case "voiceover import":
		return "Asked to place a narration recording"
	case "voiceover cleanup":
		return "Asked to clean up the narration recording"
	case "voiceover clone":
		return "Asked to copy a voice"
	case "voiceover record":
		return "Asked to record narration"
	case "mix", "mix show":
		return "Read the sound mix" + of
	case "mix set":
		return "Asked to change the sound mix"
	case "providers", "providers status", "providers list":
		return "Checked the AI voice setup"
	case "providers set", "providers remove", "providers test", "providers settings":
		return "Asked to change the AI voice setup"
	}
	return ""
}

// leadingCommand is the part of a shell line before its first pipe, redirect or
// separator (for labels only; such lines never run through MetaRush's own guard).
func leadingCommand(s string) string {
	cut := len(s)
	for _, op := range []string{"|", ";", "&&", " 2>", " >", "<"} {
		if i := strings.Index(s, op); i >= 0 && i < cut {
			cut = i
		}
	}
	return strings.TrimSpace(s[:cut])
}

var imageExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}

// hashName is a content-addressed object's file name (a SHA-256), meaningless to people.
var hashName = regexp.MustCompile(`^[0-9a-f]{32,}$`)
var framesPathRe = regexp.MustCompile(`(?:^|/)frames/(ast_[0-9a-z]{26})/`)

// step is a tool row in plain words (D140): its line, and how consecutive
// rows of the same kind group into one line ("Looked at 19 stills from
// “Lena interview”"; %d in many is the count).
type step struct {
	label, group, many string
	images             []ImageRef
}

// noteTopics names MetaRush's notes (the skills and their references) by what
// they are about, for the person.
var noteTopics = map[string]string{
	"video-footage-review": "reviewing footage", "video-story-editor": "building a cut", "video-edit-revise": "changing a cut",
	"video-render": "rendering", "video-qa": "checking a render", "video-deliver": "exporting", "video-motion-design": "graphics",
	"video-ingest": "bringing in footage", "video-project": "projects", "video-workspace-admin": "the setup", "video-voiceover": "narration",
	"metarush-improve": "improvement ideas",
	"edit-plans":       "how changes are written", "cut-sheets": "the cut as text", "motion-code": "making graphics", "motion": "graphics",
	"voice": "how to talk about the video", "cli": "what MetaRush can do", "editorial": "editing", "resources": "pictures, logos and slides",
	"safety": "what to trust", "brand": "the brand kit", "captions": "captions", "transitions": "transitions", "sound": "sound",
	"conversations": "calls and conversations", "narration": "narration", "mix": "the sound mix", "qa": "checking a render",
}

// notesTopic is what a skill file is about ("graphics", "the cut as text").
func notesTopic(rel string) string {
	parts := strings.Split(rel, "/")
	base := strings.TrimSuffix(parts[len(parts)-1], filepath.Ext(parts[len(parts)-1]))
	if strings.EqualFold(base, "SKILL") && len(parts) >= 2 {
		base = parts[len(parts)-2]
	}
	if t, ok := noteTopics[base]; ok {
		return t
	}
	for i := len(parts) - 2; i >= 0; i-- {
		if t, ok := noteTopics[parts[i]]; ok {
			return t
		}
	}
	return ""
}

func notesStep(topic string) step {
	label := "Read MetaRush's notes"
	if topic != "" {
		label += " on " + topic
	}
	return step{label: label, group: "notes", many: "Read %d of MetaRush's notes"}
}

// motionDirRe finds a motion graphic's folder in a proposals path (motion/<name>/…).
var motionDirRe = regexp.MustCompile(`(?:^|/)motion/([a-z][a-z0-9-]{0,39})(?:/|$)`)

// spilled reports whether a file is Claude Code's own (the rest of a long
// tool result it saved, a background command's output), not MetaRush's.
func (p *parser) spilled(path string) bool {
	slash := filepath.ToSlash(path)
	base := filepath.Base(slash)
	if strings.Contains(slash, "/tool-results/") || strings.HasPrefix(base, "toolu_") || strings.HasSuffix(base, ".output") {
		return true
	}
	for _, root := range []string{p.workspace, p.assistant, p.scratch} {
		if root != "" && insideDir(root, path) {
			return false
		}
	}
	for _, alias := range p.aliases {
		if alias[1] != "" && insideDir(alias[1], path) {
			return false
		}
	}
	return filepath.IsAbs(path)
}

func insideDir(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

// readLabel describes a Read in plain words; images inside scratch, the cache
// or the assistant folder become thumbnails.
func (p *parser) readLabel(path string) step {
	ext := strings.ToLower(filepath.Ext(path))
	base := filepath.Base(path)
	ref, servable := p.imageRef(path)
	// Content-addressed cache objects have no extension: precomputed contact
	// sheets and frames are read from there.
	if imageExt[ext] || (servable && ref.Root == "cache") {
		var imgs []ImageRef
		if servable {
			imgs = append(imgs, ref)
		}
		from := ""
		if m := framesPathRe.FindStringSubmatch(filepath.ToSlash(path)); m != nil {
			from = p.nameOf(m[1])
		}
		if from == "" {
			from = p.seen[filepath.Clean(path)]
		}
		of := ""
		if from != "" {
			of = " from " + from
		}
		lower := strings.ToLower(base)
		switch {
		case strings.Contains(lower, "sheet") || strings.Contains(lower, "contact") || ref.Root == "cache":
			return step{label: "Looked at the stills" + of, group: "sheet:" + from, many: "Looked at %d sets of stills" + of, images: imgs}
		case strings.Contains(filepath.ToSlash(path), "/frames/"):
			return step{label: "Looked at a still" + of, group: "still:" + from, many: "Looked at %d stills" + of, images: imgs}
		}
		return step{label: "Looked at a picture" + of, group: "picture:" + from, many: "Looked at %d pictures" + of, images: imgs}
	}
	if p.spilled(path) {
		return step{label: "Read the rest of a long result", group: "spill", many: "Read the rest of %d long results"}
	}
	rel := filepath.ToSlash(p.rel(path))
	proposal := p.proposalRel(path)
	switch {
	case strings.Contains(rel, ".claude/skills/"):
		return notesStep(notesTopic(rel[strings.Index(rel, ".claude/skills/")+len(".claude/skills/"):]))
	case strings.EqualFold(base, "CLAUDE.md") && !strings.Contains(rel, "/"):
		return notesStep("")
	case strings.HasSuffix(base, ".plan.json"):
		return step{label: "Read the change " + quoted(planName(base))}
	case strings.HasSuffix(base, ".cut.md"):
		return step{label: "Read the cut sheet " + quoted(sheetName(base))}
	case strings.HasSuffix(base, QuestionSuffix):
		return step{label: "Read its question for you"}
	case proposal != "" && motionDirRe.MatchString(proposal):
		name := motionDirRe.FindStringSubmatch(proposal)[1]
		return step{label: "Looked at the graphic " + quoted(name), group: "graphic-read:" + name, many: "Looked at the graphic " + quoted(name)}
	case proposal != "":
		return step{label: "Read its draft", group: "draft-read", many: "Read %d of its drafts"}
	}
	return step{label: "Opened something MetaRush saved", group: "stored", many: "Opened %d things MetaRush saved"}
}

// proposalRel is a path inside this conversation's proposals folder
// (t<turn>/…), or "".
func (p *parser) proposalRel(path string) string {
	if p.convDir == "" {
		return ""
	}
	rel, err := filepath.Rel(filepath.Join(p.convDir, "proposals"), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// imageRef maps an absolute path to a servable image reference.
func (p *parser) imageRef(path string) (ImageRef, bool) {
	roots := []struct{ root, dir string }{{"scratch", p.scratch}, {"assistant", p.assistant}}
	for _, alias := range p.aliases {
		roots = append(roots, struct{ root, dir string }{alias[0], alias[1]})
	}
	for _, r := range roots {
		if r.dir == "" {
			continue
		}
		rel, err := filepath.Rel(r.dir, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		return ImageRef{Root: r.root, Path: filepath.ToSlash(rel)}, true
	}
	return ImageRef{}, false
}

// writeLabel describes a Write/Edit by what Claude made, never by its file
// name; files in this conversation's proposals folders are proposals
// (t<turn>/<name>).
func (p *parser) writeLabel(tool, path string) (step, string) {
	write := tool == "Write"
	base := filepath.Base(path)
	proposal := p.proposalRel(path)
	pick := func(wrote, changed string) string {
		if write {
			return wrote
		}
		return changed
	}
	switch {
	case strings.HasSuffix(base, ".plan.json"):
		return step{label: pick("Drafted", "Revised") + " the change " + quoted(planName(base))}, proposal
	case strings.HasSuffix(base, ".render.json"):
		return step{label: "Asked for a render of the new version"}, proposal
	case strings.HasSuffix(base, ".cut.md"):
		return step{label: pick("Wrote", "Revised") + " the cut sheet " + quoted(sheetName(base))}, proposal
	case strings.HasSuffix(base, QuestionSuffix):
		return step{label: pick("Wrote a question for you", "Changed its question for you")}, proposal
	case motionDirRe.MatchString(proposal):
		name := motionDirRe.FindStringSubmatch(proposal)[1]
		label := pick("Wrote", "Changed") + " the graphic " + quoted(name)
		return step{label: label, group: "graphic:" + name, many: label}, proposal
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".mmd", ".d2":
		return step{label: pick("Drafted a diagram", "Changed a diagram")}, proposal
	case ".svg", ".png", ".jpg", ".jpeg":
		return step{label: pick("Drew a picture", "Changed a picture")}, proposal
	}
	return step{label: pick("Wrote a draft", "Changed a draft"), group: "draft", many: pick("Wrote %d drafts", "Changed %d drafts")}, proposal
}

func pluralize(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}

// rememberImages notes which footage the stills in a `footage sheet`/`footage
// frames` result show, so reading them later can say so (contact sheets are
// content-addressed cache objects whose paths carry no name).
func (p *parser) rememberImages(c *callInfo, text string) {
	if c == nil || c.cmd == nil || (c.cmd.Path != "footage sheet" && c.cmd.Path != "footage frames") {
		return
	}
	name := p.thing(first(c.cmd.Args), "")
	i := strings.Index(text, "{")
	if name == "" || i < 0 {
		return
	}
	var v any
	if json.Unmarshal([]byte(text[i:]), &v) != nil {
		return
	}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			if filepath.IsAbs(t) && len(p.seen) < 1000 {
				p.seen[filepath.Clean(t)] = name
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
}

// outcome summarizes a tool result in a few plain words.
func (p *parser) outcome(c *callInfo, text string, isError, image bool) string {
	if c == nil {
		return ""
	}
	if c.name == "Read" && image {
		return ""
	}
	if c.cmd == nil {
		if isError {
			return "didn't work"
		}
		return ""
	}
	i := strings.Index(text, "{")
	if i < 0 {
		if isError {
			return "didn't work"
		}
		return ""
	}
	var env struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(text[i:]), &env) != nil {
		if isError {
			return "didn't work"
		}
		return ""
	}
	if !env.OK {
		return "didn't work"
	}
	var d map[string]json.RawMessage
	_ = json.Unmarshal(env.Data, &d)
	count := func(k string) int {
		var xs []json.RawMessage
		_ = json.Unmarshal(d[k], &xs)
		return len(xs)
	}
	switch c.cmd.Path {
	case "edit check", "edit plan validate":
		var valid bool
		_ = json.Unmarshal(d["valid"], &valid)
		w, e := count("warnings"), count("errors")
		if !valid {
			return pluralize(e, "problem") + " to fix"
		}
		if w > 0 {
			return "looks good, " + pluralize(w, "note")
		}
		return "looks good"
	case "footage transcript", "transcript", "transcript show":
		return pluralize(count("segments"), "line")
	case "footage moments":
		return pluralize(count("moments"), "moment")
	case "footage frames":
		return pluralize(count("frames"), "still")
	case "footage keyframes":
		return pluralize(count("tiles"), "still")
	case "footage visuals":
		return pluralize(count("lines"), "stretch")
	case "footage faces", "footage faces list":
		return pluralize(count("faces"), "face")
	case "revision list":
		return pluralize(count("revisions"), "version")
	case "project list":
		return pluralize(count("projects"), "project")
	case "asset list":
		return pluralize(count("assets"), "clip")
	case "render list":
		return pluralize(count("renders"), "render")
	case "voiceover fit":
		var r struct {
			Over int `json:"over"`
		}
		if _, apply := c.cmd.Flags["apply"]; apply || json.Unmarshal(d["result"], &r) != nil {
			return ""
		}
		switch r.Over {
		case 0:
			return "every line fits"
		case 1:
			return "1 line runs long"
		}
		return strconv.Itoa(r.Over) + " lines run long"
	case "voiceover estimate":
		var credits float64
		if json.Unmarshal(d["credits"], &credits) != nil {
			return ""
		}
		return "about " + thousands(credits) + " credits"
	case "voiceover voices":
		return pluralize(count("voices"), "voice")
	}
	return recipeOutcome(c.cmd.Path, d) // Recipes: a plan's estimate (recipe_actions.go)
}
