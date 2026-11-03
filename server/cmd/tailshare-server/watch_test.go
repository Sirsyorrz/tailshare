package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"tailscale.com/ipn/ipnstate"
)

type fakeNode struct {
	st      ipnstate.Status
	logins  int
	renamed []string
	events  []string
	now     time.Time
}

func (f *fakeNode) watch() *nodeWatch {
	return &nodeWatch{
		status:     func(context.Context) (*ipnstate.Status, error) { s := f.st; return &s, nil },
		startLogin: func(context.Context) error { f.logins++; return nil },
		renamed:    func(n string) { f.renamed = append(f.renamed, n) },
		emit: func(kind string, fields map[string]string) {
			e := kind
			for _, k := range []string{"url", "reason", "message"} {
				if v, ok := fields[k]; ok {
					e += " " + k + "=" + v
				}
			}
			f.events = append(f.events, e)
		},
		now:   func() time.Time { return f.now },
		fqdn:  "share.example.ts.net",
		phase: "running",
	}
}

func (f *fakeNode) take() []string {
	e := f.events
	f.events = nil
	return e
}

func running(f *fakeNode, name string, expiry time.Time) {
	f.st = ipnstate.Status{
		BackendState: "Running",
		CertDomains:  []string{name},
		Self:         &ipnstate.PeerStatus{DNSName: name + ".", KeyExpiry: &expiry},
	}
}

func TestWatchRemovedAndSignedInAgain(t *testing.T) {
	f := &fakeNode{now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	far := f.now.Add(180 * 24 * time.Hour)
	running(f, "share.example.ts.net", far)
	w := f.watch()
	ctx := context.Background()

	w.check(ctx)
	if e := f.take(); len(e) != 0 {
		t.Fatalf("healthy node emitted %v", e)
	}

	// Deleted in the admin console: control rejects the key.
	f.st = ipnstate.Status{BackendState: "NeedsLogin", Self: &ipnstate.PeerStatus{}}
	w.check(ctx)
	e := f.take()
	if f.logins != 1 || len(e) != 1 || !strings.HasPrefix(e[0], "login url= reason=This machine was removed") {
		t.Fatalf("after removal: logins=%d %v", f.logins, e)
	}
	w.check(ctx)
	if f.logins != 1 || len(f.take()) != 0 {
		t.Fatal("sign-in started twice or duplicate event")
	}
	f.st.AuthURL = "https://login.tailscale.com/a/abc"
	w.check(ctx)
	if e := f.take(); len(e) != 1 || !strings.HasPrefix(e[0], "login url=https://login.tailscale.com/a/abc") {
		t.Fatalf("url: %v", e)
	}

	// Approved again; the old name was taken meanwhile so it got a suffix.
	running(f, "share-1.example.ts.net", far)
	w.check(ctx)
	e = f.take()
	if len(e) != 1 || e[0] != "serving url=https://share-1.example.ts.net" {
		t.Fatalf("back: %v", e)
	}
	if len(f.renamed) != 1 || f.renamed[0] != "share-1.example.ts.net" {
		t.Fatalf("renamed: %v", f.renamed)
	}

	// A second removal starts a second sign-in.
	f.st = ipnstate.Status{BackendState: "NeedsLogin"}
	w.check(ctx)
	if f.logins != 2 {
		t.Fatalf("logins %d", f.logins)
	}
}

func TestWatchKeyExpiry(t *testing.T) {
	f := &fakeNode{now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	running(f, "share.example.ts.net", f.now.Add(3*24*time.Hour))
	w := f.watch()
	w.check(context.Background())
	if e := f.take(); len(e) != 1 || !strings.Contains(e[0], "key expires") {
		t.Fatalf("soon: %v", e)
	}
	exp := f.now.Add(-time.Hour)
	f.st = ipnstate.Status{BackendState: "NeedsLogin", Self: &ipnstate.PeerStatus{KeyExpiry: &exp}}
	w.check(context.Background())
	e := f.take()
	if len(e) != 2 || !strings.Contains(e[0], "key expired") || e[1] != "warning message=" {
		t.Fatalf("expired: %v", e)
	}
	// Expiry turned off in the admin console: no warning once back.
	f.st = ipnstate.Status{BackendState: "Running", CertDomains: []string{"x"}, Self: &ipnstate.PeerStatus{DNSName: "share.example.ts.net."}}
	w.check(context.Background())
	if e := f.take(); len(e) != 1 || !strings.HasPrefix(e[0], "serving") {
		t.Fatalf("back: %v", e)
	}
}

func TestWatchApprovalHTTPSAndOffline(t *testing.T) {
	f := &fakeNode{now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	w := f.watch()
	ctx := context.Background()

	f.st = ipnstate.Status{BackendState: "NeedsMachineAuth"}
	w.check(ctx)
	if e := f.take(); len(e) != 1 || !strings.HasPrefix(e[0], "approval reason=") || f.logins != 0 {
		t.Fatalf("approval: %v", e)
	}

	f.st = ipnstate.Status{BackendState: "Running", Self: &ipnstate.PeerStatus{DNSName: "share.example.ts.net."}}
	w.check(ctx)
	if e := f.take(); len(e) != 2 || !strings.HasPrefix(e[0], "serving") || !strings.Contains(e[1], "HTTPS certificates are turned off") {
		t.Fatalf("no https: %v", e)
	}
	if len(f.renamed) != 0 {
		t.Fatal("same name counted as a rename")
	}

	f.st = ipnstate.Status{BackendState: "Starting"}
	w.check(ctx)
	f.now = f.now.Add(time.Minute)
	w.check(ctx)
	if e := f.take(); len(e) != 0 {
		t.Fatalf("short blip warned: %v", e)
	}
	f.now = f.now.Add(2 * time.Minute)
	w.check(ctx)
	if e := f.take(); len(e) != 1 || !strings.Contains(e[0], "Not connected to Tailscale for 3 min") {
		t.Fatalf("offline: %v", e)
	}
	f.st = ipnstate.Status{BackendState: "Running", CertDomains: []string{"x"}, Self: &ipnstate.PeerStatus{DNSName: "share.example.ts.net."}}
	w.check(ctx)
	if e := f.take(); len(e) != 1 || e[0] != "warning message=" {
		t.Fatalf("recovered: %v", e)
	}
}
