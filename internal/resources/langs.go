package resources

import (
	"bytes"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// What a repository's files are: their language (for the overview and code
// slides), whether they are binaries (left out of a snapshot) and the
// licence a LICENSE file states.

// langByExt maps a file extension to a language name and the Shiki grammar
// that highlights it.
var langByExt = map[string][2]string{
	".go": {"Go", "go"}, ".ts": {"TypeScript", "typescript"}, ".tsx": {"TypeScript", "tsx"}, ".js": {"JavaScript", "javascript"},
	".jsx": {"JavaScript", "jsx"}, ".mjs": {"JavaScript", "javascript"}, ".cjs": {"JavaScript", "javascript"}, ".py": {"Python", "python"},
	".rb": {"Ruby", "ruby"}, ".rs": {"Rust", "rust"}, ".java": {"Java", "java"}, ".kt": {"Kotlin", "kotlin"}, ".kts": {"Kotlin", "kotlin"},
	".swift": {"Swift", "swift"}, ".c": {"C", "c"}, ".h": {"C", "c"}, ".cc": {"C++", "cpp"}, ".cpp": {"C++", "cpp"}, ".cxx": {"C++", "cpp"},
	".hpp": {"C++", "cpp"}, ".hh": {"C++", "cpp"}, ".m": {"Objective-C", "objective-c"}, ".mm": {"Objective-C++", "objective-cpp"},
	".cs": {"C#", "csharp"}, ".fs": {"F#", "fsharp"}, ".php": {"PHP", "php"}, ".scala": {"Scala", "scala"}, ".clj": {"Clojure", "clojure"},
	".ex": {"Elixir", "elixir"}, ".exs": {"Elixir", "elixir"}, ".erl": {"Erlang", "erlang"}, ".hs": {"Haskell", "haskell"}, ".ml": {"OCaml", "ocaml"},
	".lua": {"Lua", "lua"}, ".r": {"R", "r"}, ".jl": {"Julia", "julia"}, ".dart": {"Dart", "dart"}, ".zig": {"Zig", "zig"}, ".nim": {"Nim", "nim"},
	".sh": {"Shell", "bash"}, ".bash": {"Shell", "bash"}, ".zsh": {"Shell", "bash"}, ".fish": {"Shell", "fish"}, ".ps1": {"PowerShell", "powershell"},
	".sql": {"SQL", "sql"}, ".html": {"HTML", "html"}, ".htm": {"HTML", "html"}, ".css": {"CSS", "css"}, ".scss": {"SCSS", "scss"},
	".sass": {"Sass", "sass"}, ".less": {"Less", "less"}, ".vue": {"Vue", "vue"}, ".svelte": {"Svelte", "svelte"}, ".astro": {"Astro", "astro"},
	".json": {"JSON", "json"}, ".jsonc": {"JSON", "jsonc"}, ".yaml": {"YAML", "yaml"}, ".yml": {"YAML", "yaml"}, ".toml": {"TOML", "toml"},
	".xml": {"XML", "xml"}, ".md": {"Markdown", "markdown"}, ".mdx": {"MDX", "mdx"}, ".proto": {"Protocol Buffers", "proto"},
	".graphql": {"GraphQL", "graphql"}, ".gql": {"GraphQL", "graphql"}, ".tf": {"HCL", "hcl"}, ".hcl": {"HCL", "hcl"}, ".nix": {"Nix", "nix"},
	".sol": {"Solidity", "solidity"}, ".wgsl": {"WGSL", "wgsl"}, ".glsl": {"GLSL", "glsl"}, ".vert": {"GLSL", "glsl"}, ".frag": {"GLSL", "glsl"},
	".cmake": {"CMake", "cmake"}, ".gradle": {"Groovy", "groovy"}, ".groovy": {"Groovy", "groovy"}, ".pl": {"Perl", "perl"}, ".pm": {"Perl", "perl"},
	".vb": {"Visual Basic", "vb"}, ".asm": {"Assembly", "asm"}, ".s": {"Assembly", "asm"}, ".tex": {"TeX", "latex"}, ".rst": {"reStructuredText", "rst"},
	".mmd": {"Mermaid", "mermaid"}, ".d2": {"D2", "d2"}, ".ini": {"INI", "ini"}, ".bat": {"Batchfile", "bat"}, ".cu": {"CUDA", "cpp"},
}

var langByName = map[string][2]string{
	"dockerfile": {"Dockerfile", "docker"}, "makefile": {"Makefile", "make"}, "gnumakefile": {"Makefile", "make"}, "rakefile": {"Ruby", "ruby"},
	"gemfile": {"Ruby", "ruby"}, "justfile": {"Just", "just"}, "cmakelists.txt": {"CMake", "cmake"}, "go.mod": {"Go module", "go"},
	"vagrantfile": {"Ruby", "ruby"}, "jenkinsfile": {"Groovy", "groovy"},
}

// LanguageOf names a file's language and the grammar that highlights it
// ("" when it isn't code MetaRush knows).
func LanguageOf(p string) (name, grammar string) {
	base := strings.ToLower(path.Base(p))
	if l, ok := langByName[base]; ok {
		return l[0], l[1]
	}
	if strings.HasPrefix(base, "dockerfile") {
		return "Dockerfile", "docker"
	}
	if l, ok := langByExt[strings.ToLower(path.Ext(base))]; ok {
		return l[0], l[1]
	}
	return "", ""
}

// proseOrData: files counted in a snapshot but not as a language's code in
// the overview (GitHub does the same).
var proseOrData = map[string]bool{"Markdown": true, "MDX": true, "JSON": true, "YAML": true, "TOML": true, "XML": true, "INI": true, "reStructuredText": true, "TeX": true,
	"Go module": true}

// binaryExt are extensions that are never text.
var binaryExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".ico": true, ".icns": true, ".bmp": true, ".tif": true, ".tiff": true,
	".heic": true, ".avif": true, ".psd": true, ".ai": true, ".sketch": true, ".fig": true, ".pdf": true, ".zip": true, ".gz": true, ".tgz": true,
	".bz2": true, ".xz": true, ".zst": true, ".7z": true, ".rar": true, ".tar": true, ".jar": true, ".war": true, ".class": true, ".dll": true,
	".so": true, ".dylib": true, ".a": true, ".o": true, ".obj": true, ".exe": true, ".bin": true, ".wasm": true, ".pyc": true, ".pyo": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true, ".mp3": true, ".mp4": true, ".mov": true, ".m4a": true, ".wav": true,
	".flac": true, ".ogg": true, ".webm": true, ".mkv": true, ".avi": true, ".db": true, ".sqlite": true, ".sqlite3": true, ".npy": true,
	".pt": true, ".onnx": true, ".pb": true, ".h5": true, ".parquet": true, ".dmg": true, ".iso": true, ".deb": true, ".rpm": true, ".apk": true,
	".ipa": true, ".keystore": true, ".jks": true, ".p12": true, ".pfx": true, ".blend": true, ".fbx": true, ".glb": true, ".usdz": true,
}

