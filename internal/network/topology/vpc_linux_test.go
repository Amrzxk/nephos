package topology

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/Amrzxk/nephos/internal/model"
)

func TestDesiredGatewaysAreSubnetBasePlusOne(t *testing.T) {
	vpc := model.VPC{ID: "vpc-test", CIDRBlock: netip.MustParsePrefix("10.0.0.0/16")}
	subnets := []model.Subnet{
		{VPCID: vpc.ID, CIDRBlock: netip.MustParsePrefix("10.0.2.0/24")},
		{VPCID: vpc.ID, CIDRBlock: netip.MustParsePrefix("10.0.1.0/28")},
	}
	got, err := desiredGateways(vpc, subnets)
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr("10.0.1.1"), netip.MustParseAddr("10.0.2.1")}
	if !slices.Equal(got, want) {
		t.Fatalf("desired gateways=%v, want %v", got, want)
	}
}

func TestDesiredGatewaysRejectInvalidRelationship(t *testing.T) {
	vpc := model.VPC{ID: "vpc-test", CIDRBlock: netip.MustParsePrefix("10.0.0.0/16")}
	tests := []model.Subnet{
		{VPCID: "vpc-other", CIDRBlock: netip.MustParsePrefix("10.0.1.0/24")},
		{VPCID: vpc.ID, CIDRBlock: netip.MustParsePrefix("10.1.1.0/24")},
		{VPCID: vpc.ID, CIDRBlock: netip.MustParsePrefix("2001:db8::/64")},
	}
	for _, subnet := range tests {
		if _, err := desiredGateways(vpc, []model.Subnet{subnet}); err == nil {
			t.Errorf("accepted invalid subnet %+v", subnet)
		}
	}
	if _, err := desiredGateways(vpc, []model.Subnet{
		{VPCID: vpc.ID, CIDRBlock: netip.MustParsePrefix("10.0.1.0/24")},
		{VPCID: vpc.ID, CIDRBlock: netip.MustParsePrefix("10.0.1.128/25")},
	}); err == nil {
		t.Fatal("accepted overlapping subnets")
	}
}
