package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/Sirsyorrz/tailshare/server/internal/config"
	"github.com/Sirsyorrz/tailshare/server/internal/identity"
	"github.com/Sirsyorrz/tailshare/server/internal/perm"
	"github.com/Sirsyorrz/tailshare/server/internal/store"
)

//go:embed ui
var uiFS embed.FS

type Server struct {
	resolver identity.Resolver
	hosts    map[string]bool
	https    bool

	mu     sync.RWMutex
	cfg    *config.Config
	shares map[string]*store.Share

	seen sync.Map
}

// New takes the exact Host header values the server answers to; anything
// else is rejected to stop DNS rebinding.
func New(r identity.Resolver, hosts []string, https bool) *Server {
	s := &Server{resolver: r, hosts: map[string]bool{}, https: https, shares: map[string]*store.Share{}}
	for _, h := range hosts {
		s.hosts[h] = true
	}
	return s
}

// Load swaps in a new config. Shares that fail to open are logged and left
// out rather than taking the whole server down.
func (s *Server) Load(cfg *config.Config) {
	next := map[string]*store.Share{}
	for _, sc := range cfg.Shares {
		writable := !sc.ReadOnly
		sh, err := store.Open(sc.Name, sc.Path, writable)
		if err != nil {
			log.Printf("share %q (%s) disabled: %v", sc.Name, sc.Path, err)
			continue
		}
		next[sc.Name] = sh
	}
	s.mu.Lock()
	old := s.shares
	s.cfg, s.shares = cfg, next
	s.mu.Unlock()
	for _, sh := range old {
		sh.Close()
	}
	log.Printf("loaded %d share(s)", len(next))
}

func (s *Server) Housekeep(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.mu.RLock()
		for _, sh := range s.shares {
			sh.PurgeUploads(7 * 24 * time.Hour)
			sh.PurgeTrash(30 * 24 * time.Hour)
		}
		s.mu.RUnlock()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) Handler() http.Handler {
	ui, _ := fs.Sub(uiFS, "ui")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(ui))
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("GET /api/shares/{share}/list", s.withShare(perm.View, s.list))
	mux.HandleFunc("GET /api/shares/{share}/download", s.withShare(perm.Download, s.download))
	mux.HandleFunc("POST /api/shares/{share}/mkdir", s.withShare(perm.Mkdir, s.mkdir))
	mux.HandleFunc("POST /api/shares/{share}/rename", s.withShare(perm.Rename, s.rename))
	mux.HandleFunc("POST /api/shares/{share}/delete", s.withShare(perm.Delete, s.delete))
	mux.HandleFunc("POST /api/shares/{share}/uploads", s.withShare(perm.Upload, s.uploadCreate))
	mux.HandleFunc("GET /api/shares/{share}/uploads/{id}", s.withShare(perm.Upload, s.uploadStatus))
	mux.HandleFunc("PUT /api/shares/{share}/uploads/{id}", s.withShare(perm.Upload, s.uploadChunk))
	mux.HandleFunc("POST /api/shares/{share}/uploads/{id}/complete", s.withShare(perm.Upload, s.uploadComplete))
	mux.HandleFunc("DELETE /api/shares/{share}/uploads/{id}", s.withShare(perm.Upload, s.uploadCancel))
	return s.guard(mux)
}

type ctxKey struct{}

func userOf(r *http.Request) identity.User { return r.Context().Value(ctxKey{}).(identity.User) }

type shareCtx struct {
	sh    *store.Share
	perms perm.Set
}

// withShare resolves the share and checks need. A share the user has no
// rights on at all is reported as missing, so names don't leak.
func (s *Server) withShare(need perm.Set, h func(http.ResponseWriter, *http.Request, shareCtx)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userOf(r)
		name := r.PathValue("share")
		s.mu.RLock()
		sh := s.shares[name]
		var p perm.Set
		if sh != nil {
			for i := range s.cfg.Shares {
				if s.cfg.Shares[i].Name == name {
					p = s.cfg.Shares[i].PermsFor(u.ID, u.Login)
				}
			}
		}
		s.mu.RUnlock()
		if sh == nil || p == 0 {
			httpError(w, http.StatusNotFound, "share not found")
			return
		}
		if !p.Has(need) {
			httpError(w, http.StatusForbidden, "not allowed")
			return
		}
		h(w, r, shareCtx{sh: sh, perms: p})
	}
}

