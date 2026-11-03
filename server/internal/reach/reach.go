// Package reach works out, from the access rules Tailscale pushes to this
// node, which peers are allowed to connect to it. No API key is needed: a
// node always knows the rules that apply to traffic arriving at it.
package reach

import (
	"context"
	"encoding/json"
	"log"
	"net/netip"
	"slices"
	"strings"

	"go4.org/netipx"
	"tailscale.com/client/local"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
	"tailscale.com/wgengine/filter"
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

type Report struct {
	// Self is this node's Tailscale addresses.
	Self  []string
	Peers []Peer
	// Rules as received, for diagnosing a wrong answer.
	Rules []tailcfg.FilterRule
}

// Check reports every peer and whether it may open TCP connections to port
// on this node, by running Tailscale's own packet filter over the rules this
// node was given. Rules can name sources by node capability ("cap:..."),
// which is how some sharing access arrives, so a plain IP match isn't enough.
func Check(ctx context.Context, lc *local.Client, port uint16) (Report, error) {
	st, err := lc.Status(ctx)
	if err != nil {
		return Report{}, err
	}
	rules, err := lc.DebugPacketFilterRules(ctx)
	if err != nil {
		return Report{}, err
	}
	var self []netip.Addr
	if st.Self != nil {
		self = st.Self.TailscaleIPs
	}
	caps := map[netip.Addr]tailcfg.NodeCapMap{}
	for _, p := range st.Peer {
		for _, ip := range p.TailscaleIPs {
			caps[ip] = p.CapMap
		}
	}
	f, err := newFilter(rules, self, caps)
	if err != nil {
		return Report{}, err
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
			CanReach: f.allowed(p.TailscaleIPs, self, port),
		}
		for _, ip := range p.TailscaleIPs {
			peer.IPs = append(peer.IPs, ip.String())
		}
		out = append(out, peer)
	}
	slices.SortFunc(out, func(a, b Peer) int { return strings.Compare(a.Login+a.Node, b.Login+b.Node) })
	rep := Report{Peers: out, Rules: rules}
	for _, ip := range self {
		rep.Self = append(rep.Self, ip.String())
	}
	return rep, nil
}

type pfilter struct{ f *filter.Filter }

func newFilter(rules []tailcfg.FilterRule, self []netip.Addr, caps map[netip.Addr]tailcfg.NodeCapMap) (pfilter, error) {
	matches, err := filter.MatchesFromFilterRules(rules)
	if err != nil {
		// Partial results are still usable; unknown rule forms are skipped
		// by the real filter too.
		log.Printf("policy check: %v", err)
	}
	var b netipx.IPSetBuilder
	for _, a := range self {
		b.Add(a)
	}
	local, err := b.IPSet()
	if err != nil {
		return pfilter{}, err
	}
	hasCap := func(src netip.Addr, c tailcfg.NodeCapability) bool {
		return caps[src].Contains(c)
	}
	return pfilter{filter.New(matches, hasCap, local, nil, nil, logger.Discard)}, nil
}

func (p pfilter) allowed(srcs, dsts []netip.Addr, port uint16) bool {
	for _, s := range srcs {
		for _, d := range dsts {
			if s.Is4() == d.Is4() && p.f.CheckTCP(s, d, port) == filter.Accept {
				return true
			}
		}
	}
	return false
}

// Allowed reports whether rules let any source reach any destination on
// port over TCP.
func Allowed(rules []tailcfg.FilterRule, srcs, dsts []netip.Addr, port uint16) bool {
	f, err := newFilter(rules, dsts, nil)
	return err == nil && f.allowed(srcs, dsts, port)
}

// Grant is the policy-file grant that lets people a node is shared with
// reach port 443 on it. Policy destinations must be addresses, tags or host
// aliases; a machine name is rejected by the policy editor.
func Grant(selfIPs []string) string {
	if len(selfIPs) == 0 {
		selfIPs = []string{"<this machine's Tailscale IP>"}
	}
	dst, _ := json.Marshal(selfIPs)
	return `{"src": ["autogroup:shared"], "dst": ` + strings.ReplaceAll(string(dst), ",", ", ") + `, "ip": ["tcp:443"]}`
}
