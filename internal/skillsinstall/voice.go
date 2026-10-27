package skillsinstall

import (
	"regexp"

	"github.com/metavert-io/metarush/internal/plainwords"
)

// commandTalkRe finds a reply that talks about running commands instead of
// about the change ("approve the command", "the command needs your
// permission"): the person should be asked about what happens to the video.
var commandTalkRe = regexp.MustCompile(`(?i)\b(approve|allow|permit|permission)\b[^.!?\n]{0,40}\bcommands?\b|\bcommands?\b[^.!?\n]{0,40}\b(permission|approv\w*|allowed|blocked|denied)\b`)

// VoiceGuide is the shared guide (skills/_shared/voice.md, installed as each
// skill's reference/voice.md) for how Claude talks to the person.
const VoiceGuide = "reference/voice.md"

// voiceLabels name each kind of finding in a judge's reason.
var voiceLabels = map[string]string{
	plainwords.KindID:      "internal ID",
	plainwords.KindFlag:    "command flag",
	plainwords.KindCommand: "command",
	plainwords.KindPath:    "file path",
	plainwords.KindJSON:    "JSON",
	plainwords.KindCode:    "internal code",
	plainwords.KindJargon:  "jargon",
	plainwords.KindLeak:    "missing value",
}

// VoiceIssues lists what in a reply the person should not have to read:
// internal IDs, CLI flags or commands, file paths, JSON, internal codes and
// unexplained jargon — the same rules (internal/plainwords) the plain CLI
// output and the dashboard copy are held to. It checks text meant for people
// (final answers), never machine output. Each issue reads "<kind> <text>"
// ("jargon LUFS", "internal ID rev_…"); JSON is just "JSON".
func VoiceIssues(text string) []string {
	var out []string
	if m := commandTalkRe.FindString(text); m != "" {
		out = append(out, "talks about commands: "+m)
	}
	for _, f := range plainwords.CheckShown(text) {
		if f.Kind == plainwords.KindJSON {
			out = append(out, "JSON")
			continue
		}
		out = append(out, voiceLabels[f.Kind]+" "+f.Text)
	}
	return out
}
