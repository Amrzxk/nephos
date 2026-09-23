package service

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ParseCIDR accepts M1 IPv4 VPC/subnet ranges and masks host bits, matching
// the behavior learners see when specifying an AWS-style CIDR.
func ParseCIDR(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, invalidParameter(fmt.Sprintf("invalid CIDR block %q", raw))
	}
	if !prefix.Addr().Is4() || prefix.Bits() < 16 || prefix.Bits() > 28 {
		return netip.Prefix{}, invalidParameter("CIDR block must be IPv4 with a prefix length from /16 to /28")
	}
	return prefix.Masked(), nil
}

// ValidateName returns the normalized, case-sensitive resource name. The
// length limit counts Unicode code points, not UTF-8 bytes.
func ValidateName(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", invalidParameter("name must be valid UTF-8")
	}
	name := strings.TrimSpace(raw)
	count := utf8.RuneCountInString(name)
	if count < 1 || count > 255 {
		return "", invalidParameter("name must have 1 to 255 Unicode characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", invalidParameter("name must not contain control characters")
		}
	}
	return name, nil
}

// ValidateSubnetRange requires a subnet to fit entirely inside its VPC.
func ValidateSubnetRange(vpc, subnet netip.Prefix) error {
	if !vpc.IsValid() || !subnet.IsValid() ||
		!vpc.Addr().Is4() || !subnet.Addr().Is4() ||
		vpc.Bits() > subnet.Bits() || !vpc.Masked().Contains(subnet.Masked().Addr()) {
		return &Error{
			Code: "InvalidSubnet.Range", Message: "subnet CIDR must be contained in the VPC CIDR", Status: 400,
		}
	}
	return nil
}

// ValidateSubnetDisjoint rejects overlap only among siblings in one VPC.
// Different VPCs may use identical CIDRs because they have separate namespaces.
func ValidateSubnetDisjoint(subnet netip.Prefix, siblings []netip.Prefix) error {
	for _, sibling := range siblings {
		if Overlaps(subnet, sibling) {
			return &Error{
				Code: "InvalidSubnet.Conflict", Message: "subnet CIDR overlaps an existing subnet", Status: 409,
			}
		}
	}
	return nil
}

// Overlaps reports whether two valid IPv4 prefixes share at least one address.
func Overlaps(a, b netip.Prefix) bool {
	if !a.IsValid() || !b.IsValid() || !a.Addr().Is4() || !b.Addr().Is4() {
		return false
	}
	a, b = a.Masked(), b.Masked()
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

// Gateway is the subnet's base+1 router address.
func Gateway(prefix netip.Prefix) netip.Addr {
	if !prefix.IsValid() || !prefix.Addr().Is4() {
		return netip.Addr{}
	}
	return prefix.Masked().Addr().Next()
}

func firstAssignable(prefix netip.Prefix) netip.Addr {
	if !prefix.IsValid() || !prefix.Addr().Is4() {
		return netip.Addr{}
	}
	addr := prefix.Masked().Addr()
	for range 4 {
		addr = addr.Next()
	}
	return addr
}

func reservedAddress(prefix netip.Prefix, addr netip.Addr) bool {
	if !prefix.IsValid() || !prefix.Addr().Is4() || prefix.Bits() < 16 || prefix.Bits() > 28 ||
		!addr.Is4() || !prefix.Contains(addr) {
		return false
	}
	base := prefix.Masked().Addr().As4()
	ip := addr.As4()
	offset := binary.BigEndian.Uint32(ip[:]) - binary.BigEndian.Uint32(base[:])
	last := uint32(1)<<uint(32-prefix.Bits()) - 1
	return offset <= 3 || offset == last
}
