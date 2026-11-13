package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store"
)

// RunInstanceInput is the fixed M1 instance launch request.
// RunInstanceInput is one run request. An empty InstanceType selects
// model.DefaultInstanceType.
type RunInstanceInput struct{ Name, SubnetID, InstanceType string }

// Capacity bounds what the appliance accepts (ARCHITECTURE §5.1). Zero
// values disable the matching check, which only unit tests rely on.
type Capacity struct {
	// MemoryBytes is the appliance's own memory limit.
	MemoryBytes int64
	// ReserveBytes stays free for nephosd, Podman and the kernel.
	ReserveBytes int64
	// MaxInstances caps instances that hold capacity (AWS: InstanceLimitExceeded).
	MaxInstances int
}

// DefaultCapacityReserve and DefaultMaxInstances are the M2 defaults.
const (
	DefaultCapacityReserve = 512 << 20
	DefaultMaxInstances    = 20
)

// InstancePage is one ID-ordered page of current instance snapshots.
type InstancePage struct {
	Items         []model.Instance
	NextPageToken string
}

// Instances commits lifecycle decisions without kernel or runtime I/O.
type Instances struct {
	store   *store.Store
	enqueue func(string)
	now     func() time.Time
	resets  *ResetGuard
	limits  Capacity
}

// NewInstances wires persistence and optional post-commit acceleration.
func NewInstances(s *store.Store, enqueue func(string), now func() time.Time) *Instances {
	if now == nil {
		now = time.Now
	}
	return &Instances{store: s, enqueue: enqueue, now: now}
}

// GuardResets makes Run fail with ResetInProgress while guard is held.
func (s *Instances) GuardResets(guard *ResetGuard) { s.resets = guard }

// LimitCapacity enables the capacity and quota checks on Run.
func (s *Instances) LimitCapacity(limits Capacity) { s.limits = limits }

// Run atomically reserves the primary ENI, private IP, indexes, event and replay.
func (s *Instances) Run(ctx context.Context, input RunInstanceInput, key string) (model.Instance, error) {
	if err := s.resets.check(); err != nil {
		return model.Instance{}, err
	}
	name, err := ValidateName(input.Name)
	if err != nil {
		return model.Instance{}, err
	}
	if err := validateIdempotencyKey(key); err != nil {
		return model.Instance{}, err
	}
	instanceType := input.InstanceType
	if instanceType == "" {
		instanceType = model.DefaultInstanceType
	}
	spec, ok := model.LookupInstanceType(instanceType)
	if !ok {
		return model.Instance{}, &Error{Code: "InvalidParameterValue", Status: 400,
			Message: fmt.Sprintf("instance type %q is not supported; choose t3.nano, t3.micro, t3.small or t3.medium", instanceType)}
	}
	// The default type keeps the pre-M2 two-field hash, so replays recorded
	// before the upgrade still match.
	hashFields := []string{name, input.SubnetID}
	if instanceType != model.DefaultInstanceType {
		hashFields = append(hashFields, instanceType)
	}
	hash, err := hashPayload(hashFields...)
	if err != nil {
		return model.Instance{}, err
	}
	id, err := NewID("i-", nil)
	if err != nil {
		return model.Instance{}, err
	}
	eniID, err := NewID("eni-", nil)
	if err != nil {
		return model.Instance{}, err
	}
	now := s.now().UTC()
	var result model.Instance
	created := false
	err = s.store.WithTx(ctx, func(tx *store.Tx) error {
		replay, found, err := loadReplay[model.Instance](ctx, tx, "run-instance", key, hash, now)
		if err != nil {
			return err
		}
		if found {
			result = replay
			return nil
		}
		subnet, err := tx.GetSubnet(ctx, defaultWorkspace, input.SubnetID)
		if errors.Is(err, sql.ErrNoRows) {
			return subnetNotFound(input.SubnetID)
		}
		if err != nil {
			return err
		}
		if subnet.DeletionRequested {
			return incorrectInstanceState(subnet.ID, "subnet is being deleted")
		}
		if _, err := tx.GetInstanceByName(ctx, defaultWorkspace, name); err == nil {
			return nameConflict("instance", name)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := s.checkCapacity(ctx, tx, spec); err != nil {
			return err
		}
		used, err := tx.ENIAddresses(ctx, subnet.ID)
		if err != nil {
			return err
		}
		address, err := nextPrivateIP(subnet.CIDRBlock, used)
		if errors.Is(err, errAddressExhausted) {
			return &Error{Code: "AddressLimitExceeded", Message: "subnet has no free private addresses", ResourceID: subnet.ID, Status: 409}
		}
		if err != nil {
			return err
		}
		index, err := tx.AllocateIndex(ctx, "instance", id)
		if err != nil {
			return err
		}
		eniIndex, err := tx.AllocateIndex(ctx, "eni", eniID)
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(eniID))
		mac := net.HardwareAddr(digest[:6])
		mac[0] = (mac[0] & 0xfc) | 0x02
		eni := model.ENI{ID: eniID, WorkspaceID: defaultWorkspace, InstanceID: id, SubnetID: subnet.ID,
			ShortIndex: eniIndex, PrivateIP: address, MACAddress: mac.String(), Generation: 1, State: model.StatePending}
		result = model.Instance{ID: id, WorkspaceID: defaultWorkspace, SubnetID: subnet.ID, Name: name, ShortIndex: index,
			InstanceType: spec.Name, State: model.InstancePending, Generation: 1, ENI: eni}
		if err := tx.InsertInstanceWithENI(ctx, result, eni, now); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, store.Event{WorkspaceID: defaultWorkspace, ResourceType: "instance", ResourceID: id,
			Action: "created", State: string(result.State), Generation: 1, CreatedAt: now.Unix()}); err != nil {
			return err
		}
		if err := saveReplay(ctx, tx, "run-instance", key, hash, id, result, now); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return model.Instance{}, err
	}
	if created && s.enqueue != nil {
		s.enqueue(result.ID)
	}
	return result, nil
}

