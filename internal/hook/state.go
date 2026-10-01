// Package hook implements the private OCI createRuntime plumbing protocol.
package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

// MaxStateBytes bounds the OCI state read from the runtime.
const MaxStateBytes = 64 * 1024

// SocketPath is private to the appliance mount namespace.
const SocketPath = "/run/nephos/hook.sock"
const instanceAnnotation = "io.nephos.instance-id"

var instanceID = regexp.MustCompile("^i-[0-9a-f]{17}$")
var containerID = regexp.MustCompile("^[0-9a-f]{64}$")

// Request carries the minimum runtime identity needed for ENI plumbing.
type Request struct {
	InstanceID  string `json:"instance_id"`
	ContainerID string `json:"container_id"`
	PID         int    `json:"pid"`
}

func (r Request) validate() error {
	if !instanceID.MatchString(r.InstanceID) || !containerID.MatchString(r.ContainerID) || r.PID <= 0 {
		return fmt.Errorf("invalid instance, runtime identity or PID")
	}
	return nil
}

// DecodeState validates one bounded OCI state before contacting the daemon.
func DecodeState(reader io.Reader) (Request, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxStateBytes+1))
	if err != nil {
		return Request{}, fmt.Errorf("read OCI state: %w", err)
	}
	if len(raw) > MaxStateBytes {
		return Request{}, fmt.Errorf("OCI state exceeds %d bytes", MaxStateBytes)
	}
	var state struct {
		ID          string            `json:"id"`
		PID         int               `json:"pid"`
		Bundle      string            `json:"bundle"`
		Annotations map[string]string `json:"annotations"`
	}
	// OCI state has standard fields and runtime extensions beyond this subset.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&state); err != nil {
		return Request{}, fmt.Errorf("decode OCI state: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Request{}, fmt.Errorf("trailing OCI state")
	}
	request := Request{InstanceID: state.Annotations[instanceAnnotation], ContainerID: state.ID, PID: state.PID}
	if err := request.validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}
