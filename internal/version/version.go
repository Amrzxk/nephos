// Package version reports the build identity of the Nephos binaries.
//
// The values are injected at link time by the Makefile. A build that does not
// set them reports the placeholders below, which is how a `go build ./...`
// during development is distinguished from a release artifact.
package version

import (
	"fmt"
	"runtime"
	"strings"
)

// Injected with -ldflags -X. Keep these as plain strings: the linker cannot set
// anything else.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// Info describes one build.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Get returns the build identity of the running binary.
func Get() Info {
	return Info{
		Version:   version,
		Commit:    commit,
		Date:      date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// String renders the build identity as a single human-readable line.
func (i Info) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", i.Version)
	if i.Commit != "" && i.Commit != "unknown" {
		fmt.Fprintf(&b, " (%s)", shortCommit(i.Commit))
	}
	fmt.Fprintf(&b, " %s %s", i.GoVersion, i.Platform)
	return b.String()
}

// IsRelease reports whether this binary was stamped by the release tooling
// rather than built ad hoc. Callers use it to decide whether to trust the
// version string, for example before reporting it in a bug report.
func (i Info) IsRelease() bool {
	return i.Version != "dev" && i.Version != "" && i.Commit != "unknown"
}

// shortCommit abbreviates a git SHA to the conventional 7 characters, leaving
// anything shorter (or any non-SHA marker) untouched.
func shortCommit(c string) string {
	const short = 7
	if len(c) <= short {
		return c
	}
	return c[:short]
}
