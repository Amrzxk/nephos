// Command tasklimit is a bounded, test-only pids-limit probe. It is never
// shipped in the appliance or AMI. No child creates children.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func readCount(name string) (int, error) {
	b, err := os.ReadFile("/sys/fs/cgroup/" + name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}
func run() error {
	runtime.GOMAXPROCS(1)
	limit, err := readCount("pids.max")
	if err != nil {
		return err
	}
	if limit != 512 {
		return fmt.Errorf("unexpected test limit %d", limit)
	}
	baseline, err := readCount("pids.current")
	if err != nil {
		return err
	}
	children := make([]*exec.Cmd, 0, 600)
	released := false
	release := func() {
		for _, child := range children {
			_ = child.Process.Kill()
		}
		for _, child := range children {
			_ = child.Wait()
		}
		released = true
	}
	defer func() {
		if !released {
			release()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	refused := false
	for len(children) < 600 && time.Now().Before(deadline) {
		child := exec.Command("/bin/sleep", "30")
		if err := child.Start(); err != nil {
			if !errors.Is(err, syscall.EAGAIN) {
				return fmt.Errorf("child creation: %w", err)
			}
			refused = true
			break
		}
		children = append(children, child)
	}
	peak, err := readCount("pids.current")
	if err != nil {
		return err
	}
	alive := len(children) > 0 && children[0].Process.Signal(syscall.Signal(0)) == nil
	release()
	after, err := readCount("pids.current")
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Limit, Baseline, Peak, After, Children int
		Refused, ExistingAlive                 bool
	}{limit, baseline, peak, after, len(children), refused, alive})
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
