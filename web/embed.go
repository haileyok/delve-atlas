// Package web embeds the frontend.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var content embed.FS

// Static is the frontend's files, rooted at the site root.
func Static() fs.FS {
	sub, err := fs.Sub(content, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
