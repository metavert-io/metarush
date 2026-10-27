// Package metarush carries MetaRush's renderer package inside every binary:
// the Remotion project (video/), the generated schema types and template
// catalog it imports (schema/), and the npm workspace manifests and lockfile
// that pin its dependencies. internal/renderer unpacks it into
// <home>/renderer/<version>-<digest>/ and installs exactly the locked packages
// there, so a binary always renders with the renderer of its own version (D75).
//
// Only sources are listed: node_modules, caches and self-test output are never
// embedded. Files whose names start with "." or "_" are skipped by go:embed;
// internal/renderer's tests fail if a tracked renderer file would be dropped.
package metarush

import "embed"

// RendererFiles is the renderer package (sources and lockfile, no node_modules).
//
//go:embed package.json package-lock.json web/package.json
//go:embed schema/package.json schema/ts schema/*.json
//go:embed video/package.json video/tsconfig.json video/README.md video/src video/render video/scripts
var RendererFiles embed.FS
