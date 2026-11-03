package web

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/Sirsyorrz/tailshare/server/internal/activity"
	"github.com/Sirsyorrz/tailshare/server/internal/config"
	"github.com/Sirsyorrz/tailshare/server/internal/identity"
	"github.com/Sirsyorrz/tailshare/server/internal/perm"
	"github.com/Sirsyorrz/tailshare/server/internal/store"
)

func (s *Server) fileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("GET /api/shares/{share}/list", s.withShare(perm.View, s.list))
	mux.HandleFunc("GET /api/shares/{share}/download", s.withShare(perm.Download, s.download))
	mux.HandleFunc("GET /api/shares/{share}/zip", s.withShare(perm.View|perm.Download, s.zip))
	mux.HandleFunc("POST /api/shares/{share}/mkdir", s.withShare(perm.Mkdir, s.mkdir))
	mux.HandleFunc("POST /api/shares/{share}/rename", s.withShare(perm.Rename, s.rename))
	mux.HandleFunc("POST /api/shares/{share}/delete", s.withShare(0, s.delete))
	mux.HandleFunc("POST /api/shares/{share}/uploads", s.withShare(perm.Upload, s.uploadCreate))
	mux.HandleFunc("GET /api/shares/{share}/uploads/{id}", s.withShare(perm.Upload, s.uploadStatus))
	mux.HandleFunc("PUT /api/shares/{share}/uploads/{id}", s.withShare(perm.Upload, s.uploadChunk))
	mux.HandleFunc("POST /api/shares/{share}/uploads/{id}/complete", s.withShare(perm.Upload, s.uploadComplete))
	mux.HandleFunc("DELETE /api/shares/{share}/uploads/{id}", s.withShare(perm.Upload, s.uploadCancel))
}

type shareCtx struct {
	sh    *store.Share
	grant config.Grant
}

func (c shareCtx) has(p perm.Set) bool { return c.grant.Perms.Has(p) }

// withShare resolves the share and checks need. A share the user has no
// rights on at all is reported as missing, so names don't leak.
func (s *Server) withShare(need perm.Set, h func(http.ResponseWriter, *http.Request, shareCtx)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userOf(r)
		name := r.PathValue("share")
		s.mu.RLock()
		sh := s.shares[name]
		var g config.Grant
		if sh != nil {
			if sc := s.shareConfig(name); sc != nil {
				g = sc.GrantFor(u.ID, u.Login, time.Now())
			}
		}
		s.mu.RUnlock()
		if sh == nil || g.Perms == 0 {
			httpError(w, http.StatusNotFound, "share not found")
			return
		}
		if !g.Perms.Has(need) {
			httpError(w, http.StatusForbidden, "not allowed")
			return
		}
		h(w, r, shareCtx{sh: sh, grant: g})
	}
}

// shareConfig needs s.mu held.
func (s *Server) shareConfig(name string) *config.Share {
	for i := range s.cfg.Shares {
		if s.cfg.Shares[i].Name == name {
			return &s.cfg.Shares[i]
		}
	}
	return nil
}

type shareInfo struct {
	Name    string     `json:"name"`
	Perms   perm.Set   `json:"perms"`
	Used    int64      `json:"used"`
	Quota   int64      `json:"quota,omitempty"`
	Expires *time.Time `json:"expires,omitempty"`
}

func (s *Server) isAdmin(u identity.User) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.IsAdmin(u.ID, u.Login) && (!u.External || s.cfg.AllowExternalAdmins)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	out := struct {
		User   identity.User `json:"user"`
		Admin  bool          `json:"admin"`
		Shares []shareInfo   `json:"shares"`
	}{User: u, Admin: s.isAdmin(u), Shares: []shareInfo{}}
	now := time.Now()
	s.mu.RLock()
	for i := range s.cfg.Shares {
		sc := &s.cfg.Shares[i]
		sh := s.shares[sc.Name]
		if sh == nil {
			continue
		}
		g := sc.GrantFor(u.ID, u.Login, now)
		if g.Perms == 0 {
			continue
		}
		info := shareInfo{Name: sc.Name, Perms: g.Perms, Used: sh.Usage(u.ID)}
		if g.Limited {
			info.Quota = g.QuotaBytes
		}
		if !g.Expires.IsZero() {
			info.Expires = &g.Expires
		}
		out.Shares = append(out.Shares, info)
	}
	s.mu.RUnlock()
	writeJSON(w, out)
}

type listEntry struct {
	store.Entry
	Mine bool `json:"mine,omitempty"`
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, c shareCtx) {
	ents, err := c.sh.List(r.URL.Query().Get("path"))
	if err != nil {
		storeError(w, err)
		return
	}
	me := userOf(r).ID
	out := make([]listEntry, len(ents))
	for i, e := range ents {
		out[i] = listEntry{Entry: e, Mine: e.Owner != 0 && e.Owner == me}
	}
	writeJSON(w, out)
}

