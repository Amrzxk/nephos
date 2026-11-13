package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withTerminal(t *testing.T, terminal bool) {
	t.Helper()
	previous := stdinIsTerminal
	stdinIsTerminal = func(*os.File) bool { return terminal }
	t.Cleanup(func() { stdinIsTerminal = previous })
}

// Every case must stop before Docker is contacted: none passes --yes with
// valid flags, and confirmation or validation fails first.
func TestDestructiveCommandsValidateBeforeDocker(t *testing.T) {
	withTerminal(t, false)
	tests := []struct {
		args   []string
		stderr string
	}{
		{args: []string{"down", "--purge"}, stderr: "rerun with --yes"},
		{args: []string{"down", "--yes"}, stderr: "--yes is only valid with --purge"},
		{args: []string{"down", "now"}, stderr: "unexpected arguments"},
		{args: []string{"reset", "--hard"}, stderr: "rerun with --yes"},
		{args: []string{"reset", "--hard", "--timeout", "1m"}, stderr: "--timeout cannot be combined with --hard"},
		{args: []string{"reset", "--hard", "-o", "json", "--yes"}, stderr: "-o cannot be combined with --hard"},
		{args: []string{"reset", "--memory", "2g"}, stderr: "--memory is only valid with --hard"},
		{args: []string{"reset", "--yes"}, stderr: "--yes is only valid with --hard"},
		{args: []string{"reset", "--hard", "--yes", "--cpus", "0"}, stderr: "invalid resource limits"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, _, stderr := runCLI(t, tt.args...)
			if code != exitUsage || !strings.Contains(stderr, tt.stderr) {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
		})
	}
}

func TestConfirmDestructiveOnTerminal(t *testing.T) {
	withTerminal(t, true)
	for _, tt := range []struct {
		answer string
		ok     bool
		code   int
	}{
		{answer: "yes\n", ok: true, code: exitOK},
		{answer: "  yes  \n", ok: true, code: exitOK},
		{answer: "y\n", ok: false, code: 1},
		{answer: "", ok: false, code: 1},
	} {
		dir := t.TempDir()
		in := filepath.Join(dir, "stdin")
		if err := os.WriteFile(in, []byte(tt.answer), 0o600); err != nil {
			t.Fatal(err)
		}
		stdin, err := os.Open(in)
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := os.Create(filepath.Join(dir, "stderr"))
		if err != nil {
			t.Fatal(err)
		}
		code, ok := confirmDestructive("reset --hard", false, stdin, stderr)
		_ = stdin.Close()
		_ = stderr.Close()
		prompt, _ := os.ReadFile(filepath.Join(dir, "stderr"))
		if ok != tt.ok || code != tt.code || !strings.Contains(string(prompt), "Type yes to continue") {
			t.Fatalf("answer %q: ok=%v code=%d prompt=%q", tt.answer, ok, code, prompt)
		}
	}
}

func TestConfirmDestructiveYesSkipsPrompt(t *testing.T) {
	withTerminal(t, false)
	if code, ok := confirmDestructive("down --purge", true, nil, nil); !ok || code != exitOK {
		t.Fatalf("--yes did not confirm: %d %v", code, ok)
	}
}
