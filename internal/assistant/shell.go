package assistant

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// splitCommand splits a shell command into words for the narrow subset the
// assistant understands: plain words and 'single' or "double" quoted strings.
// Anything a shell would interpret (operators, redirects, expansions, globs,
// escapes, comments) is refused, so the words are exactly what a shell would
// pass to the program. It is used to describe commands (tool rows, action
// cards), never to run them.
func splitCommand(s string) ([]string, error) {
	if len(s) > 8192 || strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
		return nil, errors.New("command too long or not text")
	}
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
			i += size
		case r == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("unterminated quote")
			}
			cur.WriteString(s[i+1 : i+1+j])
			inWord = true
			i += j + 2
		case r == '"':
			j := strings.IndexByte(s[i+1:], '"')
			if j < 0 {
				return nil, errors.New("unterminated quote")
			}
			inner := s[i+1 : i+1+j]
			if strings.ContainsAny(inner, "$`\\!") {
				return nil, errors.New("expansion inside double quotes")
			}
			cur.WriteString(inner)
			inWord = true
			i += j + 2
		case r == '#' && inWord:
			// Inside a word a # is itself (a run is app-teaser#4); only a word
			// that starts with one is a comment.
			cur.WriteRune(r)
			i += size
		case strings.ContainsRune(";&|<>()$`\\*?[]{}~#!^\n\r", r):
			return nil, errors.New("shell syntax is not supported")
		case r == '=' && !inWord:
			return nil, errors.New("shell syntax is not supported")
		default:
			cur.WriteRune(r)
			inWord = true
			i += size
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	if len(words) == 0 {
		return nil, errors.New("empty command")
	}
	return words, nil
}

// cliCall is a metarush command line split into its command path, positional
// arguments and flags (a best-effort parse for descriptions).
type cliCall struct {
	Path  string // e.g. "render submit"
	Args  []string
	Flags map[string]string   // --name value / --name=value; booleans have ""
	Multi map[string][]string // every value of a repeated flag (--beat 1 --beat 3)
}

// boolFlags never take a value.
var boolFlags = map[string]bool{"json": true, "wait": true, "dry-run": true, "allow-unlock": true, "allow-draft": true, "pin": true,
	"local": true, "reject": true, "recursive": true, "words": true, "refresh": true, "raw": true, "restore": true, "all-users": true,
	"archived": true, "verbose": true, "help": true, "for-conversation": true, "quiet": true, "details": true, "new-person": true,
	"same-person": true, "clear-notes": true, "more": true, "unsure": true,
	"unread": true, "samples": true, "all": true, "force": true, "keep-both": true, "unassigned": true, "unlink": true, "no-events": true,
	"dismiss": true, "only-new": true, "watch": true, "heavy": true,
	// narration, mix and integrations
	"append": true, "after-previous": true, "lock": true, "continuous": true, "recorded": true, "replace": true, "original": true,
	"processed": true, "keep-original": true, "list": true, "estimate": true, "regenerate": true, "library": true, "list-devices": true,
	"mute": true, "unmute": true, "solo": true, "unsolo": true, "mute-source": true, "unmute-source": true, "reset": true, "yes": true,
	"key-stdin": true, "no-test": true, "allow-unsafe-secret-path": true, "not-my-voice": true, "remove-background-noise": true,
	// cut transition
	"auto": true, "with-picture": true, "end": true,
	// the cut's editing verbs
	"roll": true, "ums": true, "cleanup": true, "muted": true,
	// the Library: collections, the Archive and the Trash
	"create": true, "off": true, "forever": true,
	// generated video, looks and consent
	"no-people": true, "no-real-people": true, "self": true, "adult": true, "public-figure": true, "clear-budget": true, "no-clips": true,
	// call recordings (D160–D162)
	"first-names": true, "full-names": true, "keep-titles": true}

// groupBoolFlags never take a value within these command groups (elsewhere a
// flag of the same name may: voiceover fit --apply <strategy>).
var groupBoolFlags = map[string]map[string]bool{
	"conversation": {"apply": true, "captions": true, "no-name-tags": true, "remove": true, "accept": true, "skip": true, "reopen": true,
		"nest": true, "no-captions": true, "clap": true, "together": true},
	"sound":      {"apply": true, "preview": true, "original": true, "estimate": true, "on": true, "remove": true, "loop": true, "no-loop": true},
	"transcript": {"everywhere": true, "part-of-words": true, "match-case": true, "spelling": true, "fix-existing": true, "no-render": true},
	"captions":   {"version-words": true, "missing": true, "human": true},
	// What a project keeps (D166): keep logo --brand, project rules add --check.
	"keep":    {"brand": true},
	"project": {"check": true},
	// Remotion sources (D131–D176, D209): checks, waiting and what to keep.
	"remotion": {"check": true, "no-wait": true, "again": true, "stills": true, "public": true, "force": true, "keep-cuts": true, "no-pr": true, "no-stills": true, "no-videos": true},
	// names and fixes (generate shot --look takes a value)
	"asset":   {"accept-all": true, "suggested": true, "none": true, "again": true, "look": true},
	"footage": {"no-captions": true},                                                                                                             // footage short --no-captions (D246)
	"cut":     {"suggested": true, "on": true},                                                                                                   // cut captions --on (D245)
	"recipe":  {"new-projects": true, "draft": true},                                                                                             // Recipes (D211): a project per run, a draft
	"ai":      {"loop": true, "instrumental": true, "own-voices": true, "dry-run": true, "wait": true, "none": true, "any": true, "clear": true}, // AI services (D226)
}

