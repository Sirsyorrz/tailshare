package identity

import (
	"context"
	"errors"
	"strings"

	"tailscale.com/client/local"
)

type User struct {
	ID      int64  `json:"id"`
	Login   string `json:"login"`
	Display string `json:"display"`
	Node    string `json:"node"`
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
		ID:      int64(w.UserProfile.ID),
		Login:   w.UserProfile.LoginName,
		Display: w.UserProfile.DisplayName,
		Node:    strings.TrimSuffix(w.Node.Name, "."),
	}, nil
}

// Fixed is for -dev mode only, which binds to loopback.
type Fixed struct{ User User }

func (f Fixed) Resolve(context.Context, string) (User, error) { return f.User, nil }
