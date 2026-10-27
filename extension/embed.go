// Package extension holds the "Send to MetaRush" browser extension (Manifest V3,
// Chromium first; see docs/browser-extension.md). It is embedded in the binary
// so `metarush extension path` can keep an up-to-date copy in the installation
// home for the browser's "Load unpacked".
package extension

import (
	"embed"
	"io/fs"
)

//go:embed manifest.json background.js content.js popup.html popup.css popup.js lib/detect.js lib/page.js lib/address.js icons/*.png
var files embed.FS

// FS is the extension's files (the folder a browser loads).
func FS() fs.FS { return files }