// pathBoolFlags never take a value in these commands (elsewhere in their
// group a flag of the same name may: `metarush recipe save --keep <key>`).
var pathBoolFlags = map[string]map[string]bool{
	"recipe picks":       {"keep": true, "drop": true},  // Recipes: first picks kept, one dropped (D215)
	"recipe check":       {"save": true, "write": true}, // Recipes: a sheet saved as a draft (D213)
	"recipe suggestions": {"save": true},                // Recipes: suggested changes saved as a draft (D217)
	"recipe triggers":    {"dismiss": true},             // Recipes: a plan made by itself set aside (D218)
	"deliver notes set":  {"clear": true},               // delivery notes taken off (D237)
}

// commandGroups are metarush commands with subcommands.
var commandGroups = map[string]bool{"project": true, "asset": true, "footage": true, "edit": true, "revision": true, "render": true, "job": true,
	"comment": true, "ops": true, "workspace": true, "cache": true, "machine": true, "qa": true, "schema": true, "skills": true, "sync": true,
	"worker": true, "assistant": true, "watch": true, "people": true, "todos": true, "notifications": true,
	"voiceover": true, "mix": true, "providers": true, "cut": true, "collection": true, "trash": true,
	"motion": true, "resources": true, "brand": true, "conversation": true, "sound": true, "generate": true, "looks": true, "consent": true,
	"transcript": true, "captions": true, "keep": true, "improve": true, "content": true, "remotion": true,
	"recipe": true, // Recipes (D211)
	"ai":     true} // AI services (D226)

// commandAliases are other names cobra accepts for a command group.
var commandAliases = map[string]string{"vo": "voiceover", "narration": "voiceover", "provider": "providers", "integrations": "providers",
	"conversations": "conversation", "call": "conversation", "gen": "generate", "look": "looks"}

// leafSubcommands are subcommands of commands that otherwise take plain words
// as arguments (`metarush ingest footage/` imports a folder).
var leafSubcommands = map[string]map[string]bool{"ingest": {"precheck": true, "sessions": true, "reconcile": true}, "deliver": {"captions": true, "notes": true}}

// commandGroups3 are two-word commands with subcommands of their own.
var commandGroups3 = map[string]bool{"edit plan": true, "footage speakers": true, "voiceover script": true, "voiceover beat": true, "voiceover takes": true,
	"sound music": true, "transcript spelling": true, "transcript pending": true, "cut signoffs": true, "project rules": true,
	"content rules": true, "content style": true, "content signoffs": true, "content logo": true, "content pack": true,
	"remotion pictures": true, "remotion refresh": true, "remotion push": true,
	"ai voices": true, "ai rules": true, // AI services (D226)
	"cut say":       true, // words changed in a speaker's own voice (D308)
	"deliver notes": true} // delivery notes (D237)

// leafSubcommands3 are subcommands of two-word commands that otherwise take a
// name (`metarush remotion words teaser` lists them; `… words set teaser` changes them).
var leafSubcommands3 = map[string]map[string]bool{"remotion words": {"set": true, "setup": true}}

// parseMetarush parses the words after `metarush`. ok is false when the
// command is not a metarush invocation.
func parseMetarush(words []string) (cliCall, bool) {
	if len(words) == 0 {
		return cliCall{}, false
	}
	first := words[0]
	if first != "metarush" && !strings.HasSuffix(first, "/metarush") {
		return cliCall{}, false
	}
	call := cliCall{Flags: map[string]string{}, Multi: map[string][]string{}}
	var path []string
	rest := words[1:]
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if strings.HasPrefix(w, "--") {
			name, val, hasVal := strings.Cut(strings.TrimPrefix(w, "--"), "=")
			isBool := boolFlags[name] || (len(path) > 0 && groupBoolFlags[path[0]][name]) || pathBoolFlags[strings.Join(path, " ")][name]
			if !hasVal && !isBool && i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "--") {
				val = rest[i+1]
				i++
			}
			call.Flags[name] = val
			call.Multi[name] = append(call.Multi[name], val)
			continue
		}
		if strings.HasPrefix(w, "-") && len(w) == 2 { // -r and the like
			call.Flags[w[1:]] = ""
			continue
		}
		if len(path) == 0 && commandAliases[w] != "" {
			w = commandAliases[w]
		} else if len(path) == 1 && path[0] == "voiceover" && w == "line" {
			w = "beat"
		}
		isName := len(path) == 0 || (len(path) == 1 && (commandGroups[path[0]] || leafSubcommands[path[0]][w])) ||
			(len(path) == 2 && (commandGroups3[path[0]+" "+path[1]] || path[0] == "assistant" || leafSubcommands3[path[0]+" "+path[1]][w]))
		if isName && isCommandWord(w) && len(call.Args) == 0 {
			path = append(path, w)
			continue
		}
		call.Args = append(call.Args, w)
	}
	call.Path = strings.Join(path, " ")
	return call, call.Path != ""
}

func isCommandWord(w string) bool {
	if w == "" || len(w) > 24 {
		return false
	}
	for _, r := range w {
		if (r < 'a' || r > 'z') && r != '-' {
			return false
		}
	}
	return true
}
