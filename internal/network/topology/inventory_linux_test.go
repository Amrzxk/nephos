package topology

import (
	"net/netip"
	"testing"

	"github.com/Amrzxk/nephos/internal/model"
)

func TestENIOrphanCollectionRequiresExpectedSubnetAndVPC(t *testing.T) {
	vpcs := []model.VPC{
		{ID: "vpc-a", WorkspaceID: "default", ShortIndex: 1, CIDRBlock: netip.MustParsePrefix("10.0.0.0/16")},
		{ID: "vpc-b", WorkspaceID: "default", ShortIndex: 2, CIDRBlock: netip.MustParsePrefix("10.1.0.0/16")},
	}
	subnets := []model.Subnet{{ID: "subnet-a", VPCID: "vpc-a", WorkspaceID: "default", CIDRBlock: netip.MustParsePrefix("10.0.1.0/24")}}
	eni := model.ENI{ID: "eni-a", SubnetID: "subnet-a", WorkspaceID: "default", ShortIndex: 3, PrivateIP: netip.MustParseAddr("10.0.1.4"), MACAddress: "02:00:00:00:00:01"}
	for _, tc := range []struct {
		name, namespace string
		eni             model.ENI
		wantErr         bool
	}{
		{"correct ownership", "nx-vpc-1", eni, false},
		{"same ID wrong VPC", "nx-vpc-2", eni, true},
		{"missing namespace owner", "nx-vpc-3", eni, true},
		{"missing subnet owner", "nx-vpc-1", model.ENI{ID: eni.ID, SubnetID: "absent", WorkspaceID: "default"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owners, err := desiredENIOwners(vpcs, subnets, []model.ENI{tc.eni})
			if err == nil && owners[tc.eni.ID].namespace != tc.namespace {
				err = errWrongENIScope
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("owners=%+v err=%v", owners, err)
			}
		})
	}
}
