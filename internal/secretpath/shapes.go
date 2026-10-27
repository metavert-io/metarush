package secretpath

import (
	"bytes"
	"regexp"
)

// secretShapes are credential shapes that never appear in a video project or
// an ability's folder by accident; a file holding one is left out or refused
// (narrower than the activity log's redaction, which also hides "token: …"
// assignments that code is full of). Remotion imports (D131) and abilities
// (D230) share them.
var secretShapes = []struct {
	re   *regexp.Regexp
	what string
}{
	// The whole block (its newlines real or written as \n in JSON); a block
	// cut short, to the end of the text.
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`), "a private key"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA|A3T[A-Z0-9])[A-Z0-9]{16}\b`), "an AWS key"},
	{regexp.MustCompile(`\bt(?:id|sec)_[A-Za-z0-9_+/=-]{16,}`), "a storage key"},
	{regexp.MustCompile(`\bsk_(?:live_|test_)?[A-Za-z0-9]{24,}\b`), "an API key"},
	{regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{24,}`), "an API key"},
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})`), "a GitHub token"},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), "a Slack token"},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), "a Google API key"},
	{regexp.MustCompile(`(?i)(mongodb(?:\+srv)?://[^:/?#@\s"'<>]+:)[^@/\s"'<>]+@`), "a database password"},
	{regexp.MustCompile(`(?i)\bxi-api-key["']?\s*[:=]\s*["']?[A-Za-z0-9_]{24,}`), "an API key"},
}

// SecretShape says what credential a text's bytes look like they hold ("" =
// none). Binary data (a NUL byte) is not read; at most 8 MB is.
func SecretShape(data []byte) string {
	if bytes.IndexByte(data, 0) >= 0 {
		return ""
	}
	if len(data) > 8<<20 {
		data = data[:8<<20]
	}
	for _, s := range secretShapes {
		if s.re.Match(data) {
			return s.what
		}
	}
	return ""
}

// HideShapes replaces every credential shape in a text with mask — a
// database address keeps its scheme and user, only its password masked —
// and says which kinds it found (none: the text as it was).
func HideShapes(text, mask string) (string, []string) {
	var kinds []string
	for _, s := range secretShapes {
		if !s.re.MatchString(text) {
			continue
		}
		kinds = append(kinds, s.what)
		if s.re.NumSubexp() > 0 {
			text = s.re.ReplaceAllString(text, "${1}"+mask+"@")
		} else {
			text = s.re.ReplaceAllLiteralString(text, mask)
		}
	}
	return text, kinds
}
