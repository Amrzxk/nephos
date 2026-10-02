package podman

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
)

const testID = "i-00000000000000001"

var testRuntimeID = compute.RuntimeID(strings.Repeat("a", 64))
var testReference = compute.Reference{Identity: compute.Identity{WorkspaceID: "default", InstanceID: testID}, ID: testRuntimeID}

func unixServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "p.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return socket
}
func ownedInspect() map[string]any {
	return map[string]any{"Id": testRuntimeID, "Name": testID, "Config": map[string]any{
		"Labels":      map[string]string{"io.nephos.managed": "true", "io.nephos.instance-id": testID, "io.nephos.workspace-id": "default"},
		"Annotations": map[string]string{"io.nephos.instance-id": testID}}, "State": map[string]any{"Running": true}}
}
func TestPodmanCreateContract(t *testing.T) {
	var mu sync.Mutex
	created, starts := 0, 0
	socket := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/v5.4.2/libpod/containers/"+testID+"/json":
			if created == 0 {
				http.Error(w, "not found", 404)
				return
			}
			json.NewEncoder(w).Encode(ownedInspect())
		case r.Method == "POST" && r.URL.Path == "/v5.4.2/libpod/containers/create":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			expected := map[string]any{"name": testID, "image": "nephos-ubuntu:dev", "systemd": "always",
				"userns": map[string]any{"nsmode": "auto", "value": "size=65536"}, "netns": map[string]any{"nsmode": "none"},
				"idmappings": map[string]any{"HostUIDMapping": false, "HostGIDMapping": false, "AutoUserNs": true, "AutoUserNsOpts": map[string]any{"Size": float64(65536)}},
				"cap_add":    []any{"NET_ADMIN"}, "sysctl": map[string]any{"net.ipv4.ping_group_range": "0 65535"},
				"resource_limits": map[string]any{"memory": map[string]any{"limit": float64(1 << 30)}, "cpu": map[string]any{"quota": float64(200000), "period": float64(100000)}, "pids": map[string]any{"limit": float64(512)}}}
			for key, want := range expected {
				if !reflect.DeepEqual(body[key], want) {
					t.Errorf("%s=%#v want %#v", key, body[key], want)
				}
			}
			for _, key := range []string{"devices", "mounts", "portmappings", "seccomp_profile_path"} {
				if _, ok := body[key]; ok {
					t.Errorf("unsafe override %s", key)
				}
			}
			for _, key := range []string{"labels", "annotations"} {
				values, _ := body[key].(map[string]any)
				if values["io.nephos.instance-id"] != testID {
					t.Errorf("%s missing identity", key)
				}
			}
			if body["seccomp_policy"] != "default" || body["privileged"] != false {
				t.Error("default confinement missing")
			}
			created++
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"Id": testRuntimeID})
		case r.Method == "GET" && r.URL.Path == "/v5.4.2/libpod/containers/"+string(testRuntimeID)+"/json":
			json.NewEncoder(w).Encode(ownedInspect())
		case r.Method == "POST" && r.URL.Path == "/v5.4.2/libpod/containers/"+string(testRuntimeID)+"/start":
			starts++
			w.WriteHeader(304)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	}))
	c := New(socket)
	ctx := context.Background()
	for attempt := range 2 {
		id, err := c.Create(ctx, model.Instance{ID: testID, WorkspaceID: "default", InstanceType: "t3.micro"})
		if err != nil || id.Reference != testReference || id.Created != (attempt == 0) {
			t.Fatalf("create %+v %v", id, err)
		}
		if err := c.Start(ctx, id.Reference); err != nil {
			t.Fatal(err)
		}
	}
	if created != 1 || starts != 2 {
		t.Fatalf("created=%d starts=%d", created, starts)
	}
}
func TestPodmanForeignContainer(t *testing.T) {
	for _, operation := range []string{"create", "start", "delete", "exec"} {
		t.Run(operation, func(t *testing.T) {
			socket := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("mutated foreign container")
				}
				doc := ownedInspect()
				doc["Config"] = map[string]any{"Labels": map[string]string{}}
				json.NewEncoder(w).Encode(doc)
			}))
			c := New(socket)
			ctx := context.Background()
			var err error
			switch operation {
			case "create":
				_, err = c.Create(ctx, model.Instance{ID: testID, WorkspaceID: "default", InstanceType: "t3.micro"})
			case "start":
				err = c.Start(ctx, testReference)
			case "delete":
				err = c.Delete(ctx, testReference)
			case "exec":
				_, err = c.Exec(ctx, testReference, compute.ExecRequest{Command: []string{"/bin/true"}})
			}
			if err == nil || !strings.Contains(err.Error(), "foreign") {
				t.Fatalf("foreign %s: %v", operation, err)
			}
		})
	}
}
func TestPodmanUnixOnly(t *testing.T) {
	for _, path := range []string{"", "http://127.0.0.1:80", "tcp://localhost:80", "relative.sock"} {
		c := New(path)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := c.EnsureImage(ctx, "nephos-ubuntu:dev")
		cancel()
		if err == nil || !strings.Contains(err.Error(), "absolute Unix") {
			t.Fatalf("path %q: %v", path, err)
		}
	}
}
func TestPodmanErrorsAndCancellation(t *testing.T) {
	socket := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "image missing: build the AMI", 404) }))
	c := New(socket)
	if err := c.EnsureImage(context.Background(), "nephos-ubuntu:dev"); err == nil || !strings.Contains(err.Error(), "image missing") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("context lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.EnsureImage(ctx, "nephos-ubuntu:dev"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
