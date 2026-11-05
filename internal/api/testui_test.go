package api

import (
	"io/fs"
	"testing/fstest"
)

// testUI is a minimal file system standing in for the real interface.
func newTestUI() fs.FS {
	return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}}
}

type testUI struct{ fs.FS }

func (testUI) Open(name string) (fs.File, error) { return newTestUI().Open(name) }
