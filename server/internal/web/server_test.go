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
	s := New(Options{Resolver: identity.Fixed{User: identity.User{ID: 7, Login: "friend@x", Node: "pc"}}, Hosts: []string{host}, HTTPS: true})
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

func serverAs(t *testing.T, u identity.User, cfg *config.Config) http.Handler {
	t.Helper()
	s := New(Options{Resolver: identity.Fixed{User: u}, Hosts: []string{host}, HTTPS: true})
	s.Load(cfg)
	return s.Handler()
}

func upload(t *testing.T, h http.Handler, share, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := do(h, req("POST", "/api/shares/"+share+"/uploads", map[string]any{"dir": "", "name": name, "size": len(body)}, csrf))
	if w.Code != 200 {
		return w
	}
	var up struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &up)
	r := httptest.NewRequest("PUT", "https://"+host+"/api/shares/"+share+"/uploads/"+up.ID+"?offset=0", strings.NewReader(body))
	for k, v := range csrf {
		r.Header.Set(k, v)
	}
	do(h, r)
	return do(h, req("POST", "/api/shares/"+share+"/uploads/"+up.ID+"/complete", map[string]bool{"replace": false}, csrf))
}

func TestDeleteOwnOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "theirs.txt"), []byte("x"), 0o644)
	cfg := &config.Config{Shares: []config.Share{{Name: "s", Path: dir, Access: map[string]perm.Set{
		"a@x": perm.Presets["contributor"] | perm.DeleteOwn,
	}}}}
	h := serverAs(t, identity.User{ID: 3, Login: "a@x"}, cfg)
	if w := upload(t, h, "s", "mine.txt", "hi"); w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	w := do(h, req("GET", "/api/shares/s/list", nil, nil))
	if !strings.Contains(w.Body.String(), `"name":"mine.txt","dir":false,"size":2`) || !strings.Contains(w.Body.String(), `"mine":true`) {
		t.Errorf("list should mark own file: %s", w.Body)
	}
	if w := do(h, req("POST", "/api/shares/s/delete", map[string]string{"path": "theirs.txt"}, csrf)); w.Code != http.StatusForbidden {
		t.Errorf("deleting someone else's file: %d", w.Code)
	}
	if w := do(h, req("POST", "/api/shares/s/delete", map[string]string{"path": "mine.txt"}, csrf)); w.Code != http.StatusNoContent {
		t.Errorf("deleting own file: %d %s", w.Code, w.Body)
	}
}

func TestQuota(t *testing.T) {
	cfg := &config.Config{Shares: []config.Share{{Name: "s", Path: t.TempDir(),
		Access:  map[string]perm.Set{"a@x": perm.Presets["contributor"]},
		QuotaGB: map[string]float64{"a@x": 0.000000010}, // 10 bytes
	}}}
	zero := 0.0
	cfg.MinFreeGB = &zero
	h := serverAs(t, identity.User{ID: 3, Login: "a@x"}, cfg)
	if w := upload(t, h, "s", "a", "123456"); w.Code != 200 {
		t.Fatalf("first upload: %d %s", w.Code, w.Body)
	}
	if w := upload(t, h, "s", "b", "123456"); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("over quota: %d %s", w.Code, w.Body)
	}
}

func TestFreeSpaceFloor(t *testing.T) {
	huge := 1e12 // 1000 PB; no test machine has that free
	cfg := &config.Config{MinFreeGB: &huge, Shares: []config.Share{{Name: "s", Path: t.TempDir(),
		Access: map[string]perm.Set{"a@x": perm.Presets["contributor"]}}}}
	h := serverAs(t, identity.User{ID: 3, Login: "a@x"}, cfg)
	if w := upload(t, h, "s", "a", "1"); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}

func TestAdminGate(t *testing.T) {
	cfg := func() *config.Config {
		return &config.Config{Admins: []string{"boss@x"}, Shares: []config.Share{{Name: "s", Path: t.TempDir(), Access: map[string]perm.Set{}}}}
	}
	cases := []struct {
		u    identity.User
		code int
	}{
		{identity.User{ID: 1, Login: "boss@x"}, 200},
		{identity.User{ID: 1, Login: "boss@x", External: true}, 404},
		{identity.User{ID: 2, Login: "friend@x"}, 404},
	}
	for _, c := range cases {
		h := serverAs(t, c.u, cfg())
		for _, p := range []string{"/api/admin/status", "/api/admin/activity", "/api/admin/shares/s/trash"} {
			if w := do(h, req("GET", p, nil, nil)); w.Code != c.code {
				t.Errorf("%+v %s: got %d want %d", c.u, p, w.Code, c.code)
			}
		}
	}
	ext := cfg()
	ext.AllowExternalAdmins = true
	h := serverAs(t, identity.User{ID: 1, Login: "boss@x", External: true}, ext)
	if w := do(h, req("GET", "/api/admin/status", nil, nil)); w.Code != 200 {
		t.Errorf("allowExternalAdmins: got %d", w.Code)
	}
}

func TestZipEndpoint(t *testing.T) {
	h, _ := setup(t)
	w := do(h, req("GET", "/api/shares/media/zip?path=", nil, nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("got %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "media.zip") {
		t.Errorf("disposition %q", cd)
	}
}

func TestAdminsOwnEveryShare(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Admins: []string{"boss@x"}, Shares: []config.Share{
		{Name: "s", Path: dir, Access: map[string]perm.Set{}},
		{Name: "ro", Path: t.TempDir(), ReadOnly: true, Access: map[string]perm.Set{}},
	}}
	h := serverAs(t, identity.User{ID: 1, Login: "boss@x"}, cfg)
	if w := upload(t, h, "s", "a.txt", "hi"); w.Code != 200 {
		t.Fatalf("admin upload to a share with no access entry: %d %s", w.Code, w.Body)
	}
	if w := do(h, req("POST", "/api/shares/s/delete", map[string]string{"path": "a.txt"}, csrf)); w.Code != http.StatusNoContent {
		t.Fatalf("admin delete: %d", w.Code)
	}
	if w := upload(t, h, "ro", "a.txt", "hi"); w.Code != http.StatusForbidden {
		t.Fatalf("admin upload to read-only share: %d", w.Code)
	}
	ext := serverAs(t, identity.User{ID: 1, Login: "boss@x", External: true}, cfg)
	if w := do(ext, req("GET", "/api/shares/s/list", nil, nil)); w.Code != http.StatusNotFound {
		t.Fatalf("admin from a shared-in device got owner access: %d", w.Code)
	}
}