// isBinaryName reports a file that is a binary by its name.
func isBinaryName(p string) bool { return binaryExt[strings.ToLower(path.Ext(p))] }

// isBinaryData reports bytes that aren't text: a NUL in the first 8 KB, or
// mostly invalid UTF-8.
func isBinaryData(b []byte) bool {
	head := b
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return true
	}
	if utf8.Valid(head) {
		return false
	}
	bad := 0
	for len(head) > 0 {
		r, n := utf8.DecodeRune(head)
		if r == utf8.RuneError && n == 1 {
			bad++
		}
		head = head[n:]
	}
	return bad > 16
}

// isLFSPointer reports a Git LFS pointer file (the real file lives elsewhere).
func isLFSPointer(b []byte) bool {
	return len(b) < 300 && bytes.HasPrefix(b, []byte("version https://git-lfs.github.com/spec/"))
}

// generatedOrVendored: folders a code tour skips in the language counts
// (they stay in the snapshot when small enough).
var vendoredDir = regexp.MustCompile(`(^|/)(node_modules|vendor|third_party|bower_components|dist|build|\.yarn|Pods)/`)

// licensePatterns recognize the common licences in a LICENSE file's text.
var licensePatterns = []struct {
	re   *regexp.Regexp
	spdx string
}{
	{regexp.MustCompile(`(?i)apache license,?\s+version 2\.0`), "Apache-2.0"},
	{regexp.MustCompile(`(?i)mozilla public license,?\s+(version )?2\.0`), "MPL-2.0"},
	{regexp.MustCompile(`(?i)gnu affero general public license`), "AGPL-3.0"},
	{regexp.MustCompile(`(?i)gnu lesser general public license`), "LGPL"},
	{regexp.MustCompile(`(?i)gnu general public license[\s\S]{0,200}version 3`), "GPL-3.0"},
	{regexp.MustCompile(`(?i)gnu general public license[\s\S]{0,200}version 2`), "GPL-2.0"},
	{regexp.MustCompile(`(?i)permission is hereby granted, free of charge`), "MIT"},
	{regexp.MustCompile(`(?i)redistribution and use in source and binary forms[\s\S]{0,900}neither the name`), "BSD-3-Clause"},
	{regexp.MustCompile(`(?i)redistribution and use in source and binary forms`), "BSD-2-Clause"},
	{regexp.MustCompile(`(?i)this is free and unencumbered software released into the public domain`), "Unlicense"},
	{regexp.MustCompile(`(?i)permission to use, copy, modify, and/or distribute this software for any purpose with or without fee`), "ISC"},
	{regexp.MustCompile(`(?i)creative commons attribution 4\.0`), "CC-BY-4.0"},
}

// LicenseFromText names the licence a LICENSE file states ("" when unsure).
func LicenseFromText(text string) string {
	if len(text) > 20000 {
		text = text[:20000]
	}
	for _, p := range licensePatterns {
		if p.re.MatchString(text) {
			return p.spdx
		}
	}
	return ""
}

// isLicenseFile reports a top-level LICENSE / COPYING file.
func isLicenseFile(p string) bool {
	if strings.Contains(p, "/") {
		return false
	}
	b := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(p, path.Ext(p)), "-mit"))
	return b == "license" || b == "licence" || b == "copying" || b == "license-mit"
}

// isReadme reports a top-level README.
func isReadme(p string) bool {
	if strings.Contains(p, "/") {
		return false
	}
	b := strings.ToLower(p)
	return b == "readme" || strings.HasPrefix(b, "readme.")
}
