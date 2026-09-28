package model

import "net/netip"

// InstanceState is an instance's persisted lifecycle state.
type InstanceState string

const (
	InstancePending      InstanceState = "pending"
	InstanceRunning      InstanceState = "running"
	InstanceShuttingDown InstanceState = "shutting-down"
	InstanceFailed       InstanceState = "failed"
)

// Instance is a consistent desired/observed snapshot, including its primary ENI.
// RuntimeID is an observed runtime cache identity, not a desired API attribute.
type Instance struct {
	ID                 string
	WorkspaceID        string
	SubnetID           string
	ShortIndex         int64
	Name               string
	InstanceType       string
	State              InstanceState
	StateReason        string
	Generation         int64
	ObservedGeneration int64
	DeletionRequested  bool
	RuntimeID          string
	ENI                ENI
}

// ENI owns an instance's stable private address and link identity.
type ENI struct {
	ID                 string
	WorkspaceID        string
	InstanceID         string
	SubnetID           string
	ShortIndex         int64
	PrivateIP          netip.Addr
	MACAddress         string
	Generation         int64
	ObservedGeneration int64
	State              State
	StateReason        string
}
