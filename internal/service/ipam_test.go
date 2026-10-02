package service

import (
	"net/netip"
	"testing"
)

func TestIPAMReservedAndExhausted(t *testing.T) {
	for _, tc := range []struct {
		prefix      string
		first, last byte
	}{
		{"10.0.1.0/28", 4, 14}, {"192.168.0.0/24", 4, 254},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			prefix := netip.MustParsePrefix(tc.prefix)
			used := make(map[netip.Addr]struct{})
			base := prefix.Addr().As4()
			for last := int(tc.first); last <= int(tc.last); last++ {
				want := base
				want[3] = byte(last)
				got, err := nextPrivateIP(prefix, used)
				if err != nil || got != netip.AddrFrom4(want) {
					t.Fatalf("IP=%v error=%v want=%v", got, err, netip.AddrFrom4(want))
				}
				used[got] = struct{}{}
			}
			if _, err := nextPrivateIP(prefix, used); err == nil {
				t.Fatal("expected exhaustion")
			}
		})
	}
	for _, raw := range []string{"::/64", "10.0.1.0/32", "10.0.1.1/28"} {
		if _, err := nextPrivateIP(netip.MustParsePrefix(raw), nil); err == nil {
			t.Errorf("invalid allocation prefix %s accepted", raw)
		}
	}
}

func TestIPAMChoosesFirstFreeHole(t *testing.T) {
	prefix := netip.MustParsePrefix("10.0.1.0/28")
	used := map[netip.Addr]struct{}{
		netip.MustParseAddr("10.0.1.1"): {},
		netip.MustParseAddr("10.0.1.4"): {},
		netip.MustParseAddr("10.0.1.6"): {},
	}
	got, err := nextPrivateIP(prefix, used)
	if err != nil || got != netip.MustParseAddr("10.0.1.5") {
		t.Fatalf("first free hole=%v err=%v", got, err)
	}
}
