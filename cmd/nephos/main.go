// Command nephos is the Nephos CLI. It runs on the learner's machine and talks
// to nephosd inside the appliance over the REST API.
//
// M0 scope: this binary exists so the module, the linker stamping, and the
// cross-compilation matrix are real and exercised by CI. Resource commands
// arrive in M1 (see docs/ROADMAP.md).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Amrzxk/nephos/internal/version"
)

// Exit codes are part of the CLI contract (ARCHITECTURE §6): 0 success,
// 1 error, 2 usage error, 3 a check did not match its expectation.
const (
	exitOK    = 0
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("nephos", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the version as JSON")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "nephos %s\n\nUsage:\n  nephos version [--json]\n\n", version.Get().Version)
		fmt.Fprintf(stderr, "Resource commands arrive in M1; see docs/ROADMAP.md.\n")
	}

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	switch cmd := fs.Arg(0); cmd {
	case "", "version":
		return printVersion(stdout, stderr, *asJSON)
	default:
		fmt.Fprintf(stderr, "nephos: unknown command %q\n", cmd)
		fs.Usage()
		return exitUsage
	}
}

func printVersion(stdout, stderr *os.File, asJSON bool) int {
	info := version.Get()
	if !asJSON {
		fmt.Fprintln(stdout, info)
		return exitOK
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(info); err != nil {
		fmt.Fprintf(stderr, "nephos: encoding version: %v\n", err)
		return 1
	}
	return exitOK
}
