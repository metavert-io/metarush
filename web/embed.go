// Package web embeds the built dashboard (web/dist) into the Go binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built dashboard files.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Built reports whether a real build (index.html) is embedded.
func Built() bool {
	_, err := fs.Stat(Dist(), "index.html")
	return err == nil
}
