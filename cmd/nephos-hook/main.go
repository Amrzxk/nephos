// Command nephos-hook wires an instance through the appliance-private hook
// socket before its first process runs. Any failure exits nonzero.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Amrzxk/nephos/internal/hook"
	"github.com/Amrzxk/nephos/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Fprintln(os.Stdout, version.Get())
		return
	}
	if err := runHook(); err != nil {
		fmt.Fprintf(os.Stderr, "nephos-hook: %v\n", err)
		os.Exit(1)
	}
}
func runHook() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return run(ctx, os.Stdin, hook.SocketPath)
}
func run(ctx context.Context, stdin io.Reader, socket string) error {
	request, err := hook.DecodeState(stdin)
	if err != nil {
		return err
	}
	return hook.Plumb(ctx, socket, request)
}
