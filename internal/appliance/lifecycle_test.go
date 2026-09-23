package appliance

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type fakeEngine struct {
	host          HostInfo
	image         bool
	container     bool
	volume        bool
	owned         bool
	volumeOwned   bool
	running       bool
	creates       int
	volumeCreates int
	starts        int
	stops         int
	copies        int
}

func (f *fakeEngine) Ping(context.Context) error                { return nil }
func (f *fakeEngine) Info(context.Context) (HostInfo, error)    { return f.host, nil }
func (f *fakeEngine) ImageExists(context.Context) (bool, error) { return f.image, nil }
func (f *fakeEngine) Inspect(context.Context) (ContainerState, error) {
	if !f.container {
		return ContainerState{}, ErrNotFound
	}
	return ContainerState{Running: f.running, Owned: f.owned, Volume: volumeName, Image: imageName}, nil
}
func (f *fakeEngine) InspectVolume(context.Context) (VolumeState, error) {
	if !f.volume {
		return VolumeState{}, ErrNotFound
	}
	return VolumeState{Owned: f.volumeOwned}, nil
}
func (f *fakeEngine) CreateVolume(context.Context) error {
	f.volume = true
	f.volumeOwned = true
	f.volumeCreates++
	return nil
}
func (f *fakeEngine) Create(_ context.Context, _ Limits) error {
	f.container = true
	f.owned = true
	f.creates++
	return nil
}
func (f *fakeEngine) Start(context.Context) error {
	f.running = true
	f.starts++
	return nil
}
func (f *fakeEngine) Stop(context.Context) error {
	f.running = false
	f.stops++
	return nil
}
func (f *fakeEngine) Remove(context.Context) error { return errors.New("unexpected remove") }
func (f *fakeEngine) RemoveVolume(context.Context) error {
	return errors.New("unexpected volume remove")
}
func (f *fakeEngine) CopyFile(context.Context) (io.ReadCloser, error) {
	f.copies++
	return io.NopCloser(strings.NewReader("archive")), nil
}

type fakeHealth struct{ state HealthState }

func (h fakeHealth) Check(context.Context) (HealthState, error) { return h.state, nil }

type fakeCredentials struct{ saves int }

func (c *fakeCredentials) Save(_ context.Context, r io.Reader) error {
	if _, err := io.Copy(io.Discard, r); err != nil {
		return err
	}
	c.saves++
	return nil
}

func validHost() HostInfo {
	return HostInfo{CgroupVersion: "2", KernelVersion: "5.15.0-100-generic", OSType: "linux", Architecture: "x86_64", MemTotal: 8 << 30, CPUs: 4, MemoryLimit: true, PidsLimit: true, CPUQuota: true}
}

func TestUpCreatesOnceAndReusesStoppedAppliance(t *testing.T) {
	ctx := context.Background()
	engine := &fakeEngine{host: validHost(), image: true}
	creds := &fakeCredentials{}
	m := Manager{Engine: engine, Health: fakeHealth{HealthReady}, Credentials: creds, PollInterval: time.Millisecond, ReadyTimeout: time.Second}
	limits := Limits{MemoryBytes: 4 << 30, NanoCPUs: 2e9, PIDs: 4096}
	if err := m.Up(ctx, limits); err != nil {
		t.Fatal(err)
	}
	if engine.creates != 1 || engine.volumeCreates != 1 || engine.starts != 1 || engine.copies != 1 || creds.saves != 1 {
		t.Fatalf("wrong first boot counts: %+v, saves=%d", engine, creds.saves)
	}
	if err := m.Up(ctx, limits); err != nil {
		t.Fatal(err)
	}
	if engine.creates != 1 || engine.volumeCreates != 1 || engine.starts != 1 {
		t.Fatalf("duplicate appliance: %+v", engine)
	}
	if err := m.Down(ctx); err != nil {
		t.Fatal(err)
	}
	if engine.stops != 1 || engine.running {
		t.Fatalf("down failed: %+v", engine)
	}
	if err := m.Up(ctx, limits); err != nil {
		t.Fatal(err)
	}
	if engine.creates != 1 || engine.volumeCreates != 1 || engine.starts != 2 {
		t.Fatalf("restart created an object: %+v", engine)
	}
}

func TestUpMissingImageMutatesNothing(t *testing.T) {
	engine := &fakeEngine{host: validHost()}
	m := Manager{Engine: engine, Health: fakeHealth{HealthReady}, Credentials: &fakeCredentials{}}
	err := m.Up(context.Background(), Limits{MemoryBytes: 4 << 30, NanoCPUs: 2e9, PIDs: 4096})
	if err == nil || !strings.Contains(err.Error(), "make dev-ami && make appliance") {
		t.Fatalf("missing image guidance: %v", err)
	}
	if engine.creates != 0 || engine.volumeCreates != 0 || engine.starts != 0 {
		t.Fatalf("mutated Docker on missing image: %+v", engine)
	}
}

func TestUpRejectsForeignObjects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		engine fakeEngine
	}{
		{"container", fakeEngine{host: validHost(), image: true, container: true, volume: true, volumeOwned: true}},
		{"volume", fakeEngine{host: validHost(), image: true, volume: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Manager{Engine: &tc.engine, Health: fakeHealth{HealthReady}, Credentials: &fakeCredentials{}}
			if err := m.Up(context.Background(), Limits{MemoryBytes: 4 << 30, NanoCPUs: 2e9, PIDs: 4096}); err == nil {
				t.Fatal("accepted foreign Docker object")
			}
			if tc.engine.creates != 0 || tc.engine.starts != 0 {
				t.Fatal("mutated foreign Docker object")
			}
		})
	}
}

func TestPreflightRejectsUnsupportedHost(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*HostInfo)
	}{
		{"rootless", func(h *HostInfo) { h.Rootless = true }},
		{"cgroup v1", func(h *HostInfo) { h.CgroupVersion = "1" }},
		{"old kernel", func(h *HostInfo) { h.KernelVersion = "5.10.0" }},
		{"memory", func(h *HostInfo) { h.MemTotal = 2 << 30 }},
		{"architecture", func(h *HostInfo) { h.Architecture = "riscv64" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := validHost()
			tc.change(&h)
			engine := &fakeEngine{host: h, image: true}
			m := Manager{Engine: engine, Health: fakeHealth{HealthReady}, Credentials: &fakeCredentials{}}
			if err := m.Up(context.Background(), Limits{MemoryBytes: 4 << 30, NanoCPUs: 2e9, PIDs: 4096}); err == nil {
				t.Fatal("accepted unsupported host")
			}
			if engine.creates != 0 || engine.volumeCreates != 0 {
				t.Fatal("mutated before preflight")
			}
		})
	}
}

func TestStatusDistinguishesLifecycleStates(t *testing.T) {
	for _, tc := range []struct {
		name            string
		exists, running bool
		health          HealthState
		want            Status
	}{
		{"missing", false, false, HealthReady, StatusMissing},
		{"stopped", true, false, HealthReady, StatusStopped},
		{"starting", true, true, HealthStarting, StatusStarting},
		{"ready", true, true, HealthReady, StatusReady},
		{"unhealthy", true, true, HealthUnhealthy, StatusUnhealthy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &fakeEngine{host: validHost(), image: true, container: tc.exists, owned: true, volume: tc.exists, volumeOwned: true, running: tc.running}
			m := Manager{Engine: engine, Health: fakeHealth{tc.health}}
			got, err := m.Status(context.Background())
			if err != nil || got != tc.want {
				t.Fatalf("status=%s, err=%v; want %s", got, err, tc.want)
			}
		})
	}
}
