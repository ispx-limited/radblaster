package radius

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIncludedFileIsReadOnce is what keeps a dictionary set from costing
// exponential work. Without it, a handful of small files that include each
// other a few times over expands into billions of parses.
func TestIncludedFileIsReadOnce(t *testing.T) {
	dir := t.TempDir()

	// A tree four wide and eight deep: 4^7 leaf visits if each include is
	// re-read, one visit per file if they are not.
	const depth, fanout = 8, 4
	for lvl := range depth {
		var b strings.Builder
		if lvl == depth-1 {
			b.WriteString("ATTRIBUTE Leaf-Attr 200 string\n")
		} else {
			for range fanout {
				fmt.Fprintf(&b, "$INCLUDE d%d\n", lvl+1)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("d%d", lvl)), []byte(b.String()), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
	}

	done := make(chan error, 1)
	go func() {
		_, err := LoadDictionary(filepath.Join(dir, "d0"))
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("LoadDictionary: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("loading did not finish: an included file is being re-read")
	}
}

// A cycle must still terminate rather than recursing until the stack runs out.
func TestIncludeCycleTerminates(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []struct{ name, body string }{
		{"a", "$INCLUDE b\n"},
		{"b", "$INCLUDE a\nATTRIBUTE Cycle-Attr 201 string\n"},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
	}
	if _, err := LoadDictionary(filepath.Join(dir, "a")); err != nil {
		t.Fatalf("a cycle should be absorbed, not fail: %v", err)
	}
}
