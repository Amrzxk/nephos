package hook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHookState(t *testing.T) {
	valid := Request{InstanceID: "i-00000000000000001", ContainerID: strings.Repeat("a", 64), PID: 42}
	doc := map[string]any{"ociVersion": "1.0.2", "id": valid.ContainerID, "pid": valid.PID, "bundle": "/run/containers/x", "annotations": map[string]string{"io.nephos.instance-id": valid.InstanceID}}
	raw, _ := json.Marshal(doc)
	got, err := DecodeState(strings.NewReader(string(raw)))
	if err != nil || got != valid {
		t.Fatalf("valid %+v %v", got, err)
	}
	cases := map[string]string{"empty": "", "invalid": "{", "oversized": strings.Repeat(" ", MaxStateBytes+1),
		"missing":  "{\"id\":\"" + valid.ContainerID + "\",\"pid\":42}",
		"mismatch": "{\"id\":\"" + valid.ContainerID + "\",\"pid\":42,\"annotations\":{\"io.nephos.instance-id\":\"wrong\"}}",
		"zero":     strings.Replace(string(raw), "\"pid\":42", "\"pid\":0", 1),
		"trailing": string(raw) + " {}", "null": "null"}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeState(strings.NewReader(input)); err == nil {
				t.Fatal("invalid OCI state accepted")
			}
		})
	}
}
func TestHookUnixFailure(t *testing.T) {
	for _, socket := range []string{"http://localhost", "relative.sock", "/run/nephos/missing-test-hook.sock"} {
		if err := Plumb(context.Background(), socket, Request{InstanceID: "i-00000000000000001", ContainerID: strings.Repeat("a", 64), PID: 42}); err == nil {
			t.Fatal("invalid socket succeeded")
		}
	}
}
