package web

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sirsyorrz/tailshare/server/internal/config"
	"github.com/Sirsyorrz/tailshare/server/internal/identity"
	"github.com/Sirsyorrz/tailshare/server/internal/perm"
)

func dav(h http.Handler, method, p, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://"+host+p, strings.NewReader(body))
	r.Header.Set("X-Tailshare", "1")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return do(h, r)
}

type msResp struct {
	Responses []struct {
		Href string `xml:"href"`
		Prop struct {
			Collection *struct{} `xml:"resourcetype>collection"`
			Length     int64     `xml:"getcontentlength"`
			Modified   string    `xml:"getlastmodified"`
			Avail      *int64    `xml:"quota-available-bytes"`
		} `xml:"propstat>prop"`
		Status string `xml:"propstat>status"`
	} `xml:"response"`
}

func hrefs(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	if w.Code != 207 {
		t.Fatalf("propfind: %d %s", w.Code, w.Body)
	}
	var ms msResp
	if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range ms.Responses {
		out = append(out, r.Href)
	}
	return out
}

func davSetup(t *testing.T, access map[string]perm.Set) (string, *config.Config) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub dir"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub dir", "a b.txt"), []byte("hello"), 0o644)
	ro := t.TempDir()
	return dir, &config.Config{Shares: []config.Share{
		{Name: "media", Path: dir, Access: access},
		{Name: "drop", Path: ro, Access: map[string]perm.Set{"f@x": perm.Upload}},
	}}
}

