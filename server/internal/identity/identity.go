package identity

import (
	"context"
	"errors"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/tailcfg"
)

type User struct {
	ID      int64  `json:"id"`
	Login   string `json:"login"`
	Display string `json:"display"`
	Node    string `json:"node"`
	// External is true for devices of users outside this tailnet who reach
	// the node through a share. They can never use admin features.
	External bool `json:"external"`
}

var ErrNoUser = errors.New("connection has no Tailscale user")

type Resolver interface {
	Resolve(ctx context.Context, remoteAddr string) (User, error)
}

type Tailscale struct{ LC *local.Client }

func (t Tailscale) Resolve(ctx context.Context, remoteAddr string) (User, error) {
	w, err := t.LC.WhoIs(ctx, remoteAddr)
	if err != nil {
		return User{}, err
	}
	// Tagged nodes act for the tailnet, not a person, so they get no
	// per-user permissions.
	if w.Node == nil || w.UserProfile == nil || w.Node.IsTagged() {
		return User{}, ErrNoUser
	}
	return User{
		ID:       int64(w.UserProfile.ID),
		Login:    w.UserProfile.LoginName,
		Display:  w.UserProfile.DisplayName,
		Node:     nodeName(w.Node),
		External: w.Node.Hostinfo.Valid() && w.Node.Hostinfo.ShareeNode(),
	}, nil
}

// nodeName falls back for devices shared in from another tailnet, which
// arrive without a MagicDNS name.
func nodeName(n *tailcfg.Node) string {
	if name := strings.TrimSuffix(n.Name, "."); name != "" {
		return name
	}
	if h := n.Hostinfo.Hostname(); n.Hostinfo.Valid() && h != "" {
		return h
	}
	if len(n.Addresses) > 0 {
		return n.Addresses[0].Addr().String()
	}
	return "unknown"
}

// Fixed is for -dev mode only, which binds to loopback.
type Fixed struct{ User User }

func (f Fixed) Resolve(context.Context, string) (User, error) { return f.User, nil }
