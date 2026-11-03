// Package reach works out, from the access rules Tailscale pushes to this
// node, which peers are allowed to connect to it. No API key is needed: a
// node always knows the rules that apply to traffic arriving at it.
package reach

import (
	"context"
	"net/netip"
	"slices"
	"strings"

	"go4.org/netipx"
	"tailscale.com/client/local"
	"tailscale.com/tailcfg"
)

type Peer struct {
	UserID   int64    `json:"userId"`
	Login    string   `json:"login"`
	Node     string   `json:"node"`
	IPs      []string `json:"ips"`
	Online   bool     `json:"online"`
	External bool     `json:"external"`
	CanReach bool     `json:"canReach"`
}

const tcp = 6

// Check reports every peer and whether it may open TCP connections to port
// on this node.
func Check(ctx context.Context, lc *local.Client, port uint16) ([]Peer, error) {
	st, err := lc.Status(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := lc.DebugPacketFilterRules(ctx)
	if err != nil {
		return nil, err
	}
	var self []netip.Addr
	if st.Self != nil {
		self = st.Self.TailscaleIPs
	}
	out := []Peer{}
	for _, p := range st.Peer {
		if p.Tags != nil && p.Tags.Len() > 0 {
			continue
		}
		u := st.User[p.UserID]
		name := strings.TrimSuffix(p.DNSName, ".")
		if name == "" {
			name = p.HostName
		}
		peer := Peer{
			UserID:   int64(p.UserID),
			Login:    u.LoginName,
			Node:     name,
			Online:   p.Online,
			External: p.ShareeNode,
			CanReach: Allowed(rules, p.TailscaleIPs, self, port),
		}
		for _, ip := range p.TailscaleIPs {
			peer.IPs = append(peer.IPs, ip.String())
		}
		out = append(out, peer)
	}
	slices.SortFunc(out, func(a, b Peer) int { return strings.Compare(a.Login+a.Node, b.Login+b.Node) })
	return out, nil
}

// Allowed reports whether any rule lets a source address reach a
// destination address on port over TCP.
func Allowed(rules []tailcfg.FilterRule, srcs, dsts []netip.Addr, port uint16) bool {
	for _, r := range rules {
		if len(r.IPProto) > 0 && !slices.Contains(r.IPProto, tcp) {
			continue
		}
		if !anyMatch(r.SrcIPs, srcs) {
			continue
		}
		for _, d := range r.DstPorts {
			if d.Ports.First <= port && port <= d.Ports.Last && anyMatch([]string{d.IP}, dsts) {
				return true
			}
		}
	}
	return false
}

func anyMatch(patterns []string, addrs []netip.Addr) bool {
	for _, pat := range patterns {
		for _, a := range addrs {
			if match(pat, a) {
				return true
			}
		}
	}
	return false
}

// match handles the address forms FilterRule uses: "*", an IP, a CIDR, or
// an "a-b" range.
func match(pat string, a netip.Addr) bool {
	switch {
	case pat == "*":
		return true
	case strings.Contains(pat, "/"):
		p, err := netip.ParsePrefix(pat)
		return err == nil && p.Contains(a)
	case strings.Contains(pat, "-"):
		r, err := netipx.ParseIPRange(pat)
		return err == nil && r.Contains(a)
	default:
		ip, err := netip.ParseAddr(pat)
		return err == nil && ip == a
	}
}
