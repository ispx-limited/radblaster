// Package web holds the interface served by the serve command.
//
// It is a single self-contained page with no build step and nothing to fetch
// at runtime, so building radblaster needs only Go and the interface works on
// a machine with no internet access. The API is the contract between the two,
// which leaves the door open to a richer front end later without the engine
// caring.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html
var files embed.FS

// FS returns the interface's files.
func FS() fs.FS { return files }
