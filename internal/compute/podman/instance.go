package podman

import (
	"context"
	"errors"
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
	State  struct {
		Running bool
		PID     int `json:"Pid"`
	}
}

func owned(doc containerInspect) error {
	if !instanceIDPattern.MatchString(doc.Name) || !runtimeIDPattern.MatchString(doc.ID) ||
		doc.Config.Labels["io.nephos.managed"] != "true" || doc.Config.Labels["io.nephos.workspace-id"] != "default" ||
		doc.Config.Labels[instanceLabel] != doc.Name || doc.Config.Annotations[instanceLabel] != doc.Name {
		return fmt.Errorf("refusing foreign Podman container %q", doc.Name)
	}
	return nil
}
func validIdentity(identity compute.Identity) error {
	if !instanceIDPattern.MatchString(identity.InstanceID) || identity.WorkspaceID != "default" {
		return fmt.Errorf("invalid expected M1 instance identity")
	}
	return nil
}

func expected(doc containerInspect, identity compute.Identity) error {
	if err := validIdentity(identity); err != nil {
		return err
	}
	if err := owned(doc); err != nil {
		return err
	}
	if doc.Name != identity.InstanceID || doc.Config.Labels["io.nephos.workspace-id"] != identity.WorkspaceID {
		return fmt.Errorf("refusing foreign runtime identity: expected %s/%s, observed %q", identity.WorkspaceID, identity.InstanceID, doc.Name)
	}
	return nil
}

func (c *Client) inspectOwned(ctx context.Context, ref compute.Reference) (containerInspect, error) {
	var doc containerInspect
	if err := validIdentity(ref.Identity); err != nil {
		return doc, err
	}
	if !runtimeIDPattern.MatchString(string(ref.ID)) {
		return doc, fmt.Errorf("invalid runtime ID")
	}
	if err := c.request(ctx, http.MethodGet, "/containers/"+string(ref.ID)+"/json", nil, &doc); err != nil {
		return doc, err
	}
	if err := expected(doc, ref.Identity); err != nil {
		return doc, err
	}
	if doc.ID != string(ref.ID) {
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
func (c *Client) Create(ctx context.Context, instance model.Instance) (compute.CreateResult, error) {
	identity := compute.Identity{WorkspaceID: instance.WorkspaceID, InstanceID: instance.ID}
	if !instanceIDPattern.MatchString(instance.ID) || instance.WorkspaceID != "default" || instance.InstanceType != "t3.micro" {
		return compute.CreateResult{}, fmt.Errorf("invalid M1 instance runtime request")
	}
	if ref, err := c.Lookup(ctx, identity); err == nil {
		return compute.CreateResult{Reference: ref}, nil
	} else if !errors.Is(err, compute.ErrNotFound) {
		return compute.CreateResult{}, err
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
			ref, lookupErr := c.Lookup(ctx, identity)
			return compute.CreateResult{Reference: ref}, lookupErr
		}
		return compute.CreateResult{}, err
	}
	if !runtimeIDPattern.MatchString(response.ID) {
		return compute.CreateResult{}, fmt.Errorf("podman returned an invalid container ID")
	}
	ref := compute.Reference{Identity: identity, ID: compute.RuntimeID(response.ID)}
	if _, err := c.inspectOwned(ctx, ref); err != nil {
		return compute.CreateResult{}, fmt.Errorf("verify created container: %w", err)
	}
	return compute.CreateResult{Reference: ref, Created: true}, nil
}

// Lookup discovers the retained container by its exact desired name and ownership.
func (c *Client) Lookup(ctx context.Context, identity compute.Identity) (compute.Reference, error) {
	if err := validIdentity(identity); err != nil {
		return compute.Reference{}, err
	}
	var doc containerInspect
	if err := c.request(ctx, http.MethodGet, "/containers/"+identity.InstanceID+"/json", nil, &doc); err != nil {
		if isStatus(err, http.StatusNotFound) {
			return compute.Reference{}, fmt.Errorf("lookup %s: %w", identity.InstanceID, compute.ErrNotFound)
		}
		return compute.Reference{}, err
	}
	if err := expected(doc, identity); err != nil {
		return compute.Reference{}, err
	}
	return compute.Reference{Identity: identity, ID: compute.RuntimeID(doc.ID)}, nil
}

// Start starts a verified Nephos-owned instance container.
func (c *Client) Start(ctx context.Context, ref compute.Reference) error {
	if _, err := c.inspectOwned(ctx, ref); err != nil {
		return err
	}
	return c.request(ctx, http.MethodPost, "/containers/"+string(ref.ID)+"/start", nil, nil)
}

// Delete removes only a verified Nephos-owned instance container.
func (c *Client) Delete(ctx context.Context, ref compute.Reference) error {
	if _, err := c.inspectOwned(ctx, ref); err != nil {
		if isStatus(err, 404) {
			return nil
		}
		return err
	}
	err := c.request(ctx, http.MethodDelete, "/containers/"+string(ref.ID)+"?force=true&v=true&timeout=0", nil, nil)
	if isStatus(err, 404) {
		return nil
	}
	return err
}

// Inspect returns the observed state of a verified instance container.
func (c *Client) Inspect(ctx context.Context, ref compute.Reference) (compute.Status, error) {
	doc, err := c.inspectOwned(ctx, ref)
	if err != nil {
		return compute.Status{}, err
	}
	return compute.Status{Running: doc.State.Running, PID: doc.State.PID}, nil
}
