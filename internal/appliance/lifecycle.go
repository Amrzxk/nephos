package appliance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Status is the learner-facing appliance lifecycle state.
type Status string

// Appliance status values.
const (
	StatusMissing   Status = "missing"
	StatusStopped   Status = "stopped"
	StatusStarting  Status = "starting"
	StatusReady     Status = "ready"
	StatusUnhealthy Status = "unhealthy"
)

// HealthState is the HTTP server's startup state as seen from the host.
type HealthState string

// Health probe results.
const (
	HealthStarting  HealthState = "starting"
	HealthReady     HealthState = "ready"
	HealthUnhealthy HealthState = "unhealthy"
)

// HealthProbe checks the public endpoint without using the secret token.
type HealthProbe interface {
	Check(context.Context) (HealthState, error)
}

// CredentialStore validates and saves the token copied from the appliance.
type CredentialStore interface {
	Save(context.Context, io.Reader) error
}

// Manager owns the single named appliance and its credential bootstrap.
type Manager struct {
	Engine       Engine
	Health       HealthProbe
	Credentials  CredentialStore
	PollInterval time.Duration
	ReadyTimeout time.Duration
}

// Up starts or reuses the appliance after all host checks have passed.
func (m Manager) Up(ctx context.Context, limits Limits) error {
	if err := m.Engine.Ping(ctx); err != nil {
		return err
	}
	host, err := m.Engine.Info(ctx)
	if err != nil {
		return err
	}
	exists, err := m.Engine.ImageExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("appliance image is missing; run make dev-ami && make appliance")
	}

	c, err := m.Engine.Inspect(ctx)
	existing := err == nil
	if errors.Is(err, ErrNotFound) {
		if err := preflight(host, limits); err != nil {
			return err
		}
		volume, volumeErr := m.Engine.InspectVolume(ctx)
		switch {
		case errors.Is(volumeErr, ErrNotFound):
			if createErr := m.Engine.CreateVolume(ctx); createErr != nil {
				return createErr
			}
		case volumeErr != nil:
			return volumeErr
		case !volume.Owned:
			return fmt.Errorf("docker volume %q exists but is not owned by Nephos", volumeName)
		}
		if createErr := m.Engine.Create(ctx, limits); createErr != nil {
			return createErr
		}
		c = ContainerState{Owned: true, Volume: volumeName, Image: imageName, Limits: limits}
	} else if err != nil {
		return err
	}
	if err := verifyContainer(c); err != nil {
		return err
	}
	if existing {
		if err := checkRequestedLimits(c.Limits, limits); err != nil {
			return err
		}
		// Docker's stored limits, not CLI defaults, govern a reused container.
		if err := preflight(host, c.Limits); err != nil {
			return err
		}
	}
	volume, err := m.Engine.InspectVolume(ctx)
	if err != nil {
		return err
	}
	if !volume.Owned {
		return fmt.Errorf("docker volume %q exists but is not owned by Nephos", volumeName)
	}
	if !c.Running {
		if err := m.Engine.Start(ctx); err != nil {
			return err
		}
	}
	if err := m.waitReady(ctx); err != nil {
		return err
	}
	archive, err := m.Engine.CopyFile(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	if err := m.Credentials.Save(ctx, archive); err != nil {
		return fmt.Errorf("save appliance credential: %w", err)
	}
	return nil
}

// Down stops the appliance without deleting its container or data volume.
func (m Manager) Down(ctx context.Context) error {
	if err := m.Engine.Ping(ctx); err != nil {
		return err
	}
	c, err := m.Engine.Inspect(ctx)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := verifyContainer(c); err != nil {
		return err
	}
	if !c.Running {
		return nil
	}
	return m.Engine.Stop(ctx)
}

// Status distinguishes absent, stopped, starting, ready, and unhealthy state.
func (m Manager) Status(ctx context.Context) (Status, error) {
	if err := m.Engine.Ping(ctx); err != nil {
		return StatusUnhealthy, err
	}
	c, err := m.Engine.Inspect(ctx)
	if errors.Is(err, ErrNotFound) {
		return StatusMissing, nil
	}
	if err != nil {
		return StatusUnhealthy, err
	}
	if err := verifyContainer(c); err != nil {
		return StatusUnhealthy, err
	}
	if !c.Running {
		return StatusStopped, nil
	}
	state, err := m.Health.Check(ctx)
	if err != nil {
		return StatusUnhealthy, fmt.Errorf("check appliance health: %w", err)
	}
	switch state {
	case HealthReady:
		return StatusReady, nil
	case HealthStarting:
		return StatusStarting, nil
	default:
		// The image imports its AMI before nephosd opens the HTTP listener.
		if c.HealthStatus == "starting" {
			return StatusStarting, nil
		}
		return StatusUnhealthy, nil
	}
}

