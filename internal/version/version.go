package version

import "time"

// These variables are stamped at build time via -ldflags by the Makefile and
// the GHA release workflow. The defaults below are used only for local dev
// builds that bypass the Makefile (e.g. a plain `go run ./cmd/gg`).
var (
	// Version is the semver release tag, e.g. v1.2.3.
	// Set via: -X 'gopher-glide/internal/version.Version=v1.2.3'
	Version = "v0.6.0-dev"

	// GitCommit is the short SHA of the commit the binary was built from.
	// Set via: -X 'gopher-glide/internal/version.GitCommit=abc1234'
	GitCommit = "none"

	// BuildDate is the UTC timestamp of the build in RFC3339 format.
	// Set via: -X 'gopher-glide/internal/version.BuildDate=2026-02-28T19:00:00Z'
	// Falls back to the process start time when not stamped.
	BuildDate = ""
)

// GetBuildDate buildDate returns the stamped value or the current UTC time as a fallback.
func GetBuildDate() string {
	if BuildDate != "" {
		return BuildDate
	}
	return time.Now().UTC().Format(time.RFC3339)
}