// Get reads the committed snapshot by ID.
func (s *Instances) Get(ctx context.Context, id string) (model.Instance, error) {
	result, err := s.store.GetInstance(ctx, defaultWorkspace, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Instance{}, instanceNotFound(id)
	}
	return result, err
}

// List returns a stable ID-ordered page with a resource-specific cursor.
func (s *Instances) List(ctx context.Context, limit int, token string) (InstancePage, error) {
	size, err := pageSize(limit)
	if err != nil {
		return InstancePage{}, err
	}
	after, err := decodePageToken("instance", token)
	if err != nil {
		return InstancePage{}, err
	}
	items, err := s.store.ListInstancePage(ctx, defaultWorkspace, after, size+1)
	if err != nil {
		return InstancePage{}, err
	}
	page := InstancePage{Items: items}
	if len(items) > size {
		page.Items = items[:size]
		page.NextPageToken = encodePageToken("instance", items[size-1].ID)
	}
	return page, nil
}

// Terminate records asynchronous deletion intent; the lease is not freed yet.
func (s *Instances) Terminate(ctx context.Context, id string) error {
	_, err := s.TerminateSnapshot(ctx, id)
	return err
}

// TerminateSnapshot returns the committed transitional state even if a worker
// removes the row immediately after this transaction commits.
func (s *Instances) TerminateSnapshot(ctx context.Context, id string) (model.Instance, error) {
	now := s.now().UTC()
	changed := false
	var result model.Instance
	err := s.store.WithTx(ctx, func(tx *store.Tx) error {
		current, err := tx.GetInstance(ctx, defaultWorkspace, id)
		if errors.Is(err, sql.ErrNoRows) {
			return instanceNotFound(id)
		}
		if err != nil {
			return err
		}
		if current.DeletionRequested {
			result = current
			return nil
		}
		if err := tx.MarkInstanceTerminating(ctx, defaultWorkspace, id, now); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, store.Event{WorkspaceID: defaultWorkspace, ResourceType: "instance", ResourceID: id,
			Action: "terminating", State: string(model.InstanceShuttingDown), Generation: current.Generation + 1, CreatedAt: now.Unix()}); err != nil {
			return err
		}
		current.DeletionRequested = true
		current.Generation++
		current.State = model.InstanceShuttingDown
		current.StateReason = ""
		result = current
		changed = true
		return nil
	})
	if err == nil && changed && s.enqueue != nil {
		s.enqueue(id)
	}
	return result, err
}

func instanceNotFound(id string) error {
	return &Error{Code: "InvalidInstanceID.NotFound", Message: "instance does not exist", ResourceID: id, Status: 404}
}

func incorrectInstanceState(id, message string) error {
	return &Error{Code: "IncorrectState", Message: message, ResourceID: id, Status: 409}
}

// checkCapacity runs inside the run transaction, so concurrent launches see
// each other's reservations and can never jointly overcommit the appliance.
func (s *Instances) checkCapacity(ctx context.Context, tx *store.Tx, spec model.InstanceTypeSpec) error {
	if s.limits.MemoryBytes == 0 && s.limits.MaxInstances == 0 {
		return nil
	}
	types, err := tx.CapacityInstanceTypes(ctx, defaultWorkspace)
	if err != nil {
		return err
	}
	if s.limits.MaxInstances > 0 && len(types) >= s.limits.MaxInstances {
		return &Error{Code: "InstanceLimitExceeded", Status: 400,
			Message: fmt.Sprintf("you have reached the limit of %d instances; stop or terminate one first", s.limits.MaxInstances)}
	}
	if s.limits.MemoryBytes == 0 {
		return nil
	}
	used := spec.MemoryBytes
	for _, name := range types {
		held, ok := model.LookupInstanceType(name)
		if !ok {
			return fmt.Errorf("stored instance has unknown type %q", name)
		}
		used += held.MemoryBytes
	}
	if available := s.limits.MemoryBytes - s.limits.ReserveBytes; used > available {
		return &Error{Code: "InsufficientInstanceCapacity", Status: 503,
			Message: fmt.Sprintf("the appliance has %d MiB for instances and %d MiB is in use; a %s needs %d MiB. Stop an instance, choose a smaller type, or run nephos reset --hard with a larger --memory",
				available>>20, (used-spec.MemoryBytes)>>20, spec.Name, spec.MemoryBytes>>20)}
	}
	return nil
}
