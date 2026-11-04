package web

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Sirsyorrz/tailshare/server/internal/config"
	"github.com/Sirsyorrz/tailshare/server/internal/identity"
)

func TestChangesWakesOnConfig(t *testing.T) {
	s := newServer(t, Options{Resolver: identity.Fixed{User: identity.User{ID: 7, Login: "friend@x", Node: "pc"}}, Hosts: []string{host}, HTTPS: true})
	s.Load(&config.Config{})
	h := s.Handler()

	var first map[string]string
	json.Unmarshal(do(h, req("GET", "/api/changes", nil, nil)).Body.Bytes(), &first)
	if first["config"] == "" {
		t.Fatalf("no config revision: %v", first)
	}
	if _, ok := first["activity"]; ok {
		t.Errorf("non-admin sees activity revision")
	}

	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- do(h, req("GET", "/api/changes?config="+first["config"], nil, nil)) }()
	select {
	case w := <-done:
		t.Fatalf("answered before anything changed: %s", w.Body)
	case <-time.After(100 * time.Millisecond):
	}
	// Activity alone must not wake a non-admin.
	s.changes.bump(false)
	select {
	case w := <-done:
		t.Fatalf("woken by activity: %s", w.Body)
	case <-time.After(100 * time.Millisecond):
	}
	s.Load(&config.Config{})
	select {
	case w := <-done:
		var next map[string]string
		json.Unmarshal(w.Body.Bytes(), &next)
		if next["config"] == first["config"] {
			t.Errorf("revision didn't move: %v", next)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("not woken by a config change")
	}
}
