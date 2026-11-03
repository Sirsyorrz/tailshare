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
	"github.com/Sirsyorrz/tailshare/server/internal/perm"
)

const host = "tailshare.example.ts.net"

func setup(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)
	cfg := &config.Config{Shares: []config.Share{
		{Name: "media", Path: dir, Access: map[string]perm.Set{
			"friend@x": perm.Presets["contributor"],
			"viewer@x": perm.Presets["viewer"],
		}},
		{Name: "secret", Path: t.TempDir(), Access: map[string]perm.Set{"owner@x": perm.Presets["editor"]}},
	}}
	s := New(identity.Fixed{User: identity.User{ID: 7, Login: "friend@x", Node: "pc"}}, []string{host}, true)
	s.Load(cfg)
	return s.Handler(), dir
}

func req(method, path string, body any, hdr map[string]string) *http.Request {
	var b bytes.Buffer
	if body != nil {
		json.NewEncoder(&b).Encode(body)
	}
	r := httptest.NewRequest(method, "https://"+host+path, &b)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

var csrf = map[string]string{"X-Tailshare": "1", "Origin": "https://" + host, "Sec-Fetch-Site": "same-origin"}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHostCheck(t *testing.T) {
	h, _ := setup(t)
	r := req("GET", "/api/me", nil, nil)
	r.Host = "evil.example"
	if w := do(h, r); w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("got %d", w.Code)
	}
}

func TestCSRF(t *testing.T) {
	h, _ := setup(t)
	body := map[string]string{"path": "", "name": "x"}
	cases := []map[string]string{
		{},
		{"Origin": "https://" + host},
		{"X-Tailshare": "1", "Origin": "https://evil.example"},
		{"X-Tailshare": "1", "Sec-Fetch-Site": "cross-site"},
	}
	for _, hd := range cases {
		if w := do(h, req("POST", "/api/shares/media/mkdir", body, hd)); w.Code != http.StatusForbidden {
			t.Errorf("%v: got %d", hd, w.Code)
		}
	}
	if w := do(h, req("POST", "/api/shares/media/mkdir", body, csrf)); w.Code != http.StatusNoContent {
		t.Errorf("valid request: got %d %s", w.Code, w.Body)
	}
	if w := do(h, req("GET", "/api/shares/media/list", nil, map[string]string{"Sec-Fetch-Site": "cross-site"})); w.Code != http.StatusForbidden {
		t.Errorf("cross-site GET: got %d", w.Code)
	}
}

func TestPermissions(t *testing.T) {
	h, _ := setup(t)
	if w := do(h, req("GET", "/api/shares/secret/list", nil, nil)); w.Code != http.StatusNotFound {
		t.Errorf("share without access should look missing, got %d", w.Code)
	}
	if w := do(h, req("POST", "/api/shares/media/delete", map[string]string{"path": "a.txt"}, csrf)); w.Code != http.StatusForbidden {
		t.Errorf("contributor delete: got %d", w.Code)
	}
	w := do(h, req("GET", "/api/me", nil, nil))
	if strings.Contains(w.Body.String(), "secret") {
		t.Errorf("/api/me leaks share names: %s", w.Body)
	}
}

func TestUploadFlowAndOverwriteGate(t *testing.T) {
	h, dir := setup(t)
	w := do(h, req("POST", "/api/shares/media/uploads", map[string]any{"dir": "", "name": "a.txt", "size": 3}, csrf))
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var up struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &up)

	r := httptest.NewRequest("PUT", "https://"+host+"/api/shares/media/uploads/"+up.ID+"?offset=0", strings.NewReader("new"))
	for k, v := range csrf {
		r.Header.Set(k, v)
	}
	if w := do(h, r); w.Code != 200 {
		t.Fatalf("chunk: %d %s", w.Code, w.Body)
	}
	if w := do(h, req("POST", "/api/shares/media/uploads/"+up.ID+"/complete", map[string]bool{"replace": true}, csrf)); w.Code != http.StatusForbidden {
		t.Fatalf("replace without overwrite: %d", w.Code)
	}
	w = do(h, req("POST", "/api/shares/media/uploads/"+up.ID+"/complete", map[string]bool{"replace": false}, csrf))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "a (1).txt") {
		t.Fatalf("complete: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "hello" {
		t.Fatalf("original changed: %q", b)
	}
}

func TestDownloadHeaders(t *testing.T) {
	h, _ := setup(t)
	w := do(h, req("GET", "/api/shares/media/download?path=a.txt", nil, nil))
	if w.Code != 200 || w.Body.String() != "hello" {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("content-type %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("disposition %q", cd)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("csp %q", csp)
	}
}
