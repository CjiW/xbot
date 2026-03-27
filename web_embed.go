package main

import (
	"embed"
	"io/fs"
)

//go:embed all:web/dist
var webDistFS embed.FS

// WebStaticFS returns the embedded web frontend files.
func WebStaticFS() fs.FS {
	sub, _ := fs.Sub(webDistFS, "web/dist")
	return sub
}
