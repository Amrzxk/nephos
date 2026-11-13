package appliance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runningAppliance(limits Limits) *fakeEngine {
	return &fakeEngine{host: validHost(), image: true, container: true, owned: true, running: true, limits: limits,
		volume: true, volumeOwned: true, volumeCreated: "2026-11-13T10:00:00Z"}
}

func TestPurgeRemovesContainerVolumeAndCredential(t *testing.T) {
	old := Limits{MemoryBytes: 6 << 30, NanoCPUs: 3e9, PIDs: 2048}
	engine := runningAppliance(old)
	creds := &fakeCredentials{}
	m := Manager{Engine: engine, Credentials: creds}
	got, err := m.Purge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.RemovedContainer || !got.RemovedVolume || got.Limits != old {
		t.Fatalf("result=%+v", got)
	}
	if engine.stops != 1 || engine.removes != 1 || engine.volumeRemoves != 1 || engine.container || engine.volume || creds.removes != 1 {
		t.Fatalf("engine=%+v credentialRemoves=%d", engine, creds.removes)
	}
}

func TestPurgeIsIdempotentWhenObjectsAreMissing(t *testing.T) {
	for name, engine := range map[string]*fakeEngine{
		"nothing":        {},
		"volume only":    {volume: true, volumeOwned: true},
		"container only": {container: true, owned: true},
	} {
		t.Run(name, func(t *testing.T) {
			creds := &fakeCredentials{}
			if _, err := (Manager{Engine: engine, Credentials: creds}).Purge(context.Background()); err != nil {
				t.Fatal(err)
			}
			if engine.container || engine.volume || creds.removes != 1 {
				t.Fatalf("engine=%+v", engine)
			}
		})
	}
}

func TestPurgeRefusesForeignObjectsBeforeDeletingAnything(t *testing.T) {
	tests := map[string]*fakeEngine{
		"foreign container": {container: true, owned: false, running: true, volume: true, volumeOwned: true},
		"foreign volume":    {container: true, owned: true, running: true, volume: true, volumeOwned: false},
	}
	for name, engine := range tests {
		t.Run(name, func(t *testing.T) {
			creds := &fakeCredentials{}
			_, err := (Manager{Engine: engine, Credentials: creds}).Purge(context.Background())
			if err == nil || !strings.Contains(err.Error(), "refusing to purge") {
				t.Fatalf("err=%v", err)
			}
			if engine.stops != 0 || engine.removes != 0 || engine.volumeRemoves != 0 || creds.removes != 0 {
				t.Fatalf("mutated before refusing: %+v", engine)
			}
		})
	}
}

func TestPurgeRefusesVolumeReplacedDuringPurge(t *testing.T) {
	engine := runningAppliance(Limits{})
	engine.replaceVolumeOnInspect = 2 // the recheck after container removal
	creds := &fakeCredentials{}
	_, err := (Manager{Engine: engine, Credentials: creds}).Purge(context.Background())
	if err == nil || !strings.Contains(err.Error(), "changed during the purge") {
		t.Fatalf("err=%v", err)
	}
	if engine.volumeRemoves != 0 || creds.removes != 0 {
		t.Fatalf("replaced volume was removed: %+v", engine)
	}
}

func TestHardResetKeepsObservedLimitsUnlessOverridden(t *testing.T) {
	old := Limits{MemoryBytes: 6 << 30, NanoCPUs: 3e9, PIDs: 2048}
	defaults := Limits{MemoryBytes: 4 << 30, NanoCPUs: 2e9, PIDs: 4096}
	tests := []struct {
		name      string
		requested Limits
		want      Limits
	}{
		{name: "defaults keep old limits", requested: defaults, want: old},
		{name: "explicit memory wins", requested: Limits{MemoryBytes: 8 << 30, NanoCPUs: 2e9, PIDs: 4096, Explicit: LimitSelection{Memory: true}},
			want: Limits{MemoryBytes: 8 << 30, NanoCPUs: 3e9, PIDs: 2048}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := runningAppliance(old)
			creds := &fakeCredentials{}
			m := Manager{Engine: engine, Health: fakeHealth{HealthReady}, Credentials: creds, PollInterval: time.Millisecond, ReadyTimeout: time.Second}
			if _, err := m.HardReset(context.Background(), tt.requested); err != nil {
				t.Fatal(err)
			}
			got := engine.limits
			got.Explicit = LimitSelection{}
			if got != tt.want {
				t.Fatalf("limits=%+v want %+v", got, tt.want)
			}
			if engine.removes != 1 || engine.volumeRemoves != 1 || engine.creates != 1 || engine.volumeCreates != 1 || !engine.running || creds.saves != 1 {
				t.Fatalf("engine=%+v saves=%d", engine, creds.saves)
			}
		})
	}
}

func TestHardResetWithNothingToPurgeStillStarts(t *testing.T) {
	engine := &fakeEngine{host: validHost(), image: true}
	creds := &fakeCredentials{}
	m := Manager{Engine: engine, Health: fakeHealth{HealthReady}, Credentials: creds, PollInterval: time.Millisecond, ReadyTimeout: time.Second}
	defaults := Limits{MemoryBytes: 4 << 30, NanoCPUs: 2e9, PIDs: 4096}
	if _, err := m.HardReset(context.Background(), defaults); err != nil {
		t.Fatal(err)
	}
	if engine.creates != 1 || !engine.running || engine.limits.MemoryBytes != defaults.MemoryBytes {
		t.Fatalf("engine=%+v", engine)
	}
}

func TestLocalCredentialRemoveIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := LocalCredentialStore{Path: path}
	for i := 0; i < 2; i++ {
		if err := store.Remove(); err != nil {
			t.Fatalf("remove %d: %v", i, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("credential still present: %v", err)
	}
}
