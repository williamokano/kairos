// Package version reports the build identity, set at link time via -ldflags
// (`-X github.com/williamokano/kairos/internal/version.Version=…`). Defaults
// are for `go run`/`go test`.
package version

import "fmt"

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String renders the build identity for `kairos version` and log lines.
func String() string {
	return fmt.Sprintf("kairos %s (%s, built %s)", Version, Commit, Date)
}