func attachment(w http.ResponseWriter, contentType, filename string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, c shareCtx) {
	p := r.URL.Query().Get("path")
	f, st, err := c.sh.OpenFile(p)
	if err != nil {
		storeError(w, err)
		return
	}
	defer f.Close()
	attachment(w, "application/octet-stream", st.Name())
	cw := &countingWriter{ResponseWriter: w}
	start := time.Now()
	http.ServeContent(cw, r, "", st.ModTime(), f)

	// Players and resumed downloads send many small Range requests; only
	// whole-file requests and sizeable ranges are worth a log line.
	ranged := r.Header.Get("Range") != ""
	if ranged && cw.n < 1<<20 {
		return
	}
	e := activity.Entry{Action: "download", Share: c.sh.Name, Path: p, Bytes: cw.n, Seconds: time.Since(start).Seconds()}
	if ranged {
		e.Note = "partial"
	} else if cw.n < st.Size() {
		e.Note = fmt.Sprintf("incomplete, file is %d bytes", st.Size())
	}
	s.record(r, e)
}

func (s *Server) zip(w http.ResponseWriter, r *http.Request, c shareCtx) {
	p := r.URL.Query().Get("path")
	name, err := c.sh.ZipName(p)
	if err != nil {
		storeError(w, err)
		return
	}
	attachment(w, "application/zip", name+".zip")
	start := time.Now()
	n, err := c.sh.WriteZip(w, p)
	e := activity.Entry{Action: "download-zip", Share: c.sh.Name, Path: p, Bytes: n, Seconds: time.Since(start).Seconds()}
	if err != nil {
		// Headers are already sent, so the client just sees a cut-off zip.
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrNotDir) || errors.Is(err, store.ErrBadPath) {
			return
		}
		e.Note = "incomplete"
	}
	s.record(r, e)
}

type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}

func (s *Server) mkdir(w http.ResponseWriter, r *http.Request, c shareCtx) {
	var in struct{ Path, Name string }
	if !readJSON(w, r, &in) {
		return
	}
	if err := c.sh.Mkdir(in.Path, in.Name, userOf(r).ID); err != nil {
		storeError(w, err)
		return
	}
	s.record(r, activity.Entry{Action: "mkdir", Share: c.sh.Name, Path: path.Join(in.Path, in.Name)})
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
	s.record(r, activity.Entry{Action: "rename", Share: c.sh.Name, Path: in.Path, To: path.Join(in.ToDir, in.Name)})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request, c shareCtx) {
	var in struct{ Path string }
	if !readJSON(w, r, &in) {
		return
	}
	u := userOf(r)
	if !c.has(perm.Delete) && !(c.has(perm.DeleteOwn) && c.sh.OwnsAll(in.Path, u.ID)) {
		httpError(w, http.StatusForbidden, "not allowed")
		return
	}
	if err := c.sh.Delete(in.Path, u.Login); err != nil {
		storeError(w, err)
		return
	}
	s.record(r, activity.Entry{Action: "delete", Share: c.sh.Name, Path: in.Path})
	w.WriteHeader(http.StatusNoContent)
}

// human matches the web UI's decimal units.
func human(n int64) string {
	f := float64(n)
	if f < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	i := -1
	for f >= 1000 && i < len(units)-1 {
		f /= 1000
		i++
	}
	if f < 10 {
		return fmt.Sprintf("%.1f %s", f, units[i])
	}
	return fmt.Sprintf("%.0f %s", f, units[i])
}

func (s *Server) uploadCreate(w http.ResponseWriter, r *http.Request, c shareCtx) {
	var in struct {
		Dir, Name string
		Size      int64
	}
	if !readJSON(w, r, &in) {
		return
	}
	u := userOf(r)
	pendingAll, pendingMine := c.sh.Pending(u.ID)
	if c.grant.Limited {
		used := c.sh.Usage(u.ID) + pendingMine
		if used+in.Size > c.grant.QuotaBytes {
			httpError(w, http.StatusInsufficientStorage, fmt.Sprintf("over your storage limit (%s of %s used)", human(used), human(c.grant.QuotaBytes)))
			return
		}
	}
	s.mu.RLock()
	minFree := s.cfg.MinFreeBytes()
	s.mu.RUnlock()
	if free, err := c.sh.FreeBytes(); err == nil && free-pendingAll-in.Size < minFree {
		httpError(w, http.StatusInsufficientStorage, "not enough free space on the server")
		return
	}
	info, err := c.sh.CreateUpload(u.ID, in.Dir, in.Name, in.Size)
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
		writeJSON(w, map[string]any{"error": "offset mismatch", "offset": cur})
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
	if in.Replace && !c.has(perm.Overwrite) {
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
	e := activity.Entry{Action: "upload", Share: c.sh.Name, Path: path.Join(st.Dir, name), Bytes: st.Size, Seconds: time.Since(st.Created).Seconds()}
	if in.Replace {
		e.Note = "replaced existing"
	}
	s.record(r, e)
	writeJSON(w, map[string]string{"name": name})
}

func (s *Server) uploadCancel(w http.ResponseWriter, r *http.Request, c shareCtx) {
	if err := c.sh.CancelUpload(r.PathValue("id"), userOf(r).ID); err != nil {
		storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
