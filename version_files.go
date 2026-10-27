package metarush

import _ "embed"

// VersionFile is VERSION: the release this source is, or follows (D130). It is
// the one place the version number is written; scripts/version.sh adds how far
// past that release a build is, and internal/version falls back to it (plus
// "-dev") in a build without those stamps (go build, go run, go test).
//
//go:embed VERSION
var VersionFile string

// Changelog is CHANGELOG.md as it was when this binary was built: Settings →
// About and `metarush version --details` show the part about this version (or,
// in a build past a release, the changes not released yet).
//
//go:embed CHANGELOG.md
var Changelog string