func verifyContainer(c ContainerState) error {
	if !c.Owned || c.Volume != volumeName || c.Image != imageName {
		return fmt.Errorf("docker container %q exists but does not match the Nephos appliance", containerName)
	}
	return nil
}

func checkRequestedLimits(actual, requested Limits) error {
	if actual.MemoryBytes <= 0 || actual.NanoCPUs <= 0 || actual.PIDs <= 0 {
		return fmt.Errorf("existing appliance has missing Docker resource limits; inspect its configuration before restarting")
	}
	if requested.Explicit.Memory && actual.MemoryBytes != requested.MemoryBytes ||
		requested.Explicit.CPUs && actual.NanoCPUs != requested.NanoCPUs ||
		requested.Explicit.PIDs && actual.PIDs != requested.PIDs {
		return fmt.Errorf(
			"existing appliance limits are memory=%d bytes, cpus=%.3f, pids=%d; requested limits differ; run 'nephos down', then 'docker rm nephos' to recreate the container while preserving nephos-data",
			actual.MemoryBytes, float64(actual.NanoCPUs)/1e9, actual.PIDs,
		)
	}
	return nil
}

func preflight(host HostInfo, limits Limits) error {
	if limits.MemoryBytes < 3<<30 || limits.NanoCPUs <= 0 || limits.PIDs <= 0 {
		return fmt.Errorf("invalid appliance limits: memory must be at least 3 GiB and CPU/PIDs positive")
	}
	if host.Rootless {
		return fmt.Errorf("rootless Docker cannot run the privileged Nephos appliance")
	}
	if host.OSType != "linux" || host.CgroupVersion != "2" {
		return fmt.Errorf("nephos needs Linux Docker with cgroup v2")
	}
	if host.Architecture != "x86_64" && host.Architecture != "amd64" && host.Architecture != "aarch64" && host.Architecture != "arm64" {
		return fmt.Errorf("unsupported Docker architecture %q; use amd64 or arm64", host.Architecture)
	}
	if !kernelAtLeast(host.KernelVersion, 5, 15) {
		return fmt.Errorf("nephos needs Linux kernel 5.15 or newer; Docker reports %q", host.KernelVersion)
	}
	if host.MemTotal < limits.MemoryBytes {
		return fmt.Errorf("docker has %.2f GiB memory, below the %.2f GiB appliance limit; increase Docker/WSL memory", float64(host.MemTotal)/(1<<30), float64(limits.MemoryBytes)/(1<<30))
	}
	if !host.MemoryLimit || !host.PidsLimit || !host.CPUQuota {
		return fmt.Errorf("docker must support memory, pids, and CPU limits")
	}
	if host.CPUs < int((limits.NanoCPUs+1e9-1)/1e9) {
		return fmt.Errorf("docker exposes too few CPUs for the appliance limit")
	}
	return nil
}

func kernelAtLeast(release string, wantMajor, wantMinor int) bool {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return major > wantMajor || major == wantMajor && minor >= wantMinor
}

func (m Manager) waitReady(ctx context.Context) error {
	interval := m.PollInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	timeout := m.ReadyTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		state, err := m.Health.Check(readyCtx)
		if err == nil && state == HealthReady {
			return nil
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("appliance did not become ready within %s (last health state %s): %w", timeout, state, readyCtx.Err())
		case <-ticker.C:
		}
		c, inspectErr := m.Engine.Inspect(readyCtx)
		if inspectErr != nil {
			return inspectErr
		}
		if !c.Running {
			return fmt.Errorf("appliance stopped during startup (state %s); inspect Docker logs", c.Status)
		}
	}
}

// HTTPHealthProbe reads the appliance's public loopback-only health endpoint.
type HTTPHealthProbe struct {
	Client *http.Client
	URL    string
}

// Check reports unhealthy when the endpoint cannot be reached or returns an
// unexpected response; a 503 with starting status is not considered ready.
func (p HTTPHealthProbe) Check(ctx context.Context) (HealthState, error) {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	url := p.URL
	if url == "" {
		url = "http://127.0.0.1:7788/v1/health"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return HealthUnhealthy, fmt.Errorf("build health request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return HealthUnhealthy, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&body); err != nil {
		return HealthUnhealthy, nil
	}
	switch {
	case resp.StatusCode == http.StatusOK && body.Status == "ready":
		return HealthReady, nil
	case resp.StatusCode == http.StatusServiceUnavailable && body.Status == "starting":
		return HealthStarting, nil
	default:
		return HealthUnhealthy, nil
	}
}
