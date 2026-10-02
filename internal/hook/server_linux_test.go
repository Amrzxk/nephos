//go:build linux

package hook

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
)

type testPlumber struct{ calls int }

func (p *testPlumber) EnsureENI(context.Context, model.VPC, model.Subnet, model.ENI, *netns.InstanceTarget) error {
	p.calls++
	return nil
}
func hookRows(t *testing.T) (*store.Store, model.Instance, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	network := service.NewNetwork(s, nil, nil)
	vpc, err := network.CreateVPC(ctx, service.CreateVPCInput{Name: "hook", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	subnet, err := network.CreateSubnet(ctx, service.CreateSubnetInput{Name: "hook", VPCID: vpc.ID, CIDRBlock: "10.0.1.0/24", AvailabilityZone: "local-1a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTopology(ctx, vpc, []model.Subnet{subnet}, nil); err != nil {
		t.Fatal(err)
	}
	instance, err := service.NewInstances(s, nil, nil).Run(ctx, service.RunInstanceInput{Name: "hook", SubnetID: subnet.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	return s, instance, path
}
func TestHookIdentity(t *testing.T) {
	for _, kind := range []string{"wrong-container", "absent", "deleting", "self", "stale", "zero", "unknown-field", "missing-eni"} {
		t.Run(kind, func(t *testing.T) {
			s, instance, path := hookRows(t)
			ctx := context.Background()
			id := strings.Repeat("a", 64)
			if err := s.RecordRuntimeID(ctx, instance.ID, instance.Generation, id); err != nil {
				t.Fatal(err)
			}
			request := Request{InstanceID: instance.ID, ContainerID: id, PID: os.Getpid()}
			switch kind {
			case "wrong-container":
				request.ContainerID = strings.Repeat("b", 64)
			case "absent":
				request.InstanceID = "i-00000000000000000"
			case "deleting":
				if err := service.NewInstances(s, nil, nil).Terminate(ctx, instance.ID); err != nil {
					t.Fatal(err)
				}
			case "stale":
				request.PID = 2147483647
			case "zero":
				request.PID = 0
			case "missing-eni":
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, "DELETE FROM enis WHERE instance_id=?", instance.ID); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			body, _ := json.Marshal(request)
			if kind == "unknown-field" {
				body = []byte(strings.TrimSuffix(string(body), "}") + ",\"extra\":true}")
			}
			plumber := &testPlumber{}
			handler := NewHandler(s, plumber)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/plumb", strings.NewReader(string(body))))
			if recorder.Code < 400 || plumber.calls != 0 {
				t.Fatalf("unsafe %s accepted %d calls=%d", kind, recorder.Code, plumber.calls)
			}
		})
	}
}
func TestHookPeer(t *testing.T) {
	for _, uid := range []uint32{0, 1000, 65534} {
		if allowedPeer(uid) != (uid == 0) {
			t.Fatalf("peer uid %d policy", uid)
		}
	}
	path := filepath.Join(t.TempDir(), "hook.sock")
	ln, err := Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions=%v", info.Mode())
	}
	if _, err := Listen(context.Background(), path); err == nil {
		t.Fatal("existing socket overwritten")
	}
}
