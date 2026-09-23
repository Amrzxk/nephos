package service

import (
	"bytes"
	"errors"
	"io"
	"net/netip"
	"regexp"
	"strings"
	"testing"
)

func TestParseCIDRCanonicalizesOnlySupportedIPv4Prefixes(t *testing.T) {
	for _, tc := range []struct {
		raw, want, code string
	}{
		{"10.0.1.7/24", "10.0.1.0/24", ""},
		{"10.10.0.0/16", "10.10.0.0/16", ""},
		{"10.0.1.15/28", "10.0.1.0/28", ""},
		{"10.0.0.0/15", "", "InvalidParameterValue"},
		{"10.0.0.0/29", "", "InvalidParameterValue"},
		{"2001:db8::/64", "", "InvalidParameterValue"},
		{"::ffff:10.0.0.0/112", "", "InvalidParameterValue"},
		{"garbage", "", "InvalidParameterValue"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ParseCIDR(tc.raw)
			if tc.code != "" {
				assertCode(t, err, tc.code, 400)
				return
			}
			if err != nil || got.String() != tc.want {
				t.Fatalf("ParseCIDR(%q)=%s, %v; want %s", tc.raw, got, err, tc.want)
			}
		})
	}
}

func TestValidateNameTrimsButPreservesUnicodeAndCase(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		ok        bool
	}{
		{"  東京 Lab  ", "東京 Lab", true},
		{"Lab East", "Lab East", true},
		{"lab east", "lab east", true},
		{strings.Repeat("界", 255), strings.Repeat("界", 255), true},
		{"", "", false},
		{"   ", "", false},
		{"Lab\x00East", "", false},
		{"Lab\u0080East", "", false},
		{string([]byte{0xff, 'A'}), "", false},
		{strings.Repeat("界", 256), "", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ValidateName(tc.raw)
			if !tc.ok {
				assertCode(t, err, "InvalidParameterValue", 400)
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ValidateName(%q)=%q, %v; want %q", tc.raw, got, err, tc.want)
			}
		})
	}
}

func TestSubnetRangeAndSiblingOverlap(t *testing.T) {
	vpc := netip.MustParsePrefix("10.0.0.0/16")
	if err := ValidateSubnetRange(vpc, netip.MustParsePrefix("10.0.1.7/24")); err != nil {
		t.Fatalf("contained subnet rejected: %v", err)
	}
	for _, subnet := range []string{"10.1.0.0/24", "10.0.0.0/15"} {
		assertCode(t, ValidateSubnetRange(vpc, netip.MustParsePrefix(subnet)), "InvalidSubnet.Range", 400)
	}
	if err := ValidateSubnetDisjoint(netip.MustParsePrefix("10.0.2.0/24"), []netip.Prefix{
		netip.MustParsePrefix("10.0.1.0/24"),
	}); err != nil {
		t.Fatalf("disjoint sibling rejected: %v", err)
	}
	assertCode(t, ValidateSubnetDisjoint(netip.MustParsePrefix("10.0.1.128/25"), []netip.Prefix{
		netip.MustParsePrefix("10.0.1.0/24"),
	}), "InvalidSubnet.Conflict", 409)
	if !Overlaps(netip.MustParsePrefix("10.0.1.0/24"), netip.MustParsePrefix("10.0.1.128/25")) {
		t.Fatal("nested prefixes did not overlap")
	}
	if Overlaps(netip.MustParsePrefix("10.0.1.0/24"), netip.MustParsePrefix("10.0.2.0/24")) {
		t.Fatal("disjoint prefixes overlapped")
	}
}

func TestSubnetGatewayAndReservedAddresses(t *testing.T) {
	prefix := netip.MustParsePrefix("10.0.1.0/24")
	if got := Gateway(prefix).String(); got != "10.0.1.1" {
		t.Fatalf("gateway=%s", got)
	}
	if got := firstAssignable(prefix).String(); got != "10.0.1.4" {
		t.Fatalf("first assignable=%s", got)
	}
	for _, tc := range []struct {
		ip       string
		reserved bool
	}{
		{"10.0.1.0", true},
		{"10.0.1.1", true},
		{"10.0.1.2", true},
		{"10.0.1.3", true},
		{"10.0.1.4", false},
		{"10.0.1.254", false},
		{"10.0.1.255", true},
		{"10.0.2.1", false},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			if got := reservedAddress(prefix, netip.MustParseAddr(tc.ip)); got != tc.reserved {
				t.Fatalf("%s reserved=%t, want %t", tc.ip, got, tc.reserved)
			}
		})
	}
}

func TestNewIDUsesSeventeenLowercaseHexDigits(t *testing.T) {
	id, err := NewID("vpc-", bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9}))
	if err != nil || id != "vpc-01020304050607080" {
		t.Fatalf("deterministic ID=%q, %v", id, err)
	}
	id, err = NewID("subnet-", nil)
	if err != nil || !regexp.MustCompile("^subnet-[0-9a-f]{17}$").MatchString(id) {
		t.Fatalf("random ID=%q, %v", id, err)
	}
	_, err = NewID("bad", bytes.NewReader(make([]byte, 9)))
	assertCode(t, err, "InvalidParameterValue", 400)
	_, err = NewID("vpc-", io.LimitReader(bytes.NewReader(nil), 0))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("short random source error=%v, want EOF", err)
	}
}

func assertCode(t *testing.T, err error, code string, status int) {
	t.Helper()
	var domain *Error
	if !errors.As(err, &domain) {
		t.Fatalf("error=%v, want domain error %s", err, code)
	}
	if domain.Code != code || domain.Status != status || domain.Message == "" {
		t.Fatalf("domain error=%+v, want code %s status %d", domain, code, status)
	}
}
