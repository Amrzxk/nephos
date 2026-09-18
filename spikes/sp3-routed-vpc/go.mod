// A separate module on purpose: the spikes are throwaway (ROADMAP M0) and must
// not add dependencies to the main module, which stays dependency-free until
// M1. `go test ./...` and golangci-lint at the repository root do not see this.
module nephos-spike/sp3

go 1.27.1

require (
	github.com/vishvananda/netlink v1.3.1
	github.com/vishvananda/netns v0.0.5
	golang.org/x/sys v0.30.0
)
