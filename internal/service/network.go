package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store"
)

const defaultWorkspace = "default"

// CreateVPCInput is the M1 subset of a VPC create request.
type CreateVPCInput struct {
	Name      string
	CIDRBlock string
}

// CreateSubnetInput is the M1 subset of a subnet create request.
type CreateSubnetInput struct {
	Name             string
	VPCID            string
	CIDRBlock        string
	AvailabilityZone string
}

// Network validates VPC/subnet requests and atomically records desired state.
// It never mutates the kernel; a post-commit enqueue only accelerates the
// level-triggered reconciler, whose sweep also repairs missed enqueues.
type Network struct {
	store   *store.Store
	enqueue func(string)
	now     func() time.Time
}

// NewNetwork wires persistence, post-commit enqueue, and an injectable clock.
func NewNetwork(s *store.Store, enqueue func(string), now func() time.Time) *Network {
	if now == nil {
		now = time.Now
	}
	return &Network{store: s, enqueue: enqueue, now: now}
}

// CreateVPC records a pending VPC and event atomically, then enqueues it.
func (n *Network) CreateVPC(ctx context.Context, input CreateVPCInput, key string) (model.VPC, error) {
	name, err := ValidateName(input.Name)
	if err != nil {
		return model.VPC{}, err
	}
	cidr, err := ParseCIDR(input.CIDRBlock)
	if err != nil {
		return model.VPC{}, err
	}
	if err := validateIdempotencyKey(key); err != nil {
		return model.VPC{}, err
	}
	hash, err := hashPayload(name, cidr.String())
	if err != nil {
		return model.VPC{}, err
	}
	id, err := NewID("vpc-", nil)
	if err != nil {
		return model.VPC{}, err
	}
	now := n.now().UTC()
	var result model.VPC
	created := false
	err = n.store.WithTx(ctx, func(tx *store.Tx) error {
		replay, found, err := loadReplay[model.VPC](ctx, tx, "create-vpc", key, hash, now)
		if err != nil {
			return err
		}
		if found {
			result = replay
			return nil
		}
		if _, err := tx.GetVPCByName(ctx, defaultWorkspace, name); err == nil {
			return nameConflict("VPC", name)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		shortIndex, err := tx.AllocateIndex(ctx, "vpc", id)
		if err != nil {
			return err
		}
		result = model.VPC{
			ID: id, WorkspaceID: defaultWorkspace, Name: name, CIDRBlock: cidr,
			ShortIndex: shortIndex, State: model.StatePending, Generation: 1,
		}
		if err := tx.InsertVPC(ctx, result, now.Unix()); err != nil {
			if store.IsUniqueViolation(err) {
				return nameConflict("VPC", name)
			}
			return err
		}
		if err := tx.AppendEvent(ctx, store.Event{
			WorkspaceID: defaultWorkspace, ResourceType: "vpc", ResourceID: id,
			Action: "created", State: string(result.State), Generation: 1, CreatedAt: now.Unix(),
		}); err != nil {
			return err
		}
		if err := saveReplay(ctx, tx, "create-vpc", key, hash, id, result, now); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return model.VPC{}, err
	}
	if created && n.enqueue != nil {
		n.enqueue(result.ID)
	}
	return result, nil
}

// GetVPC returns the current persisted VPC snapshot by ID.
func (n *Network) GetVPC(ctx context.Context, id string) (model.VPC, error) {
	vpc, err := n.store.GetVPC(ctx, defaultWorkspace, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.VPC{}, vpcNotFound(id)
	}
	return vpc, err
}

// ListVPCs returns one stable ID-ordered page of VPCs.
func (n *Network) ListVPCs(ctx context.Context, limit int, token string) ([]model.VPC, string, error) {
	size, err := pageSize(limit)
	if err != nil {
		return nil, "", err
	}
	afterID, err := decodePageToken("vpc", token)
	if err != nil {
		return nil, "", err
	}
	items, err := n.store.ListVPCPage(ctx, defaultWorkspace, afterID, size+1)
	if err != nil {
		return nil, "", err
	}
	if len(items) <= size {
		return items, "", nil
	}
	next := encodePageToken("vpc", items[size-1].ID)
	return items[:size], next, nil
}

// DeleteVPC validates dependencies and requests asynchronous teardown.
func (n *Network) DeleteVPC(ctx context.Context, id string) (model.VPC, error) {
	now := n.now().UTC()
	var result model.VPC
	changed := false
	err := n.store.WithTx(ctx, func(tx *store.Tx) error {
		vpc, err := tx.GetVPC(ctx, defaultWorkspace, id)
		if errors.Is(err, sql.ErrNoRows) {
			return vpcNotFound(id)
		}
		if err != nil {
			return err
		}
		children, err := tx.ListSubnetsByVPC(ctx, id)
		if err != nil {
			return err
		}
		if len(children) != 0 {
			return dependencyViolation(id, "VPC still has subnets")
		}
		if vpc.State == model.StateDeleting {
			result = vpc
			return nil
		}
		result, err = tx.MarkVPCDeleting(ctx, defaultWorkspace, id, now.Unix())
		if err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, store.Event{
			WorkspaceID: defaultWorkspace, ResourceType: "vpc", ResourceID: id,
			Action: "deleting", State: string(result.State), Generation: result.Generation,
			CreatedAt: now.Unix(),
		}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return model.VPC{}, err
	}
	if changed && n.enqueue != nil {
		n.enqueue(id)
	}
	return result, nil
}

// CreateSubnet validates its VPC and siblings inside one transaction.
func (n *Network) CreateSubnet(ctx context.Context, input CreateSubnetInput, key string) (model.Subnet, error) {
	name, err := ValidateName(input.Name)
	if err != nil {
		return model.Subnet{}, err
	}
	cidr, err := ParseCIDR(input.CIDRBlock)
	if err != nil {
		return model.Subnet{}, err
	}
	if input.AvailabilityZone != "local-1a" && input.AvailabilityZone != "local-1b" && input.AvailabilityZone != "local-1c" {
		return model.Subnet{}, invalidParameter("availability_zone must be local-1a, local-1b, or local-1c")
	}
	if err := validateIdempotencyKey(key); err != nil {
		return model.Subnet{}, err
	}
	hash, err := hashPayload(name, input.VPCID, cidr.String(), input.AvailabilityZone)
	if err != nil {
		return model.Subnet{}, err
	}
	id, err := NewID("subnet-", nil)
	if err != nil {
		return model.Subnet{}, err
	}
	now := n.now().UTC()
	var result model.Subnet
	created := false
	err = n.store.WithTx(ctx, func(tx *store.Tx) error {
		replay, found, err := loadReplay[model.Subnet](ctx, tx, "create-subnet", key, hash, now)
		if err != nil {
			return err
		}
		if found {
			result = replay
			return nil
		}
		vpc, err := tx.GetVPC(ctx, defaultWorkspace, input.VPCID)
		if errors.Is(err, sql.ErrNoRows) {
			return vpcNotFound(input.VPCID)
		}
		if err != nil {
			return err
		}
		if vpc.State == model.StateDeleting {
			return dependencyViolation(vpc.ID, "VPC is deleting")
		}
		if err := ValidateSubnetRange(vpc.CIDRBlock, cidr); err != nil {
			return err
		}
		siblings, err := tx.ListSubnetsByVPC(ctx, vpc.ID)
		if err != nil {
			return err
		}
		siblingCIDRs := make([]netip.Prefix, 0, len(siblings))
		for i := range siblings {
			siblingCIDRs = append(siblingCIDRs, siblings[i].CIDRBlock)
		}
		if err := ValidateSubnetDisjoint(cidr, siblingCIDRs); err != nil {
			return err
		}
		if _, err := tx.GetSubnetByName(ctx, defaultWorkspace, name); err == nil {
			return nameConflict("subnet", name)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		shortIndex, err := tx.AllocateIndex(ctx, "subnet", id)
		if err != nil {
			return err
		}
		result = model.Subnet{
			ID: id, WorkspaceID: defaultWorkspace, VPCID: vpc.ID,
			Name: name, CIDRBlock: cidr, AvailabilityZone: input.AvailabilityZone,
			ShortIndex: shortIndex, State: model.StatePending, Generation: 1,
		}
		if err := tx.InsertSubnet(ctx, result, now.Unix()); err != nil {
			if store.IsUniqueViolation(err) {
				return nameConflict("subnet", name)
			}
			if store.IsForeignKeyViolation(err) {
				return vpcNotFound(input.VPCID)
			}
			return err
		}
		if err := tx.AppendEvent(ctx, store.Event{
			WorkspaceID: defaultWorkspace, ResourceType: "subnet", ResourceID: id,
			Action: "created", State: string(result.State), Generation: 1, CreatedAt: now.Unix(),
		}); err != nil {
			return err
		}
		if err := saveReplay(ctx, tx, "create-subnet", key, hash, id, result, now); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return model.Subnet{}, err
	}
	if created && n.enqueue != nil {
		n.enqueue(result.VPCID)
	}
	return result, nil
}

// GetSubnet returns the current persisted subnet snapshot by ID.
func (n *Network) GetSubnet(ctx context.Context, id string) (model.Subnet, error) {
	subnet, err := n.store.GetSubnet(ctx, defaultWorkspace, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Subnet{}, subnetNotFound(id)
	}
	return subnet, err
}

// ListSubnets returns one stable ID-ordered page of subnets.
func (n *Network) ListSubnets(ctx context.Context, limit int, token string) ([]model.Subnet, string, error) {
	size, err := pageSize(limit)
	if err != nil {
		return nil, "", err
	}
	afterID, err := decodePageToken("subnet", token)
	if err != nil {
		return nil, "", err
	}
	items, err := n.store.ListSubnetPage(ctx, defaultWorkspace, afterID, size+1)
	if err != nil {
		return nil, "", err
	}
	if len(items) <= size {
		return items, "", nil
	}
	next := encodePageToken("subnet", items[size-1].ID)
	return items[:size], next, nil
}

// DeleteSubnet validates dependencies and requests gateway teardown.
func (n *Network) DeleteSubnet(ctx context.Context, id string) (model.Subnet, error) {
	now := n.now().UTC()
	var result model.Subnet
	changed := false
	err := n.store.WithTx(ctx, func(tx *store.Tx) error {
		subnet, err := tx.GetSubnet(ctx, defaultWorkspace, id)
		if errors.Is(err, sql.ErrNoRows) {
			return subnetNotFound(id)
		}
		if err != nil {
			return err
		}
		instances, err := tx.CountInstancesInSubnet(ctx, id)
		if err != nil {
			return err
		}
		if instances != 0 {
			return dependencyViolation(id, "subnet still has instances")
		}
		if subnet.State == model.StateDeleting {
			result = subnet
			return nil
		}
		result, err = tx.MarkSubnetDeleting(ctx, defaultWorkspace, id, now.Unix())
		if err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, store.Event{
			WorkspaceID: defaultWorkspace, ResourceType: "subnet", ResourceID: id,
			Action: "deleting", State: string(result.State), Generation: result.Generation,
			CreatedAt: now.Unix(),
		}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return model.Subnet{}, err
	}
	if changed && n.enqueue != nil {
		n.enqueue(result.VPCID)
	}
	return result, nil
}

func nameConflict(kind, name string) error {
	return &Error{
		Code: "InvalidParameterValue", Message: fmt.Sprintf("%s name %q already exists", kind, name),
		Status: 409,
	}
}

func dependencyViolation(id, message string) error {
	return &Error{Code: "DependencyViolation", Message: message, ResourceID: id, Status: 409}
}

func vpcNotFound(id string) error {
	return &Error{Code: "InvalidVpcID.NotFound", Message: "VPC does not exist", ResourceID: id, Status: 404}
}

func subnetNotFound(id string) error {
	return &Error{Code: "InvalidSubnetID.NotFound", Message: "subnet does not exist", ResourceID: id, Status: 404}
}
