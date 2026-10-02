package topology

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/Amrzxk/nephos/internal/model"
)

func TestENILinkNames(t *testing.T) {
	for _, tc := range []struct {
		index int64
		want  string
	}{{1, "ve1"}, {42, "ve42"}, {9999999999999, "ve9999999999999"}} {
		got, err := eniLinkName(tc.index)
		if err != nil || got != tc.want || len(got) > 15 {
			t.Fatalf("name(%d)=%q err=%v", tc.index, got, err)
		}
	}
	for _, index := range []int64{-1, 0, 10000000000000} {
		if _, err := eniLinkName(index); err == nil {
			t.Errorf("oversized/foreign index %d accepted", index)
		}
	}
}

func TestENIForeignLinkAlias(t *testing.T) {
	eni := model.ENI{ID: "eni-00000000000000001", PrivateIP: netip.MustParseAddr("10.0.1.4"), MACAddress: "02:00:00:00:00:01"}
	alias := eniAlias(eni, 1234)
	record, err := parseENIAlias(alias)
	if err != nil || record.ID != eni.ID || record.IP != eni.PrivateIP || record.MAC != eni.MACAddress || record.Namespace != 1234 {
		t.Fatalf("alias=%q record=%+v err=%v", alias, record, err)
	}
	for _, raw := range []string{"", "foreign", strings.Replace(alias, "10.0.1.4", "::1", 1), strings.Replace(alias, eni.ID, "../foreign", 1)} {
		if _, err := parseENIAlias(raw); err == nil {
			t.Errorf("foreign alias %q accepted", raw)
		}
	}
}

func TestENIRejectsInvalidTargetBeforeKernelMutation(t *testing.T) {
	engine := New()
	if err := engine.EnsureENI(context.Background(), model.VPC{}, model.Subnet{}, model.ENI{}, nil); err == nil {
		t.Fatal("invalid ENI accepted")
	}
}

func TestENISourceCheckRendering(t *testing.T) {
	records := map[string]eniIdentity{
		"ve2": {ID: "eni-00000000000000002", IP: netip.MustParseAddr("10.0.2.4")},
		"ve1": {ID: "eni-00000000000000001", IP: netip.MustParseAddr("10.0.1.4")},
	}
	got, err := renderENISourceChecks(records, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"delete table inet nephos", "table inet nephos", "comment \"nephos:m1\"",
		"type ifname . ipv4_addr", "\"ve1\" . 10.0.1.4", "\"ve2\" . 10.0.2.4",
		"hook prerouting", "iifname . ip saddr @eni_sources", "counter drop",
	}
	for _, fragment := range want {
		if !strings.Contains(got, fragment) {
			t.Errorf("missing %q in %s", fragment, got)
		}
	}
	if strings.Index(got, "\"ve1\" .") > strings.Index(got, "\"ve2\" .") {
		t.Fatal("ruleset not deterministic")
	}
	records["foreign"] = eniIdentity{IP: netip.MustParseAddr("10.0.1.5")}
	if _, err := renderENISourceChecks(records, false); err == nil {
		t.Fatal("foreign source interface accepted")
	}
}
