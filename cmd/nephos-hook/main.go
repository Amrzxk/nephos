// Command nephos-hook is the Podman OCI hook that wires an instance's network
// interface before its first process runs (ADR-0004, ARCHITECTURE §3.3).
//
// It is registered at the createRuntime stage and applied only to containers
// labeled io.nephos.instance-id. It reads the OCI container state from stdin,
// asks nephosd to plumb the interfaces over /run/nephos/hook.sock, and exits
// non-zero if that fails, so an instance never boots without its network.
//
// The hook contains no logic of its own. That is deliberate: every decision
// belongs to the network engine, which is the only component that can be held
// to the honesty rule in ADR-0005.
//
// M0 scope: the binary exists and reports its version so packaging and
// cross-builds are exercised. The socket call is prototyped in spike SP2 and
// implemented in M1.
package main

import (
	"fmt"
	"os"

	"github.com/Amrzxk/nephos/internal/version"
)

// socketPath is where nephosd listens for hook calls. It lives on tmpfs and is
// recreated at every appliance start (ARCHITECTURE §4).
const socketPath = "/run/nephos/hook.sock"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Fprintln(os.Stdout, version.Get())
		return
	}

	// Failing closed is the whole point of this binary: if it cannot wire the
	// instance, the instance must not start.
	fmt.Fprintf(os.Stderr, "nephos-hook: not implemented until M1; would call %s\n", socketPath)
	os.Exit(1)
}
