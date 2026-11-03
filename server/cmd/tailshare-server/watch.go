package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
)

// Once serving, tsnet stops watching its own login state, so a node deleted
// or logged out in the admin console, an expired key, a rename or HTTPS being
// switched off would all go unnoticed while the app kept saying "Hosting".
// nodeWatch notices, tells the app, and starts a new sign-in when needed.

const (
	keyExpiryWarning = 14 * 24 * time.Hour
	offlineWarning   = 90 * time.Second
	recheckEvery     = 30 * time.Second
)

type nodeWatch struct {
	status     func(context.Context) (*ipnstate.Status, error)
	startLogin func(context.Context) error
	// renamed is called with the node's new name, before "serving" is sent.
	renamed func(fqdn string)
	emit    func(kind string, fields map[string]string)
	now     func() time.Time

	fqdn       string
	phase      string
	loginURL   string
	loginAsked bool
	warning    string
	notSince   time.Time
}

func newNodeWatch(lc *local.Client, fqdn string, renamed func(string)) *nodeWatch {
	return &nodeWatch{
		status:     lc.Status,
		startLogin: lc.StartLoginInteractive,
		renamed:    renamed,
		emit:       event,
		now:        time.Now,
		fqdn:       fqdn,
		phase:      "running",
	}
}

func (w *nodeWatch) run(ctx context.Context, lc *local.Client) {
	kick := make(chan struct{}, 1)
	go func() {
		for ctx.Err() == nil {
			watchBus(ctx, lc, kick)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}()
	t := time.NewTicker(recheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
			// Let a burst of notifications settle into one check.
			time.Sleep(500 * time.Millisecond)
		}
		w.check(ctx)
	}
}

func watchBus(ctx context.Context, lc *local.Client, kick chan<- struct{}) {
	bus, err := lc.WatchIPNBus(ctx, 0)
	if err != nil {
		return
	}
	defer bus.Close()
	for {
		n, err := bus.Next()
		if err != nil {
			return
		}
		if n.State != nil || n.SelfChange != nil || n.BrowseToURL != nil || n.ErrMessage != nil || n.LoginFinished != nil {
			select {
			case kick <- struct{}{}:
			default:
			}
		}
	}
}

func (w *nodeWatch) check(ctx context.Context) {
	st, err := w.status(ctx)
	if err != nil {
		log.Printf("checking Tailscale state: %v", err)
		return
	}
	now := w.now()
	var expiry *time.Time
	if st.Self != nil {
		expiry = st.Self.KeyExpiry
	}

	switch st.BackendState {
	case ipn.Running.String():
		w.notSince = time.Time{}
		w.loginAsked = false
		fqdn := ""
		if st.Self != nil {
			fqdn = strings.TrimSuffix(st.Self.DNSName, ".")
		}
		renamed := fqdn != "" && fqdn != w.fqdn
		if renamed {
			log.Printf("machine name is now %s (was %s)", fqdn, w.fqdn)
			w.fqdn = fqdn
			w.renamed(fqdn)
		}
		if w.phase != "running" || renamed {
			if w.phase == "login" || w.phase == "approval" {
				log.Print("signed in again, serving")
			}
			w.phase, w.loginURL = "running", ""
			w.emit("serving", map[string]string{"url": "https://" + w.fqdn})
		}
		w.warn(runningWarning(st, expiry, now))

	case ipn.NeedsLogin.String():
		w.notSince = time.Time{}
		reason := "This machine was removed or signed out in the Tailscale admin console, so nobody can reach it. Sign in again to bring it back."
		if expiry != nil && !expiry.IsZero() && expiry.Before(now) {
			reason = "This machine's Tailscale key expired, so nobody can reach it. Sign in again to keep hosting."
		}
		if st.AuthURL == "" && !w.loginAsked {
			w.loginAsked = true
			log.Print(reason)
			if err := w.startLogin(ctx); err != nil {
				log.Printf("starting sign-in: %v", err)
			}
			// The URL arrives a moment later; the bus wakes us for it.
		}
		if w.phase != "login" || w.loginURL != st.AuthURL {
			w.phase, w.loginURL = "login", st.AuthURL
			if st.AuthURL != "" {
				log.Printf("sign in again at: %s", st.AuthURL)
			}
			w.emit("login", map[string]string{"url": st.AuthURL, "reason": reason})
		}
		w.warn("")

	case ipn.NeedsMachineAuth.String():
		w.notSince = time.Time{}
		if w.phase != "approval" {
			w.phase = "approval"
			msg := "Waiting for a tailnet admin to approve this machine in the Tailscale admin console (Machines page)."
			log.Print(msg)
			w.emit("approval", map[string]string{"reason": msg})
		}
		w.warn("")

	default:
		// Starting means it lost its connection to Tailscale (network down,
		// coordination server unreachable). Brief blips are normal.
		if w.notSince.IsZero() {
			w.notSince = now
		}
		if now.Sub(w.notSince) >= offlineWarning {
			w.warn(fmt.Sprintf("Not connected to Tailscale for %d min (%s); friends can't reach it right now.",
				int(now.Sub(w.notSince).Minutes()), st.BackendState))
		}
	}
}

func runningWarning(st *ipnstate.Status, expiry *time.Time, now time.Time) string {
	if len(st.CertDomains) == 0 {
		return "HTTPS certificates are turned off for this tailnet (admin console, DNS page), so browsers can't connect."
	}
	if expiry != nil && !expiry.IsZero() && expiry.Sub(now) < keyExpiryWarning {
		return fmt.Sprintf("This machine's Tailscale key expires %s. Turn off key expiry for it in the admin console (Machines, ... menu, Disable key expiry) to avoid signing in again.",
			expiry.Local().Format("Mon 2 Jan 15:04"))
	}
	return ""
}

func (w *nodeWatch) warn(msg string) {
	if msg == w.warning {
		return
	}
	w.warning = msg
	if msg != "" {
		log.Print("warning: " + msg)
	}
	w.emit("warning", map[string]string{"message": msg})
}
