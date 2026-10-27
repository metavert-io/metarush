// Package visionsrc holds the Swift source of metarush-vision, the on-device
// helper that looks at video frames with Apple Vision (macOS). The metarush
// binary embeds it so any Mac with the Xcode Command Line Tools can build the
// helper that matches it (internal/vision; ./bootstrap.sh and `metarush doctor`
// build it too).
package visionsrc

import _ "embed"

// Source is main.swift.
//
//go:embed main.swift
var Source []byte
