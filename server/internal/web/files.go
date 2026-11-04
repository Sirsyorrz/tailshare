package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
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
	// Previews show file contents, so they need download, not just view.
	mux.HandleFunc("GET /api/shares/{share}/thumb", s.withShare(perm.View|perm.Download, s.thumb))
	mux.HandleFunc("GET /api/shares/{share}/view", s.withShare(perm.View|perm.Download, s.view))
	mux.HandleFunc("GET /api/shares/{share}/search", s.withShare(perm.View, s.search))
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
				g = s.grantLocked(u, sc)
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
	// Owner is set for admins, who have full control of every share.
	Owner bool `json:"owner,omitempty"`
}

func (s *Server) isAdmin(u identity.User) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isAdminLocked(u)
}

// isAdminLocked and grantLocked need s.mu held.
func (s *Server) isAdminLocked(u identity.User) bool {
	return s.cfg.IsAdmin(u.ID, u.Login) && (!u.External || s.cfg.AllowExternalAdmins)
}

// grantLocked gives admins full control of every share, so the owner can
// never be locked out of their own server's folders.
func (s *Server) grantLocked(u identity.User, sc *config.Share) config.Grant {
	if s.isAdminLocked(u) {
		return sc.OwnerGrant()
	}
	return sc.GrantFor(u.ID, u.Login, time.Now())
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	out := struct {
		User   identity.User `json:"user"`
		Admin  bool          `json:"admin"`
		Shares []shareInfo   `json:"shares"`
	}{User: u, Admin: s.isAdmin(u), Shares: []shareInfo{}}
	s.mu.RLock()
	for i := range s.cfg.Shares {
		sc := &s.cfg.Shares[i]
		sh := s.shares[sc.Name]
		if sh == nil {
			continue
		}
		g := s.grantLocked(u, sc)
		if g.Perms == 0 {
			continue
		}
		info := shareInfo{Name: sc.Name, Perms: g.Perms, Used: sh.Usage(u.ID), Owner: s.isAdminLocked(u)}
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
	// The web UI polls the open folder; an unchanged one costs a 304.
	b, err := json.Marshal(out)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(append(b, '\n'))
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
	paths := r.URL.Query()["path"]
	if len(paths) == 0 {
		paths = []string{""}
	}
	if len(paths) > 5000 {
		httpError(w, http.StatusBadRequest, "too many items")
		return
	}
	name, err := c.sh.ZipName(paths...)
	if err != nil {
		storeError(w, err)
		return
	}
	// Check the paths before headers go out, so a bad one is a clean error.
	for _, p := range paths {
		if _, err := c.sh.Stat(p); err != nil {
			storeError(w, err)
			return
		}
	}
	attachment(w, "application/zip", name+".zip")
	start := time.Now()
	n, err := c.sh.WriteZip(w, paths...)
	e := activity.Entry{Action: "download-zip", Share: c.sh.Name, Path: strings.Join(paths, ", "), Bytes: n, Seconds: time.Since(start).Seconds()}
	if err != nil {
		// Headers are already sent, so the client just sees a cut-off zip.
		e.Note = "incomplete"
	}
	s.record(r, e)
}

var thumbSizes = map[string]int{"": 320, "small": 320, "large": 1600}

func (s *Server) thumb(w http.ResponseWriter, r *http.Request, c shareCtx) {
	size, ok := thumbSizes[r.URL.Query().Get("size")]
	if !ok {
		httpError(w, http.StatusBadRequest, "bad size")
		return
	}
	b, mod, err := c.sh.Thumbnail(r.Context(), r.URL.Query().Get("path"), size)
	if errors.Is(err, store.ErrNoPreview) {
		w.Header().Set("Cache-Control", "private, max-age=3600")
		httpError(w, http.StatusNotFound, "no preview")
		return
	}
	if err != nil {
		storeError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	// The client puts the file's mtime in the URL, so a changed file gets a
	// new URL and this can be cached hard.
	h.Set("Cache-Control", "private, max-age=604800")
	h.Set("Last-Modified", mod.UTC().Format(http.TimeFormat))
	w.Write(b)
}

// inlineTypes are the only types served for display in the page. Anything
// that can run script (HTML, SVG, XML) is never on this list.
var inlineTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp",
	// .mov and .mkv are usually H.264 inside; browsers play them under these
	// types even though the containers differ.
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mov": "video/mp4", ".mkv": "video/webm",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac", ".ogg": "audio/ogg", ".opus": "audio/ogg",
	".wav": "audio/wav", ".flac": "audio/flac",
	".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8", ".log": "text/plain; charset=utf-8",
	".csv": "text/plain; charset=utf-8", ".json": "text/plain; charset=utf-8",
}

// view serves a file for the preview lightbox: inline, with a type from
// the allow list (never sniffed), sandboxed, and with Range support so
// video can seek.
func (s *Server) view(w http.ResponseWriter, r *http.Request, c shareCtx) {
	p := r.URL.Query().Get("path")
	ct, ok := inlineTypes[strings.ToLower(path.Ext(p))]
	if !ok {
		httpError(w, http.StatusUnsupportedMediaType, "can't preview this type")
		return
	}
	f, st, err := c.sh.OpenFile(p)
	if err != nil {
		storeError(w, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Disposition", "inline")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

type searchHit struct {
	store.Hit
	Mine bool `json:"mine,omitempty"`
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, c shareCtx) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	hits, truncated, err := c.sh.Search(ctx, r.URL.Query().Get("path"), r.URL.Query().Get("q"), 1000)
	if err != nil {
		storeError(w, err)
		return
	}
	me := userOf(r).ID
	out := make([]searchHit, len(hits))
	for i, h := range hits {
		out[i] = searchHit{Hit: h, Mine: h.Owner != 0 && h.Owner == me}
	}
	writeJSON(w, map[string]any{"hits": out, "truncated": truncated})
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

// spaceProblem says why size more bytes can't be uploaded by user, or "".
// Unfinished uploads count as already used.
func (s *Server) spaceProblem(c shareCtx, user int64, size int64) string {
	pendingAll, pendingMine := c.sh.Pending(user)
	if c.grant.Limited {
		used := c.sh.Usage(user) + pendingMine
		if used+size > c.grant.QuotaBytes {
			return fmt.Sprintf("over your storage limit (%s of %s used)", human(used), human(c.grant.QuotaBytes))
		}
	}
	if free, err := c.sh.FreeBytes(); err == nil && free-pendingAll-size < s.minFree() {
		return "not enough free space on the server"
	}
	return ""
}

// spaceLeft is how much user could still upload to the share.
func (s *Server) spaceLeft(c shareCtx, user int64) int64 {
	pendingAll, pendingMine := c.sh.Pending(user)
	left := int64(-1)
	if free, err := c.sh.FreeBytes(); err == nil {
		left = max(0, free-pendingAll-s.minFree())
	}
	if c.grant.Limited {
		q := max(0, c.grant.QuotaBytes-c.sh.Usage(user)-pendingMine)
		if left < 0 || q < left {
			left = q
		}
	}
	return left
}

func (s *Server) minFree() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.MinFreeBytes()
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
	if msg := s.spaceProblem(c, u.ID, in.Size); msg != "" {
		httpError(w, http.StatusInsufficientStorage, msg)
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
