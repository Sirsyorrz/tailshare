package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sirsyorrz/tailshare/server/internal/config"
	"github.com/Sirsyorrz/tailshare/server/internal/identity"
)

func adminServer(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "footage", "2026"), 0o755)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	cfg, _ := config.Parse([]byte(`{"admins":["boss@x"],"shares":[]}`))
	cfg.Save(cfgPath)
	s := New(Options{Resolver: identity.Fixed{User: identity.User{ID: 1, Login: "boss@x"}}, Hosts: []string{host}, HTTPS: true})
	s.Configure(cfgPath, []string{root}, "")
	s.Load(cfg)
	return s.Handler(), root, cfgPath
}

func getVersion(t *testing.T, h http.Handler) string {
	t.Helper()
	w := do(h, req("GET", "/api/admin/config", nil, nil))
	var out struct{ Version string }
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Version == "" {
		t.Fatalf("no version: %s", w.Body)
	}
	return out.Version
}

func putConfig(h http.Handler, version, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PUT", "https://"+host+"/api/admin/config", bytes.NewBufferString(body))
	for k, v := range csrf {
		r.Header.Set(k, v)
	}
	r.Header.Set("If-Match", `"`+version+`"`)
	return do(h, r)
}

func TestConfigUpdate(t *testing.T) {
	h, root, cfgPath := adminServer(t)
	v := getVersion(t, h)
	good := `{"admins":["boss@x"],"shares":[{"name":"footage","path":"` + filepath.Join(root, "footage") + `","access":{"id:7":"contributor"}}]}`

	if w := putConfig(h, "stale", good); w.Code != http.StatusPreconditionFailed {
		t.Errorf("stale version: %d", w.Code)
	}
	if w := putConfig(h, v, `{"admins":["someone@else"],"shares":[]}`); w.Code != http.StatusBadRequest {
		t.Errorf("self lockout allowed: %d", w.Code)
	}
	if w := putConfig(h, v, `{"admins":["boss@x"],"shares":[{"name":"etc","path":"/etc","access":{}}]}`); w.Code != http.StatusBadRequest {
		t.Errorf("share outside roots allowed: %d", w.Code)
	}
	os.Symlink("/etc", filepath.Join(root, "sneaky"))
	if w := putConfig(h, v, `{"admins":["boss@x"],"shares":[{"name":"s","path":"`+filepath.Join(root, "sneaky")+`","access":{}}]}`); w.Code != http.StatusBadRequest {
		t.Errorf("symlink out of root allowed: %d", w.Code)
	}
	w := putConfig(h, v, good)
	if w.Code != 200 {
		t.Fatalf("valid update: %d %s", w.Code, w.Body)
	}
	saved, err := config.Load(cfgPath)
	if err != nil || len(saved.Shares) != 1 || saved.Shares[0].Name != "footage" {
		t.Fatalf("not saved: %+v %v", saved, err)
	}
	if w := do(h, req("GET", "/api/admin/status", nil, nil)); !strings.Contains(w.Body.String(), `"name":"footage"`) {
		t.Errorf("new share not live: %s", w.Body)
	}
	if w := putConfig(h, v, good); w.Code != http.StatusPreconditionFailed {
		t.Errorf("reusing the old version after a change: %d", w.Code)
	}
}

func TestBrowse(t *testing.T) {
	h, root, _ := adminServer(t)
	real, _ := filepath.EvalSymlinks(root)
	w := do(h, req("GET", "/api/admin/browse", nil, nil))
	if !strings.Contains(w.Body.String(), real) {
		t.Errorf("roots not listed: %s", w.Body)
	}
	w = do(h, req("GET", "/api/admin/browse?path="+filepath.Join(root, "footage"), nil, nil))
	var out browseResult
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Dirs) != 1 || out.Dirs[0] != "2026" || out.Parent != real {
		t.Errorf("browse: %+v", out)
	}
	if w := do(h, req("GET", "/api/admin/browse?path=/etc", nil, nil)); w.Code != http.StatusBadRequest {
		t.Errorf("browse outside roots: %d", w.Code)
	}
}

func TestConfigEndpointsAdminOnly(t *testing.T) {
	_, _, cfgPath := adminServer(t)
	cfg, _ := config.Load(cfgPath)
	s := New(Options{Resolver: identity.Fixed{User: identity.User{ID: 2, Login: "friend@x"}}, Hosts: []string{host}, HTTPS: true})
	s.Configure(cfgPath, []string{"/"}, "")
	s.Load(cfg)
	h := s.Handler()
	for _, p := range []string{"/api/admin/config", "/api/admin/browse?path=/"} {
		if w := do(h, req("GET", p, nil, nil)); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

func TestRootsFileIsLive(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "late"), 0o755)
	rootsFile := filepath.Join(t.TempDir(), "roots.txt")
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	cfg, _ := config.Parse([]byte(`{"admins":["boss@x"],"shares":[]}`))
	cfg.Save(cfgPath)
	s := New(Options{Resolver: identity.Fixed{User: identity.User{ID: 1, Login: "boss@x"}}, Hosts: []string{host}, HTTPS: true})
	s.Configure(cfgPath, nil, rootsFile)
	s.Load(cfg)
	h := s.Handler()
	if w := do(h, req("GET", "/api/admin/browse", nil, nil)); strings.Contains(w.Body.String(), root) {
		t.Fatal("root listed before it was added")
	}
	os.WriteFile(rootsFile, []byte(root+"\n"), 0o600)
	real, _ := filepath.EvalSymlinks(root)
	if w := do(h, req("GET", "/api/admin/browse", nil, nil)); !strings.Contains(w.Body.String(), real) {
		t.Fatalf("root added at runtime not picked up: %s", w.Body)
	}
}
