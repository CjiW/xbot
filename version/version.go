package version

import "fmt"

// Version, Commit, BuildTime are injected via -ldflags at build time.
//
//	go build -ldflags "-X xbot/version.Version=v1.0.0 -X xbot/version.Commit=$(git rev-parse --short HEAD) -X xbot/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Info returns a formatted version string.
func Info() string {
	return fmt.Sprintf("xbot %s (commit: %s, built: %s)", Version, Commit, BuildTime)
}
