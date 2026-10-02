package service

import (
	"errors"
	"fmt"
	"net/netip"
)

var errAddressExhausted = errors.New("subnet has no free private addresses")

// nextPrivateIP is deterministic: reserve the first four and final IPv4 address.
func nextPrivateIP(prefix netip.Prefix, used map[netip.Addr]struct{}) (netip.Addr, error) {
	if !prefix.IsValid() || !prefix.Addr().Is4() || prefix.Bits() < 16 || prefix.Bits() > 28 || prefix != prefix.Masked() {
		return netip.Addr{}, fmt.Errorf("IPAM requires a canonical IPv4 /16 through /28 subnet")
	}
	base := prefix.Addr()
	first := base.Next().Next().Next().Next()
	octets := base.As4()
	last := uint32(octets[0])<<24 | uint32(octets[1])<<16 | uint32(octets[2])<<8 | uint32(octets[3])
	last |= uint32(1)<<(32-prefix.Bits()) - 1
	broadcast := netip.AddrFrom4([4]byte{byte(last >> 24), byte(last >> 16), byte(last >> 8), byte(last)})
	for ip := first; ip != broadcast; ip = ip.Next() {
		if _, exists := used[ip]; !exists {
			return ip, nil
		}
	}
	return netip.Addr{}, errAddressExhausted
}
