// Package appliance manages the single local Nephos appliance through Docker.
package appliance

import (
	"context"
	"errors"
	"io"
)

const (
	containerName = "nephos"
	volumeName    = "nephos-data"
	imageName     = "nephos-appliance:dev"
	ownerLabel    = "io.nephos.appliance"
)

// ErrNotFound means the fixed Nephos container or volume does not exist.
var ErrNotFound = errors.New("appliance object not found")

// Limits are enforced by Docker on the appliance, not on the host process.
type Limits struct {
	MemoryBytes int64
	NanoCPUs    int64
	PIDs        int64
	Explicit    LimitSelection
}

// LimitSelection distinguishes CLI overrides from defaults when reusing a
// container whose original limits must remain in force.
type LimitSelection struct {
	Memory bool
	CPUs   bool
	PIDs   bool
}

// HostInfo contains Docker host capabilities needed for appliance preflight.
type HostInfo struct {
	Rootless      bool
	CgroupVersion string
	KernelVersion string
	OSType        string
	Architecture  string
	MemTotal      int64
	CPUs          int
	MemoryLimit   bool
	PidsLimit     bool
	CPUQuota      bool
}

// ContainerState is the observed state of the fixed Nephos container.
type ContainerState struct {
	Running      bool
	Owned        bool
	Status       string
	Image        string
	Volume       string
	Limits       Limits
	HealthStatus string
}

// VolumeState is the observed state of the fixed Nephos data volume.
type VolumeState struct {
	Owned bool
}

// Engine is intentionally bound to Nephos' fixed object names. Lifecycle code
// cannot accidentally mutate a caller-supplied container or volume name.
type Engine interface {
	Ping(context.Context) error
	Info(context.Context) (HostInfo, error)
	ImageExists(context.Context) (bool, error)
	Inspect(context.Context) (ContainerState, error)
	InspectVolume(context.Context) (VolumeState, error)
	CreateVolume(context.Context) error
	Create(context.Context, Limits) error
	Start(context.Context) error
	Stop(context.Context) error
	Remove(context.Context) error
	RemoveVolume(context.Context) error
	CopyFile(context.Context) (io.ReadCloser, error)
}
