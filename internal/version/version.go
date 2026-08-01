// Package version carries build metadata stamped in at link time.
package version

// Values are overridden with -ldflags -X at build time. The defaults describe
// an unstamped local build.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns the full build identity.
func String() string {
	return Version + " (" + Commit + ", built " + Date + ")"
}
