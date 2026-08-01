// Package web holds the interface served by the serve command.
//
// The built assets are committed so that building radblaster needs only Go.
// Rebuild them with "make ui" after changing anything under ui/.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var files embed.FS

// FS returns the interface's built assets, rooted so index.html is at the top.
func FS() fs.FS {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		// The directory is embedded at build time, so this cannot happen
		// unless the embed directive itself is wrong.
		panic(err)
	}
	return sub
}
