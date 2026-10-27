// Package skills embeds MetaRush's first-party Claude Code skills so the binary
// can install them (`metarush skills install`) without the repository.
package skills

import (
	"embed"
	"io/fs"
)

//go:embed all:_shared all:video-project all:video-ingest all:video-footage-review all:video-story-editor all:video-edit-revise all:video-render
//go:embed all:video-qa all:video-deliver all:video-motion-design all:video-workspace-admin all:video-voiceover
//go:embed all:video-generate all:video-remotion all:video-recipe all:metarush-improve
var files embed.FS

// FS returns the embedded skills tree.
func FS() fs.FS { return files }

// Names lists the skills shipped in this build.
var Names = []string{"video-project", "video-ingest", "video-footage-review", "video-story-editor", "video-edit-revise", "video-render",
	"video-qa", "video-deliver", "video-motion-design", "video-workspace-admin", "video-voiceover", "video-generate", "video-remotion", "video-recipe", "metarush-improve"}
