# M1 connectivity and isolation matrix

This matrix separates observed slice-2 VPC/subnet topology from packet tests
that require the slice-3 instance and ENI path. The source behavior is
[ADR-0005](../adr/0005-nephos-owned-routed-network-plane.md); the M1 scope is
[ROADMAP](../ROADMAP.md#m1-thinnest-end-to-end-slice-two-instances-ping).
The executable slice-2 rows are in `tests/vpc-subnet-smoke.sh` (native Docker CI
and WSL2) and privileged `internal/network` integration tests. A namespace and
gateway existing is not proof that packets traverse them.

| Case | AWS-style behavior / Nephos constraint | Slice and assertion |
|---|---|---|
| Two VPCs both `10.0.0.0/16` | VPC address spaces are independent; overlapping ranges are legal | Slice 2: both become `available`, have different `nx-vpc-*` namespace names/short indexes, and keep separate gateways. |
| Subnet `10.0.1.0/24` in VPC A and `10.0.2.0/24` in VPC B | Each subnet has a base+1 gateway in its own VPC router | Slice 2: `nxr0` has the expected `/32` only in its owning namespace; deletion removes it. |
| Appliance `down` then `up` | SQLite desired state reconstructs kernel state | Slice 2: IDs, namespaces/short indexes, and `/32` gateways survive a restart. |
| Host boundary | VPC topology stays inside the appliance, not the host namespace | Slice 2: host namespace inode, links, routes, policy rules, and (when readable) nftables are snapshotted before startup and compared after test-owned object cleanup. Native CI requires nftables inspection. |
| Cross-subnet ICMP in one VPC | The local route permits private connectivity between subnets | Slice 3: launch two instances through the API/CLI, inspect ENI/veth and `/32` routes, and assert a real ping succeeds. No slice-2 packet result is claimed. |
| Same-address VPC packet isolation | No crosstalk between VPCs with overlapping CIDRs | Slice 3: instances in distinct VPCs cannot reach each other despite equal prefixes. Namespace separation alone is not a packet test. |
| Instance source spoof and missing ENI | Only the assigned private IP may source packets; failed plumbing must fail closed | Slice 3: inject bad source/hook failure and assert no silent `running` state or packet escape. |

Security groups and NACLs are M4, not M1; their packet matrix will be added
with their implementation. DNS/IMDS is M2, and internet edge routing is M3.
