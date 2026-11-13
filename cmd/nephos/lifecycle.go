package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Amrzxk/nephos/internal/appliance"
)

func newManager() (appliance.Manager, error) {
	engine, err := appliance.NewDockerEngine()
	if err != nil {
		return appliance.Manager{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return appliance.Manager{}, fmt.Errorf("find home directory: %w", err)
	}
	return appliance.Manager{
		Engine:      engine,
		Health:      appliance.HTTPHealthProbe{},
		Credentials: appliance.LocalCredentialStore{Path: filepath.Join(home, ".nephos", "credentials")},
	}, nil
}

func runUp(ctx context.Context, args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limitFlags := addLimitFlags(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "nephos up: unexpected positional arguments")
		return exitUsage
	}
	limits, err := limitFlags.limits()
	if err != nil {
		fmt.Fprintf(stderr, "nephos up: %v\n", err)
		return exitUsage
	}
	m, err := newManager()
	if err != nil {
		fmt.Fprintf(stderr, "nephos up: %v\n", err)
		return 1
	}
	if err := m.Up(ctx, limits); err != nil {
		fmt.Fprintf(stderr, "nephos up: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Nephos appliance ready at http://127.0.0.1:7788")
	return exitOK
}

// runDown stops the appliance; --purge also deletes it and its data volume.
func runDown(ctx context.Context, args []string, stdin, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	fs.SetOutput(stderr)
	purge := fs.Bool("purge", false, "also remove the appliance container and its data volume")
	yes := fs.Bool("yes", false, "with --purge: do not ask for confirmation")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "nephos down: unexpected arguments")
		return exitUsage
	}
	if *yes && !*purge {
		fmt.Fprintln(stderr, "nephos down: --yes is only valid with --purge")
		return exitUsage
	}
	if *purge {
		return runPurge(ctx, *yes, stdin, stdout, stderr)
	}
	m, err := newManager()
	if err != nil {
		fmt.Fprintf(stderr, "nephos down: %v\n", err)
		return 1
	}
	if err := m.Down(ctx); err != nil {
		fmt.Fprintf(stderr, "nephos down: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Nephos appliance stopped; data volume preserved")
	return exitOK
}

func runStatus(ctx context.Context, args []string, stdout, stderr *os.File) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "nephos status: unexpected arguments")
		return exitUsage
	}
	m, err := newManager()
	if err != nil {
		fmt.Fprintf(stderr, "nephos status: %v\n", err)
		return 1
	}
	status, err := m.Status(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "nephos status: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, status)
	return exitOK
}

func parseMemory(value string) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(value))
	multiplier := int64(1)
	for _, suffix := range []struct {
		suffix string
		bytes  int64
	}{
		{"gib", 1 << 30}, {"gb", 1 << 30}, {"g", 1 << 30},
		{"mib", 1 << 20}, {"mb", 1 << 20}, {"m", 1 << 20},
	} {
		if strings.HasSuffix(s, suffix.suffix) {
			s = strings.TrimSuffix(s, suffix.suffix)
			multiplier = suffix.bytes
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("invalid memory limit %q", value)
	}
	return n * multiplier, nil
}
