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

func runUp(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	memory := fs.String("memory", "4g", "appliance memory limit")
	cpus := fs.Float64("cpus", 2, "appliance CPU limit")
	pids := fs.Int64("pids-limit", 4096, "appliance PID limit")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "nephos up: unexpected positional arguments")
		return exitUsage
	}
	bytes, err := parseMemory(*memory)
	if err != nil || math.IsNaN(*cpus) || math.IsInf(*cpus, 0) || *cpus <= 0 || *cpus > 128 || *pids <= 0 {
		fmt.Fprintln(stderr, "nephos up: invalid resource limits")
		return exitUsage
	}
	m, err := newManager()
	if err != nil {
		fmt.Fprintf(stderr, "nephos up: %v\n", err)
		return 1
	}
	limits := appliance.Limits{MemoryBytes: bytes, NanoCPUs: int64(*cpus * 1e9), PIDs: *pids}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "memory":
			limits.Explicit.Memory = true
		case "cpus":
			limits.Explicit.CPUs = true
		case "pids-limit":
			limits.Explicit.PIDs = true
		}
	})
	if err := m.Up(context.Background(), limits); err != nil {
		fmt.Fprintf(stderr, "nephos up: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Nephos appliance ready at http://127.0.0.1:7788")
	return exitOK
}

func runDown(args []string, stdout, stderr *os.File) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "nephos down: unexpected arguments")
		return exitUsage
	}
	m, err := newManager()
	if err != nil {
		fmt.Fprintf(stderr, "nephos down: %v\n", err)
		return 1
	}
	if err := m.Down(context.Background()); err != nil {
		fmt.Fprintf(stderr, "nephos down: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Nephos appliance stopped; data volume preserved")
	return exitOK
}

func runStatus(args []string, stdout, stderr *os.File) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "nephos status: unexpected arguments")
		return exitUsage
	}
	m, err := newManager()
	if err != nil {
		fmt.Fprintf(stderr, "nephos status: %v\n", err)
		return 1
	}
	status, err := m.Status(context.Background())
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
