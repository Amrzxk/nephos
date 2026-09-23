package appliance

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"slices"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type dockerEngine struct {
	client *client.Client
}

// NewDockerEngine connects to the user's configured Docker daemon.
func NewDockerEngine() (Engine, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("configure Docker client: %w", err)
	}
	return &dockerEngine{client: cli}, nil
}

func (d *dockerEngine) Ping(ctx context.Context) error {
	_, err := d.client.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		return fmt.Errorf("connect to Docker: %w", err)
	}
	return nil
}

func (d *dockerEngine) Info(ctx context.Context) (HostInfo, error) {
	result, err := d.client.Info(ctx, client.InfoOptions{})
	if err != nil {
		return HostInfo{}, fmt.Errorf("inspect Docker host: %w", err)
	}
	i := result.Info
	return HostInfo{
		Rootless:      slices.Contains(i.SecurityOptions, "name=rootless"),
		CgroupVersion: i.CgroupVersion,
		KernelVersion: i.KernelVersion,
		OSType:        i.OSType,
		Architecture:  i.Architecture,
		MemTotal:      i.MemTotal,
		CPUs:          i.NCPU,
		MemoryLimit:   i.MemoryLimit,
		PidsLimit:     i.PidsLimit,
		CPUQuota:      i.CPUCfsQuota,
	}, nil
}

func (d *dockerEngine) ImageExists(ctx context.Context) (bool, error) {
	_, err := d.client.ImageInspect(ctx, imageName)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect appliance image: %w", err)
	}
	return true, nil
}

func (d *dockerEngine) Inspect(ctx context.Context) (ContainerState, error) {
	result, err := d.client.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{})
	if err != nil {
		return ContainerState{}, objectError("inspect appliance container", err)
	}
	c := result.Container
	state := ContainerState{}
	if c.State != nil {
		state.Running = c.State.Running
		state.Status = string(c.State.Status)
	}
	if c.Config != nil {
		state.Owned = c.Config.Labels[ownerLabel] == "true"
		state.Image = c.Config.Image
	}
	for _, m := range c.Mounts {
		if m.Type == mount.TypeVolume && m.Destination == "/var/lib/nephos" {
			state.Volume = m.Name
		}
	}
	return state, nil
}

func (d *dockerEngine) InspectVolume(ctx context.Context) (VolumeState, error) {
	result, err := d.client.VolumeInspect(ctx, volumeName, client.VolumeInspectOptions{})
	if err != nil {
		return VolumeState{}, objectError("inspect appliance volume", err)
	}
	return VolumeState{Owned: result.Volume.Labels[ownerLabel] == "true"}, nil
}

func (d *dockerEngine) CreateVolume(ctx context.Context) error {
	_, err := d.client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volumeName, Labels: map[string]string{ownerLabel: "true"}})
	if err != nil {
		return fmt.Errorf("create appliance volume: %w", err)
	}
	return nil
}

// ContainerConfig fixes the appliance image and isolation policy.
func ContainerConfig(limits Limits) (*container.Config, *container.HostConfig) {
	port := network.MustParsePort("7788/tcp")
	pids := limits.PIDs
	return &container.Config{
		Image:        imageName,
		Labels:       map[string]string{ownerLabel: "true"},
		ExposedPorts: network.PortSet{port: {}},
	}, &container.HostConfig{
		Privileged:   true,
		CgroupnsMode: "private",
		NetworkMode:  "default",
		PidMode:      "",
		Mounts:       []mount.Mount{{Type: mount.TypeVolume, Source: volumeName, Target: "/var/lib/nephos"}},
		PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "7788"}}},
		Resources:    container.Resources{Memory: limits.MemoryBytes, NanoCPUs: limits.NanoCPUs, PidsLimit: &pids},
	}
}

func (d *dockerEngine) Create(ctx context.Context, limits Limits) error {
	cfg, host := ContainerConfig(limits)
	_, err := d.client.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: host, Name: containerName})
	if err != nil {
		return fmt.Errorf("create appliance container: %w", err)
	}
	return nil
}

func (d *dockerEngine) Start(ctx context.Context) error {
	_, err := d.client.ContainerStart(ctx, containerName, client.ContainerStartOptions{})
	if err != nil {
		return fmt.Errorf("start appliance container: %w", err)
	}
	return nil
}

func (d *dockerEngine) Stop(ctx context.Context) error {
	_, err := d.client.ContainerStop(ctx, containerName, client.ContainerStopOptions{})
	if err != nil {
		return objectError("stop appliance container", err)
	}
	return nil
}

func (d *dockerEngine) Remove(ctx context.Context) error {
	_, err := d.client.ContainerRemove(ctx, containerName, client.ContainerRemoveOptions{})
	if err != nil {
		return objectError("remove appliance container", err)
	}
	return nil
}

func (d *dockerEngine) RemoveVolume(ctx context.Context) error {
	_, err := d.client.VolumeRemove(ctx, volumeName, client.VolumeRemoveOptions{})
	if err != nil {
		return objectError("remove appliance volume", err)
	}
	return nil
}

func (d *dockerEngine) CopyFile(ctx context.Context) (io.ReadCloser, error) {
	result, err := d.client.CopyFromContainer(ctx, containerName, client.CopyFromContainerOptions{SourcePath: "/var/lib/nephos/secrets/api-token"})
	if err != nil {
		return nil, objectError("copy appliance token", err)
	}
	return result.Content, nil
}

func objectError(action string, err error) error {
	if cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%s: %w", action, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", action, err)
}