type shareInfo struct {
	Name  string   `json:"name"`
	Perms perm.Set `json:"perms"`
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	out := struct {
		User   identity.User `json:"user"`
		Shares []shareInfo   `json:"shares"`
	}{User: u, Shares: []shareInfo{}}
	s.mu.RLock()
	for i := range s.cfg.Shares {
		sc := &s.cfg.Shares[i]
		if s.shares[sc.Name] == nil {
			continue
		}
		if p := sc.PermsFor(u.ID, u.Login); p != 0 {
			out.Shares = append(out.Shares, shareInfo{Name: sc.Name, Perms: p})
		}
	}
	s.mu.RUnlock()
	writeJSON(w, out)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, c shareCtx) {
	ents, err := c.sh.List(r.URL.Query().Get("path"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, ents)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, c shareCtx) {
	p := r.URL.Query().Get("path")
	f, st, err := c.sh.OpenFile(p)
	if err != nil {
		storeError(w, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": st.Name()}))
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if r.Header.Get("Range") == "" {
		activity(r, "download", c.sh.Name, p)
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (s *Server) mkdir(w http.ResponseWriter, r *http.Request, c shareCtx) {
	var in struct{ Path, Name string }
	if !readJSON(w, r, &in) {
		return
	}
	if err := c.sh.Mkdir(in.Path, in.Name); err != nil {
		storeError(w, err)
		return
	}
	activity(r, "mkdir", c.sh.Name, path.Join(in.Path, in.Name))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) rename(w http.ResponseWriter, r *http.Request, c shareCtx) {
	var in struct{ Path, ToDir, Name string }
	if !readJSON(w, r, &in) {
		return
	}
	if err := c.sh.Rename(in.Path, in.ToDir, in.Name); err != nil {
		storeError(w, err)
		return
	}
	activity(r, "rename", c.sh.Name, in.Path+" -> "+path.Join(in.ToDir, in.Name))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request, c shareCtx) {
	var in struct{ Path string }
	if !readJSON(w, r, &in) {
		return
	}
	if err := c.sh.Delete(in.Path, userOf(r).Login); err != nil {
		storeError(w, err)
		return
	}
	activity(r, "delete", c.sh.Name, in.Path)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) uploadCreate(w http.ResponseWriter, r *http.Request, c shareCtx) {
	var in struct {
		Dir, Name string
		Size      int64
	}
	if !readJSON(w, r, &in) {
		return
	}
	info, err := c.sh.CreateUpload(userOf(r).ID, in.Dir, in.Name, in.Size)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, info)
}

func (s *Server) uploadStatus(w http.ResponseWriter, r *http.Request, c shareCtx) {
	info, err := c.sh.UploadStatus(r.PathValue("id"), userOf(r).ID)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, info)
}

func (s *Server) uploadChunk(w http.ResponseWriter, r *http.Request, c shareCtx) {
	off, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || off < 0 {
		httpError(w, http.StatusBadRequest, "bad offset")
		return
	}
	cur, err := c.sh.WriteChunk(r.PathValue("id"), userOf(r).ID, off, r.Body)
	if errors.Is(err, store.ErrOffset) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"error": "offset mismatch", "offset": cur})
		return
	}
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, map[string]int64{"offset": cur})
}

func (s *Server) uploadComplete(w http.ResponseWriter, r *http.Request, c shareCtx) {
	var in struct{ Replace bool }
	if !readJSON(w, r, &in) {
		return
	}
	if in.Replace && !c.perms.Has(perm.Overwrite) {
		httpError(w, http.StatusForbidden, "not allowed to overwrite")
		return
	}
	u := userOf(r)
	st, err := c.sh.UploadStatus(r.PathValue("id"), u.ID)
	if err != nil {
		storeError(w, err)
		return
	}
	name, err := c.sh.CompleteUpload(st.ID, u.ID, in.Replace, u.Login)
	if err != nil {
		storeError(w, err)
		return
	}
	activity(r, "upload", c.sh.Name, path.Join(st.Dir, name)+" ("+strconv.FormatInt(st.Size, 10)+" bytes)")
	writeJSON(w, map[string]string{"name": name})
}

func (s *Server) uploadCancel(w http.ResponseWriter, r *http.Request, c shareCtx) {
	if err := c.sh.CancelUpload(r.PathValue("id"), userOf(r).ID); err != nil {
		storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// activity is the audit trail until the append-only log file exists.
func activity(r *http.Request, action, share, detail string) {
	u := userOf(r)
	log.Printf("activity user=%s id=%d node=%s action=%s share=%q path=%q", u.Login, u.ID, u.Node, action, share, detail)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		httpError(w, http.StatusBadRequest, "bad request body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrExists):
		httpError(w, http.StatusConflict, "already exists")
	case errors.Is(err, store.ErrIncomplete):
		httpError(w, http.StatusConflict, "upload incomplete")
	case errors.Is(err, store.ErrTooLarge):
		httpError(w, http.StatusRequestEntityTooLarge, "more data than declared")
	case errors.Is(err, store.ErrBadName):
		httpError(w, http.StatusBadRequest, "invalid name")
	case errors.Is(err, store.ErrBadPath), errors.Is(err, store.ErrNotDir), errors.Is(err, store.ErrIsDir):
		httpError(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("error: %v", err)
		httpError(w, http.StatusInternalServerError, "server error")
	}
}
