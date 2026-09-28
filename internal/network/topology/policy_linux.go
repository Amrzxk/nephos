package topology

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

// netlink v1.3.1 RuleList does not populate Rule.Type from RtMsg.Type. Read
// that one field from the raw kernel reply so an ordinary lookup rule cannot
// masquerade as our fail-closed blackhole at priority 32000.
type kernelPolicyRule struct {
	priority int
	table    int
	action   uint8
	dst      *net.IPNet
	extra    []string
}

func listKernelPolicyRules() ([]kernelPolicyRule, error) {
	req := nl.NewNetlinkRequest(unix.RTM_GETRULE, unix.NLM_F_DUMP|unix.NLM_F_REQUEST)
	req.AddData(nl.NewIfInfomsg(netlink.FAMILY_V4))
	messages, err := req.Execute(unix.NETLINK_ROUTE, unix.RTM_NEWRULE)
	if err != nil {
		return nil, fmt.Errorf("query IPv4 policy rules: %w", err)
	}
	rules := make([]kernelPolicyRule, 0, len(messages))
	for _, raw := range messages {
		if len(raw) < 12 {
			return nil, fmt.Errorf("short kernel policy-rule reply")
		}
		msg := nl.DeserializeRtMsg(raw)
		attrs, err := nl.ParseRouteAttr(raw[msg.Len():])
		if err != nil {
			return nil, fmt.Errorf("decode kernel policy-rule attributes: %w", err)
		}
		rule := kernelPolicyRule{table: int(msg.Table), action: msg.Type}
		for _, attr := range attrs {
			switch attr.Attr.Type {
			case nl.FRA_PRIORITY:
				if len(attr.Value) != 4 {
					return nil, fmt.Errorf("invalid policy-rule priority length %d", len(attr.Value))
				}
				rule.priority = int(nl.NativeEndian().Uint32(attr.Value))
			case nl.FRA_TABLE:
				if len(attr.Value) != 4 {
					return nil, fmt.Errorf("invalid policy-rule table length %d", len(attr.Value))
				}
				rule.table = int(nl.NativeEndian().Uint32(attr.Value))
			case nl.FRA_DST:
				if len(attr.Value) != 4 {
					return nil, fmt.Errorf("invalid IPv4 policy-rule destination length %d", len(attr.Value))
				}
				rule.dst = &net.IPNet{
					IP: net.IP(attr.Value), Mask: net.CIDRMask(int(msg.Dst_len), 32),
				}
			case nl.FRA_SUPPRESS_PREFIXLEN:
				// The kernel returns 0xffffffff for "not set".
				if len(attr.Value) != 4 || nl.NativeEndian().Uint32(attr.Value) != ^uint32(0) {
					rule.extra = append(rule.extra, fmt.Sprintf("%d:%x", attr.Attr.Type, attr.Value))
				}
			case nl.FRA_PROTOCOL:
				// Zero and BOOT are metadata defaults, not packet selectors.
				if len(attr.Value) != 1 || (attr.Value[0] != 0 && attr.Value[0] != unix.RTPROT_BOOT) {
					rule.extra = append(rule.extra, fmt.Sprintf("%d:%x", attr.Attr.Type, attr.Value))
				}
			default:
				rule.extra = append(rule.extra, fmt.Sprintf("%d:%x", attr.Attr.Type, attr.Value))
			}
		}
		rules = append(rules, rule)
	}
	return rules, nil
}
