// Command nephos-spike-hook is the OCI createRuntime hook (ADR-0004 R3,
// ARCHITECTURE §3.3).
//
// Podman runs it after the container's namespaces exist but BEFORE PID 1
// starts. It reads the OCI container state from stdin, asks the plumbing
// daemon to wire the instance's interface, and exits non-zero if that fails —
// so the instance refuses to boot rather than starting with no network.
//
// It contains no logic of its own. Every decision belongs to the network
// engine, which is the only component that can be held to ADR-0005's honesty
// rule: Nephos never reports a rule as active when it isn't.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	socketPath = "/run/nephos/hook.sock"
	hookLabel  = "io.nephos.instance-id"
	timeout    = 10 * time.Second
)

// ociState is the subset of the OCI container state the hook needs. The full
// document has more; decoding only what is used keeps the hook indifferent to
// runtime-spec changes.
type ociState struct {
	ID          string            `json:"id"`
	Pid         int               `json:"pid"`
	Bundle      string            `json:"bundle"`
	Annotations map[string]string `json:"annotations"`
}

func main() {
	if err := run(); err != nil {
		// Exiting non-zero is the entire contract: a failure here must stop
		// the instance from starting.
		fmt.Fprintf(os.Stderr, "nephos-hook: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading the container state from stdin: %w", err)
	}

	var state ociState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("parsing the container state: %w", err)
	}
	if state.Pid == 0 {
		return fmt.Errorf("container state carries no PID; the hook must run at the createRuntime stage")
	}

	instanceID := state.Annotations[hookLabel]
	if instanceID == "" {
		// Not a Nephos instance. Podman applies hooks by directory, so this
		// is the filter that keeps the hook off unrelated containers.
		return nil
	}

	body, err := json.Marshal(map[string]any{
		"instance_id":  instanceID,
		"pid":          state.Pid,
		"container_id": state.ID,
	})
	if err != nil {
		return fmt.Errorf("encoding the plumb request: %w", err)
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}

	resp, err := client.Post("http://nephos/plumb", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("calling nephosd at %s: %w", socketPath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("nephosd refused to plumb instance %s: %s: %s",
			instanceID, resp.Status, bytes.TrimSpace(detail))
	}
	return nil
}
