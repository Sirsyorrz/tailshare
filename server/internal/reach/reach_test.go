package reach

import (
	"net/netip"
	"testing"

	"tailscale.com/tailcfg"
)

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, x := range s {
		out = append(out, netip.MustParseAddr(x))
	}
	return out
}

func TestAllowed(t *testing.T) {
	self := addrs("100.101.1.1")
	friend := addrs("100.102.2.2")
	ports := func(ip string, first, last uint16) []tailcfg.NetPortRange {
		return []tailcfg.NetPortRange{{IP: ip, Ports: tailcfg.PortRange{First: first, Last: last}}}
	}
	ownOnly := []tailcfg.FilterRule{{SrcIPs: []string{"100.103.3.3"}, DstPorts: ports("*", 0, 65535)}}
	if Allowed(ownOnly, friend, self, 443) {
		t.Error("own-members rule should not admit friend")
	}
	cases := map[string][]tailcfg.FilterRule{
		"exact":    {{SrcIPs: []string{"100.102.2.2"}, DstPorts: ports("100.101.1.1", 443, 443)}},
		"cidr":     {{SrcIPs: []string{"100.64.0.0/10"}, DstPorts: ports("100.101.1.1/32", 80, 443)}},
		"range":    {{SrcIPs: []string{"100.102.2.0-100.102.2.9"}, DstPorts: ports("*", 443, 443)}},
		"wildcard": {{SrcIPs: []string{"*"}, DstPorts: ports("*", 0, 65535)}},
	}
	for name, rules := range cases {
		if !Allowed(rules, friend, self, 443) {
			t.Errorf("%s: should allow", name)
		}
	}
	wrongPort := []tailcfg.FilterRule{{SrcIPs: []string{"*"}, DstPorts: ports("*", 80, 80)}}
	if Allowed(wrongPort, friend, self, 443) {
		t.Error("port 80 rule should not allow 443")
	}
	udpOnly := []tailcfg.FilterRule{{SrcIPs: []string{"*"}, DstPorts: ports("*", 443, 443), IPProto: []int{17}}}
	if Allowed(udpOnly, friend, self, 443) {
		t.Error("udp-only rule should not allow tcp")
	}
}
