package podman

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
)

const image = "nephos-ubuntu:dev"
const instanceLabel = "io.nephos.instance-id"

var instanceIDPattern = regexp.MustCompile("^i-[0-9a-f]{17}$")
var runtimeIDPattern = regexp.MustCompile("^[0-9a-f]{64}$")

type containerInspect struct {
	ID     string `json:"Id"`
	Name   string
	Config struct{ Labels, Annotations map[string]string }
	State  struct{ Running bool }
}

func owned(doc containerInspect) error {
	if !instanceIDPattern.MatchString(doc.Name) || !runtimeIDPattern.MatchString(doc.ID) ||
		doc.Config.Labels["io.nephos.managed"] != "true" || doc.Config.Labels["io.nephos.workspace-id"] != "default" ||
		doc.Config.Labels[instanceLabel] != doc.Name || doc.Config.Annotations[instanceLabel] != doc.Name {
		return fmt.Errorf("refusing foreign Podman container %q", doc.Name)
	}
	return nil
}
func (c *Client) inspectOwned(ctx context.Context, id compute.RuntimeID) (containerInspect, error) {
	var doc containerInspect
	if !runtimeIDPattern.MatchString(string(id)) {
		return doc, fmt.Errorf("invalid runtime ID")
	}
	if err := c.request(ctx, http.MethodGet, "/containers/"+string(id)+"/json", nil, &doc); err != nil {
		return doc, err
	}
	if err := owned(doc); err != nil {
		return doc, err
	}
	if doc.ID != string(id) {
		return doc, fmt.Errorf("runtime identity changed")
	}
	return doc, nil
}

// EnsureImage requires the locally built M1 development AMI.
func (c *Client) EnsureImage(ctx context.Context, ref string) error {
	if ref != image {
		return fmt.Errorf("unsupported M1 image %q", ref)
	}
	if err := c.request(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/exists", nil, nil); err != nil {
		return fmt.Errorf("development AMI must be built and imported locally: %w", err)
	}
	return nil
}

// Create creates or rediscovers one Nephos-owned instance container.
func (c *Client) Create(ctx context.Context, instance model.Instance) (compute.RuntimeID, error) {
	if !instanceIDPattern.MatchString(instance.ID) || instance.WorkspaceID != "default" || instance.InstanceType != "t3.micro" {
		return "", fmt.Errorf("invalid M1 instance runtime request")
	}
	if id, err := c.discover(ctx, instance.ID); err == nil {
		return id, nil
	} else if !isStatus(err, http.StatusNotFound) {
		return "", err
	}
	spec := map[string]any{
		"name": instance.ID, "image": image, "systemd": "always",
		"userns": map[string]string{"nsmode": "auto", "value": "size=65536"},
		// The REST spec does not parse ID mappings from an explicitly supplied
		// UserNS. Match the CLI's ParseIDMapping(auto:size=65536) result too.
		"idmappings": map[string]any{"HostUIDMapping": false, "HostGIDMapping": false, "AutoUserNs": true, "AutoUserNsOpts": map[string]uint32{"Size": 65536}},
		"netns":      map[string]string{"nsmode": "none"},
		"cap_add":    []string{"NET_ADMIN"}, "privileged": false, "seccomp_policy": "default",
		"sysctl":      map[string]string{"net.ipv4.ping_group_range": "0 65535"},
		"labels":      map[string]string{"io.nephos.managed": "true", instanceLabel: instance.ID, "io.nephos.workspace-id": "default"},
		"annotations": map[string]string{instanceLabel: instance.ID},
		"resource_limits": map[string]any{"memory": map[string]int64{"limit": 1 << 30},
			"cpu": map[string]int64{"quota": 200000, "period": 100000}, "pids": map[string]int64{"limit": 512}},
	}
	var response struct {
		ID string `json:"Id"`
	}
	if err := c.request(ctx, http.MethodPost, "/containers/create", spec, &response); err != nil {
		if isStatus(err, http.StatusConflict) {
			return c.discover(ctx, instance.ID)
		}
		return "", err
	}
	if !runtimeIDPattern.MatchString(response.ID) {
		return "", fmt.Errorf("podman returned an invalid container ID")
	}
	return compute.RuntimeID(response.ID), nil
}
func (c *Client) discover(ctx context.Context, name string) (compute.RuntimeID, error) {
	var doc containerInspect
	if err := c.request(ctx, http.MethodGet, "/containers/"+name+"/json", nil, &doc); err != nil {
		return "", err
	}
	if err := owned(doc); err != nil {
		return "", err
	}
	if doc.Name != name {
		return "", fmt.Errorf("foreign container name collision")
	}
	return compute.RuntimeID(doc.ID), nil
}

// Start starts a verified Nephos-owned instance container.
func (c *Client) Start(ctx context.Context, id compute.RuntimeID) error {
	if _, err := c.inspectOwned(ctx, id); err != nil {
		return err
	}
	return c.request(ctx, http.MethodPost, "/containers/"+string(id)+"/start", nil, nil)
}

// Delete removes only a verified Nephos-owned instance container.
func (c *Client) Delete(ctx context.Context, id compute.RuntimeID) error {
	if _, err := c.inspectOwned(ctx, id); err != nil {
		if isStatus(err, 404) {
			return nil
		}
		return err
	}
	err := c.request(ctx, http.MethodDelete, "/containers/"+string(id)+"?force=true&v=true&timeout=0", nil, nil)
	if isStatus(err, 404) {
		return nil
	}
	return err
}

// Inspect returns the observed state of a verified instance container.
func (c *Client) Inspect(ctx context.Context, id compute.RuntimeID) (compute.Status, error) {
	doc, err := c.inspectOwned(ctx, id)
	return compute.Status{Running: doc.State.Running}, err
}
