// Package version provides build-time version information.
// These variables are set via ldflags by goreleaser.
package version

// version, commit, and buildTime are set at link time via -ldflags -X.
// They are unexported to prevent accidental mutation; use the accessor
// functions below to read them.
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

// Version returns the semantic version (set by goreleaser).
func Version() string { return version }

// Commit returns the git commit hash (set by goreleaser).
func Commit() string { return commit }

// BuildTime returns the build timestamp in RFC3339 format (set by goreleaser).
func BuildTime() string { return buildTime }
