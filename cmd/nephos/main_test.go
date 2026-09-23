package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (exitCode int, output, errorOutput string) {
	t.Helper()
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	code := run(args, out, stderr)
	if err := out.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := stderr.Sync(); err != nil {
		t.Fatal(err)
	}
	outBytes, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	errBytes, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(outBytes), string(errBytes)
}

func TestVersionFlagAfterCommand(t *testing.T) {
	code, out, errOut := runCLI(t, "version", "--json")
	if code != 0 || !strings.Contains(out, `"version"`) || errOut != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func TestUpInvalidLimitsFailBeforeDocker(t *testing.T) {
	for _, args := range [][]string{
		{"up", "--memory=banana"},
		{"up", "--cpus=0"},
		{"up", "--pids-limit=-1"},
	} {
		code, _, errOut := runCLI(t, args...)
		if code != exitUsage || errOut == "" {
			t.Fatalf("args=%v code=%d stderr=%q", args, code, errOut)
		}
	}
}

func TestDownRejectsPurgeUntilResetSlice(t *testing.T) {
	code, _, _ := runCLI(t, "down", "--purge")
	if code != exitUsage {
		t.Fatalf("unexpected exit %d", code)
	}
}
