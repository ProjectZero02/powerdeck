// Package version holds build metadata, set at link time:
//
//	go build -ldflags "-X github.com/projectzero02/powerdeck/windows/internal/version.Version=1.2.3"
package version

import "strings"

// Version is the release version without a leading "v", or "dev" for local builds.
var Version = "dev"

// Repo is the GitHub repository that publishes releases.
const Repo = "ProjectZero02/powerdeck"

// APIVersion is the phone ↔ PC protocol version. Bump it on incompatible changes.
const APIVersion = 1

// IsRelease reports whether this is a tagged release build ("1.2.3"); dev and CI builds ("dev",
// "0.0.0-ci") never auto-update.
func IsRelease() bool { return Version != "dev" && Version != "" && !strings.Contains(Version, "-") }
