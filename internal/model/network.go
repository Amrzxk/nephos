// Package model holds immutable resource snapshots shared by services,
// reconcilers, and kernel adapters without importing their implementations.
package model

import "net/netip"

// State is a VPC or subnet reconciliation state.
type State string

const (
	// StatePending is desired state not yet observed in the kernel.
	StatePending State = "pending"
	// StateAvailable is the converged resource state.
	StateAvailable State = "available"
	// StateDeleting awaits reverse-order teardown.
	StateDeleting State = "deleting"
	// StateFailed records an observed error for retry.
	StateFailed State = "failed"
)

// VPC is a desired/observed network resource snapshot from SQLite.
type VPC struct {
	ID                 string
	WorkspaceID        string
	Name               string
	CIDRBlock          netip.Prefix
	ShortIndex         int64
	State              State
	StateReason        string
	DeletionRequested  bool
	Generation         int64
	ObservedGeneration int64
}

// Subnet is a logical range whose gateway belongs to its VPC namespace.
type Subnet struct {
	ID                 string
	WorkspaceID        string
	VPCID              string
	Name               string
	CIDRBlock          netip.Prefix
	AvailabilityZone   string
	ShortIndex         int64
	State              State
	StateReason        string
	DeletionRequested  bool
	Generation         int64
	ObservedGeneration int64
}
