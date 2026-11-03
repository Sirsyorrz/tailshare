package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sirsyorrz/tailshare/server/internal/perm"
)

func load(t *testing.T, js string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(js), 0o600)
	return Load(p)
}

func TestGrantMergesAndExpires(t *testing.T) {
	c, err := load(t, `{"admins":["id:1"],"shares":[{"name":"s","path":"/x",
		"access":{"friend@x":"viewer","id:7":["upload"],"old@x":"editor"},
		"expires":{"old@x":"2000-01-01"},
		"quotaGB":{"id:7":2}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	s := &c.Shares[0]
	now := time.Now()

	g := s.GrantFor(7, "Friend@X", now)
	if g.Perms != perm.View|perm.Download|perm.Upload {
		t.Errorf("merged perms: %v", g.Perms.Names())
	}
	if g.Limited {
		t.Error("friend@x has no quota, so the merged grant must be unlimited")
	}
	if g := s.GrantFor(7, "", now); !g.Limited || g.QuotaBytes != 2e9 {
		t.Errorf("quota: %+v", g)
	}
	if g := s.GrantFor(9, "old@x", now); g.Perms != 0 {
		t.Errorf("expired access still granted: %v", g.Perms.Names())
	}
	if !c.IsAdmin(1, "") || c.IsAdmin(7, "friend@x") {
		t.Error("admin check")
	}
	if c.MinFreeBytes() != 5e9 {
		t.Errorf("default min free: %d", c.MinFreeBytes())
	}
}

func TestExpiryDateIsInclusive(t *testing.T) {
	c, err := load(t, `{"shares":[{"name":"s","path":"/x","access":{"a@x":"viewer"},"expires":{"a@x":"2030-06-15"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	s := &c.Shares[0]
	lastDay := time.Date(2030, 6, 15, 23, 0, 0, 0, time.Local)
	if s.GrantFor(0, "a@x", lastDay).Perms == 0 {
		t.Error("access should last through the expiry date")
	}
	if s.GrantFor(0, "a@x", lastDay.Add(2*time.Hour)).Perms != 0 {
		t.Error("access should end after the expiry date")
	}
}

func TestValidation(t *testing.T) {
	bad := []string{
		`{"shares":[{"name":"Bad Name","path":"/x","access":{}}]}`,
		`{"shares":[{"name":"s","path":"rel","access":{}}]}`,
		`{"shares":[{"name":"s","path":"/x","access":{"a@x":"viewer"},"expires":{"b@x":"2030-01-01"}}]}`,
		`{"shares":[{"name":"s","path":"/x","access":{"a@x":"viewer"},"expires":{"a@x":"soon"}}]}`,
		`{"shares":[{"name":"s","path":"/x","access":{"a@x":"viewer"},"quotaGB":{"a@x":-1}}]}`,
		`{"shares":[],"unknown":1}`,
	}
	for _, js := range bad {
		if _, err := load(t, js); err == nil {
			t.Errorf("accepted: %s", js)
		}
	}
}