func TestDAVListAndRead(t *testing.T) {
	_, cfg := davSetup(t, map[string]perm.Set{"f@x": perm.Presets["viewer"]})
	h := serverAs(t, identity.User{ID: 2, Login: "f@x"}, cfg)

	got := hrefs(t, dav(h, "PROPFIND", "/dav/", "", map[string]string{"Depth": "1"}))
	// Upload-only shares can't be mounted, so "drop" is absent.
	if strings.Join(got, " ") != "/dav/ /dav/media/" {
		t.Errorf("root: %v", got)
	}
	got = hrefs(t, dav(h, "PROPFIND", "/dav/media/sub%20dir/", "", map[string]string{"Depth": "1"}))
	if strings.Join(got, " ") != "/dav/media/sub%20dir/ /dav/media/sub%20dir/a%20b.txt" {
		t.Errorf("subdir: %v", got)
	}
	if w := dav(h, "PROPFIND", "/dav/media/", "", map[string]string{"Depth": "infinity"}); w.Code != 403 {
		t.Errorf("infinite depth: %d", w.Code)
	}
	if w := dav(h, "GET", "/dav/media/sub%20dir/a%20b.txt", "", map[string]string{"Range": "bytes=1-3"}); w.Code != 206 || w.Body.String() != "ell" {
		t.Errorf("range get: %d %q", w.Code, w.Body)
	}
	for _, p := range []string{"/dav/drop/", "/dav/nope/", "/dav/media/.tailshare/", "/dav/media/../x"} {
		if w := dav(h, "PROPFIND", p, "", map[string]string{"Depth": "0"}); w.Code != 404 && w.Code != 409 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	// Viewers can't write anything.
	for _, c := range []struct{ m, p string }{{"PUT", "/dav/media/new.txt"}, {"MKCOL", "/dav/media/n/"}, {"DELETE", "/dav/media/sub%20dir"}} {
		if w := dav(h, c.m, c.p, "x", nil); w.Code != 403 {
			t.Errorf("viewer %s: %d", c.m, w.Code)
		}
	}
}

func TestDAVNeedsHeader(t *testing.T) {
	_, cfg := davSetup(t, map[string]perm.Set{"f@x": perm.Presets["editor"]})
	h := serverAs(t, identity.User{ID: 2, Login: "f@x"}, cfg)
	r := httptest.NewRequest("GET", "https://"+host+"/dav/media/sub%20dir/a%20b.txt", nil)
	if w := do(h, r); w.Code != 403 {
		t.Errorf("GET without header: %d", w.Code)
	}
	r = httptest.NewRequest("DELETE", "https://"+host+"/dav/media/sub%20dir", nil)
	r.Header.Set("X-Tailshare", "1")
	r.Header.Set("Origin", "https://evil.example")
	if w := do(h, r); w.Code != 403 {
		t.Errorf("cross-origin delete: %d", w.Code)
	}
}

func TestDAVWrites(t *testing.T) {
	dir, cfg := davSetup(t, map[string]perm.Set{"f@x": perm.Presets["editor"]})
	srv := newServer(t, Options{Resolver: identity.Fixed{User: identity.User{ID: 2, Login: "f@x"}}, Hosts: []string{host}, HTTPS: true})
	srv.Load(cfg)
	h, data := srv.Handler(), srv.dataDir(dir)

	if w := dav(h, "MKCOL", "/dav/media/new/", "", nil); w.Code != 201 {
		t.Fatalf("mkcol: %d %s", w.Code, w.Body)
	}
	if w := dav(h, "MKCOL", "/dav/media/new/", "", nil); w.Code != 405 {
		t.Errorf("mkcol existing: %d", w.Code)
	}
	if w := dav(h, "MKCOL", "/dav/media/missing/x/", "", nil); w.Code != 409 {
		t.Errorf("mkcol without parent: %d", w.Code)
	}
	w := dav(h, "PUT", "/dav/media/new/f.txt", "v1", map[string]string{"X-OC-Mtime": "1600000000"})
	if w.Code != 201 || w.Header().Get("X-OC-Mtime") != "accepted" {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	st, _ := os.Stat(filepath.Join(dir, "new", "f.txt"))
	if !st.ModTime().Equal(time.Unix(1600000000, 0)) {
		t.Errorf("mtime %v", st.ModTime())
	}
	if w := dav(h, "PUT", "/dav/media/new/f.txt", "v2", nil); w.Code != 204 {
		t.Fatalf("overwrite: %d", w.Code)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "new", "f.txt")); string(b) != "v2" {
		t.Errorf("content %q", b)
	}

	// Editor-style save: write a temp file, rename it over the original.
	dav(h, "PUT", "/dav/media/new/.f.tmp", "v3", nil)
	w = dav(h, "MOVE", "/dav/media/new/.f.tmp", "", map[string]string{"Destination": "https://" + host + "/dav/media/new/f.txt", "Overwrite": "T"})
	if w.Code != 204 {
		t.Fatalf("move over: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "new", "f.txt")); string(b) != "v3" {
		t.Errorf("after move %q", b)
	}
	// v1 and v2 went to the trash rather than being lost.
	if ents, _ := os.ReadDir(filepath.Join(data, "trash")); len(ents) != 4 {
		t.Errorf("trash has %d entries, want 2 items + 2 records", len(ents))
	}
	if w := dav(h, "MOVE", "/dav/media/new/f.txt", "", map[string]string{"Destination": "/dav/media/sub%20dir/a%20b.txt", "Overwrite": "F"}); w.Code != 412 {
		t.Errorf("move without overwrite onto existing: %d", w.Code)
	}
	if w := dav(h, "MOVE", "/dav/media/new/", "", map[string]string{"Destination": "/dav/media/renamed/"}); w.Code != 201 {
		t.Errorf("dir move: %d %s", w.Code, w.Body)
	}
	if w := dav(h, "MOVE", "/dav/media/renamed/", "", map[string]string{"Destination": "https://other.example/dav/media/x/"}); w.Code != 502 {
		t.Errorf("move to other host: %d", w.Code)
	}
	if w := dav(h, "DELETE", "/dav/media/renamed", "", nil); w.Code != 204 {
		t.Errorf("delete: %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "renamed")); !os.IsNotExist(err) {
		t.Error("folder still there")
	}

	// A body shorter than Content-Length leaves nothing behind.
	r := httptest.NewRequest("PUT", "https://"+host+"/dav/media/cut.bin", strings.NewReader("abc"))
	r.Header.Set("X-Tailshare", "1")
	r.ContentLength = 10
	if w := do(h, r); w.Code != 400 {
		t.Errorf("short put: %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "cut.bin")); !os.IsNotExist(err) {
		t.Error("partial file left in place")
	}
	if ents, _ := os.ReadDir(filepath.Join(data, "uploads")); len(ents) != 0 {
		t.Errorf("staging not cleaned: %d", len(ents))
	}
}

func TestDAVContributorCantReplace(t *testing.T) {
	dir, cfg := davSetup(t, map[string]perm.Set{"f@x": perm.Presets["contributor"]})
	h := serverAs(t, identity.User{ID: 2, Login: "f@x"}, cfg)
	if w := dav(h, "PUT", "/dav/media/sub%20dir/a%20b.txt", "evil", nil); w.Code != 403 {
		t.Errorf("replace: %d", w.Code)
	}
	if w := dav(h, "PUT", "/dav/media/mine.txt", "ok", nil); w.Code != 201 {
		t.Errorf("new file: %d", w.Code)
	}
	if w := dav(h, "MOVE", "/dav/media/mine.txt", "", map[string]string{"Destination": "/dav/media/x.txt"}); w.Code != 403 {
		t.Errorf("rename without permission: %d", w.Code)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "sub dir", "a b.txt")); string(b) != "hello" {
		t.Errorf("original changed: %q", b)
	}
}

func TestDAVQuota(t *testing.T) {
	_, cfg := davSetup(t, map[string]perm.Set{"f@x": perm.Presets["contributor"]})
	cfg.Shares[0].QuotaGB = map[string]float64{"f@x": 0.000000005} // 5 bytes
	h := serverAs(t, identity.User{ID: 2, Login: "f@x"}, cfg)
	if w := dav(h, "PUT", "/dav/media/big.bin", "123456", nil); w.Code != http.StatusInsufficientStorage {
		t.Errorf("over quota: %d", w.Code)
	}
	w := dav(h, "PROPFIND", "/dav/media/", "", map[string]string{"Depth": "0"})
	var ms msResp
	xml.Unmarshal(w.Body.Bytes(), &ms)
	if a := ms.Responses[0].Prop.Avail; a == nil || *a != 5 {
		t.Errorf("available: %v", a)
	}
}
