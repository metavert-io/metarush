package remotion

import (
	"path"
	"strings"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/secretpath"
)

// What a Remotion source's copy keeps and what it never takes (D131). The
// rules are the same for a folder, a GitHub repository and a checkout Claude
// edits, so a version made from a checkout never picks up what the import
// left out.

// Reasons, in the words people see.
const (
	whySecretName  = "holds secrets (keys, passwords or settings with them)"
	whySecretText  = "looks like it holds a secret (an API key or a private key)"
	whyAssistant   = "instructions or settings for coding assistants (they could ask Claude to run the project's own programs)"
	whyTooling     = "settings for other tools (they can run programs; not needed for the video)"
	whyPackages    = "installed packages (MetaRush installs the locked versions itself)"
	whyGit         = "git's own history"
	whyIgnored     = "the project's .gitignore leaves it out"
	whyCache       = "a build cache or a render (renders in out/ come in as footage)"
	whyLink        = "a link to another file or folder"
	whyName        = "an unusual file name"
	whyTooBig      = "larger than MetaRush keeps"
	whyOverLimit   = "past the size limit for one project"
	whyLFS         = "stored with Git LFS (not downloaded)"
	whySubmodule   = "another repository inside this one"
	whySystem      = "a file the computer makes by itself"
	whyMetaRush    = "MetaRush's own notes about the folder"
	whyOutput      = "the bundle or build output of the project"
	whyCheckoutGit = "the checkout's git history (MetaRush keeps the versions)"
)

// Size limits for one version.
const (
	maxCodeFileBytes   = 20 << 20  // one file outside public/
	maxCodeBytes       = 200 << 20 // everything outside public/
	maxPublicFileBytes = 2 << 30   // one file in public/
	maxPublicBytes     = 4 << 30   // everything in public/
	maxFiles           = 20000
)

// secretNames are file names that hold secrets (compared lowercased).
var secretNames = map[string]bool{
	".npmrc": true, ".yarnrc": true, ".yarnrc.yml": true, ".netrc": true, ".pypirc": true, ".git-credentials": true,
	"credentials.json": true, "service-account.json": true, "client_secret.json": true, "secrets.json": true, "secrets.yml": true, "secrets.yaml": true,
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true, ".htpasswd": true, "token.json": true, "auth.json": true,
}

// secretExts are file extensions that hold keys and certificates.
var secretExts = map[string]bool{".pem": true, ".key": true, ".p8": true, ".p12": true, ".pfx": true, ".keystore": true, ".jks": true, ".asc": true, ".gpg": true, ".ovpn": true, ".kdbx": true}

// assistantFiles and assistantDirs are instructions and settings for coding
// assistants: a checkout Claude edits must not carry another project's.
var assistantFiles = map[string]bool{"claude.md": true, "claude.local.md": true, "agents.md": true, "agent.md": true, ".cursorrules": true, ".windsurfrules": true,
	".clinerules": true, ".mcp.json": true, "gemini.md": true, "copilot-instructions.md": true, ".aider.conf.yml": true, "vibectl.md": true}
var assistantDirs = map[string]bool{".claude": true, ".cursor": true, ".windsurf": true, ".clinerules": true, ".aider": true, ".continue": true, ".roo": true, ".gemini": true}

// toolingDirs are other tools' settings that can run programs.
var toolingDirs = map[string]bool{".github": true, ".gitlab": true, ".husky": true, ".vscode": true, ".idea": true, ".devcontainer": true, ".circleci": true, ".buildkite": true}

// cacheDirs are build caches and outputs a version never needs.
var cacheDirs = map[string]bool{".cache": true, ".remotion": true, ".turbo": true, ".next": true, ".parcel-cache": true, ".vercel": true, ".netlify": true,
	"coverage": true, ".nyc_output": true, ".bundle-cache": true, ".sources": true}

// systemFiles are files operating systems make by themselves.
var systemFiles = map[string]bool{".ds_store": true, "thumbs.db": true, "desktop.ini": true, ".localized": true}

// skipDir says why a folder is never copied ("" = look inside). rel is the
// folder's slash path from the project's root.
func skipDir(rel string) string {
	name := strings.ToLower(path.Base(rel))
	top := !strings.Contains(rel, "/")
	switch {
	case name == "node_modules":
		return whyPackages
	case name == ".git":
		return whyGit
	case assistantDirs[name]:
		return whyAssistant
	case toolingDirs[name]:
		return whyTooling
	case cacheDirs[name]:
		return whyCache
	case top && name == "out":
		return whyCache
	case name == ".metarush":
		return whyMetaRush
	case strings.HasPrefix(name, ".env"):
		return whySecretName
	case name == ".ssh" || name == ".aws" || name == ".gnupg" || name == ".docker" || name == ".kube":
		return whySecretName
	}
	return ""
}

// skipFile says why a file is never copied by its name ("" = keep it, subject
// to its contents).
func skipFile(rel string) string {
	name := strings.ToLower(path.Base(rel))
	ext := path.Ext(name)
	switch {
	case !safeRel(rel):
		return whyName
	case name == ".env" || strings.HasPrefix(name, ".env.") || strings.HasSuffix(name, ".env"):
		return whySecretName
	case secretNames[name] || secretExts[ext]:
		return whySecretName
	case strings.Contains(name, "secret") && (ext == ".json" || ext == ".yml" || ext == ".yaml" || ext == ".txt" || ext == ".toml" || ext == ""):
		return whySecretName
	case strings.HasPrefix(name, "id_rsa") || strings.HasPrefix(name, "id_ed25519"):
		return whySecretName
	case assistantFiles[name]:
		return whyAssistant
	case systemFiles[name] || strings.HasPrefix(name, "._"):
		return whySystem
	case name == "remotion.json" && strings.HasPrefix(rel, ".metarush/"):
		return whyMetaRush
	}
	return ""
}

// safeRel refuses names a copy couldn't hold safely.
func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.ContainsRune(p, 0) || len(p) > 1024 || !utf8.ValidString(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return false
			}
		}
	}
	return path.Clean(p) == p
}

// isPublic reports a file in the project's public/ folder (staticFile()).
func isPublic(rel string) bool { return strings.HasPrefix(rel, "public/") }

// textExts are files read as text for secret shapes (everything else that
// isn't public/ media is checked when it is valid UTF-8).
var mediaExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".avif": true, ".bmp": true, ".tif": true, ".tiff": true,
	".heic": true, ".ico": true, ".mp3": true, ".wav": true, ".aac": true, ".m4a": true, ".ogg": true, ".flac": true, ".opus": true, ".mp4": true, ".mov": true,
	".webm": true, ".mkv": true, ".m4v": true, ".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".glb": true, ".gltf": true, ".lottie": true, ".riv": true}

func isMedia(rel string) bool { return mediaExts[strings.ToLower(path.Ext(rel))] }

// secretIn says what secret a file's bytes look like they hold ("" = none).
// Media files are not read (secretpath.SecretShape skips other binary data).
func secretIn(rel string, data []byte) string {
	if isMedia(rel) {
		return ""
	}
	return secretpath.SecretShape(data)
}

// leftOut groups what a copy left out by reason, with a few examples each.
type leftOut struct {
	order []string
	n     map[string]int
	ex    map[string][]string
}

func (l *leftOut) add(rel, why string) {
	if l.n == nil {
		l.n, l.ex = map[string]int{}, map[string][]string{}
	}
	if l.n[why] == 0 {
		l.order = append(l.order, why)
	}
	l.n[why]++
	if len(l.ex[why]) < 5 {
		l.ex[why] = append(l.ex[why], rel)
	}
}
