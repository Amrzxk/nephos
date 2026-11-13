package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/Amrzxk/nephos/internal/appliance"
)

// stdinIsTerminal is replaced in tests.
var stdinIsTerminal = func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// limitFlags are the appliance resource flags shared by up and reset --hard.
type limitFlags struct {
	fs     *flag.FlagSet
	memory *string
	cpus   *float64
	pids   *int64
}

func addLimitFlags(fs *flag.FlagSet) *limitFlags {
	return &limitFlags{fs: fs,
		memory: fs.String("memory", "4g", "appliance memory limit"),
		cpus:   fs.Float64("cpus", 2, "appliance CPU limit"),
		pids:   fs.Int64("pids-limit", 4096, "appliance PID limit"),
	}
}

func (l *limitFlags) limits() (appliance.Limits, error) {
	bytes, err := parseMemory(*l.memory)
	if err != nil || math.IsNaN(*l.cpus) || math.IsInf(*l.cpus, 0) || *l.cpus <= 0 || *l.cpus > 128 || *l.pids <= 0 {
		return appliance.Limits{}, fmt.Errorf("invalid resource limits")
	}
	limits := appliance.Limits{MemoryBytes: bytes, NanoCPUs: int64(*l.cpus * 1e9), PIDs: *l.pids}
	l.fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "memory":
			limits.Explicit.Memory = true
		case "cpus":
			limits.Explicit.CPUs = true
		case "pids-limit":
			limits.Explicit.PIDs = true
		}
	})
	return limits, nil
}

// confirmDestructive asks on an interactive terminal and otherwise requires
// --yes, because a purge deletes every instance's data and cannot be undone.
// It returns an exit code when the caller must stop.
func confirmDestructive(command string, yes bool, stdin, stderr *os.File) (int, bool) {
	if yes {
		return exitOK, true
	}
	if !stdinIsTerminal(stdin) {
		fmt.Fprintf(stderr, "nephos %s: this deletes the appliance and all instance data; rerun with --yes to confirm\n", command)
		return exitUsage, false
	}
	fmt.Fprintf(stderr, "This deletes the Nephos appliance container and the %q volume:\nevery instance, its files, and the API token. It cannot be undone.\nType yes to continue: ", "nephos-data")
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	if strings.TrimSpace(line) != "yes" {
		fmt.Fprintf(stderr, "nephos %s: aborted\n", command)
		return 1, false
	}
	return exitOK, true
}

func describePurge(result appliance.PurgeResult) string {
	switch {
	case result.RemovedContainer && result.RemovedVolume:
		return "Removed the Nephos appliance container and its data volume"
	case result.RemovedContainer:
		return "Removed the Nephos appliance container (no data volume was present)"
	case result.RemovedVolume:
		return "Removed the Nephos data volume (no appliance container was present)"
	default:
		return "No Nephos appliance container or data volume was present"
	}
}

func runPurge(ctx context.Context, yes bool, stdin, stdout, stderr *os.File) int {
	if code, ok := confirmDestructive("down --purge", yes, stdin, stderr); !ok {
		return code
	}
	m, err := newManager()
	if err != nil {
		fmt.Fprintf(stderr, "nephos down: %v\n", err)
		return 1
	}
	result, err := m.Purge(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "nephos down --purge: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, describePurge(result))
	return exitOK
}

func runHardReset(ctx context.Context, limits appliance.Limits, yes bool, stdin, stdout, stderr *os.File) int {
	if code, ok := confirmDestructive("reset --hard", yes, stdin, stderr); !ok {
		return code
	}
	m, err := newManager()
	if err != nil {
		fmt.Fprintf(stderr, "nephos reset --hard: %v\n", err)
		return 1
	}
	result, err := m.HardReset(ctx, limits)
	if err != nil {
		fmt.Fprintf(stderr, "nephos reset --hard: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, describePurge(result))
	fmt.Fprintln(stdout, "Nephos appliance ready at http://127.0.0.1:7788")
	return exitOK
}
