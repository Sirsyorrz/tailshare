package web

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Sirsyorrz/tailshare/server/internal/activity"
	"github.com/Sirsyorrz/tailshare/server/internal/config"
	"github.com/Sirsyorrz/tailshare/server/internal/perm"
	"github.com/Sirsyorrz/tailshare/server/internal/store"
)

// WebDAV for the desktop app's mounted drive (rclone, vendor "rclone").
// Only what rclone uses is implemented: PROPFIND (depth 0/1), GET/HEAD,
// PUT, MKCOL, DELETE and MOVE. The layout is /dav/<share>/<path>, and the
// root lists the shares the user can mount.
//
// Every request needs the X-Tailshare header, even reads: no browser can
// add it cross-site, so pages can neither embed nor navigate to anything
// here. The same permissions, quotas, trash and activity log apply as for
// the web UI.
const davPrefix = "/dav/"

// mountable is what a share needs to show up on the drive. Upload-only
// shares can't be mounted: a drive has to be able to read back what it
// just wrote.
const mountable = perm.View | perm.Download

type davTarget struct {
	share string // "" for the root listing
	rel   string // path inside the share, "" for its top
	c     shareCtx
}

func (s *Server) dav(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	if r.Header.Get(csrfHeader) != "1" || !s.sameOrigin(r) {
		httpError(w, http.StatusForbidden, "missing or bad origin")
		return
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		httpError(w, http.StatusForbidden, "cross-site request")
		return
	}
	if r.Method == http.MethodOptions {
		h.Set("DAV", "1")
		h.Set("Allow", "OPTIONS, PROPFIND, GET, HEAD, PUT, MKCOL, DELETE, MOVE")
		w.WriteHeader(http.StatusOK)
		return
	}
	t, ok := s.davResolve(w, r, r.URL.Path)
	if !ok {
		return
	}
	switch r.Method {
	case "PROPFIND":
		s.davPropfind(w, r, t)
	case http.MethodGet, http.MethodHead:
		s.davGet(w, r, t)
	case http.MethodPut:
		s.davPut(w, r, t)
	case "MKCOL":
		s.davMkcol(w, r, t)
	case http.MethodDelete:
		s.davDelete(w, r, t)
	case "MOVE":
		s.davMove(w, r, t)
	default:
		w.Header().Set("Allow", "OPTIONS, PROPFIND, GET, HEAD, PUT, MKCOL, DELETE, MOVE")
		httpError(w, http.StatusMethodNotAllowed, "not supported")
	}
}

// davResolve maps a /dav/ path to a share and checks the user can mount it.
// Shares they can't mount are reported missing, like everywhere else.
func (s *Server) davResolve(w http.ResponseWriter, r *http.Request, p string) (davTarget, bool) {
	rest, ok := strings.CutPrefix(p, davPrefix)
	if !ok {
		if p+"/" == davPrefix {
			return davTarget{}, true
		}
		httpError(w, http.StatusNotFound, "not found")
		return davTarget{}, false
	}
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return davTarget{}, true
	}
	name, rel, _ := strings.Cut(rest, "/")
	u := userOf(r)
	s.mu.RLock()
	sh := s.shares[name]
	var g config.Grant
	if sh != nil {
		if sc := s.shareConfig(name); sc != nil {
			g = s.grantLocked(u, sc)
		}
	}
	s.mu.RUnlock()
	if sh == nil || !g.Perms.Has(mountable) {
		httpError(w, http.StatusNotFound, "not found")
		return davTarget{}, false
	}
	return davTarget{share: name, rel: rel, c: shareCtx{sh: sh, grant: g}}, true
}

// mountableShares lists what the root shows, in config order.
func (s *Server) mountableShares(r *http.Request) []davTarget {
	u := userOf(r)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []davTarget
	for i := range s.cfg.Shares {
		sc := &s.cfg.Shares[i]
		sh := s.shares[sc.Name]
		if sh == nil {
			continue
		}
		if g := s.grantLocked(u, sc); g.Perms.Has(mountable) {
			out = append(out, davTarget{share: sc.Name, c: shareCtx{sh: sh, grant: g}})
		}
	}
	return out
}

func davError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrExists):
		httpError(w, http.StatusPreconditionFailed, "already exists")
	case errors.Is(err, store.ErrBadName):
		httpError(w, http.StatusBadRequest, "invalid name")
	case errors.Is(err, store.ErrBadPath), errors.Is(err, store.ErrNotDir), errors.Is(err, store.ErrIsDir):
		httpError(w, http.StatusConflict, err.Error())
	default:
		log.Printf("dav error: %v", err)
		httpError(w, http.StatusInternalServerError, "server error")
	}
}

func forbid(w http.ResponseWriter, msg string) { httpError(w, http.StatusForbidden, msg) }

// PROPFIND

type davResType struct {
	Collection *struct{} `xml:"D:collection,omitempty"`
}

type davProp struct {
	Name         string     `xml:"D:displayname,omitempty"`
	ResourceType davResType `xml:"D:resourcetype"`
	Length       *int64     `xml:"D:getcontentlength,omitempty"`
	Modified     string     `xml:"D:getlastmodified,omitempty"`
	ETag         string     `xml:"D:getetag,omitempty"`
	ContentType  string     `xml:"D:getcontenttype,omitempty"`
	QuotaAvail   *int64     `xml:"D:quota-available-bytes,omitempty"`
	QuotaUsed    *int64     `xml:"D:quota-used-bytes,omitempty"`
}

type davResponse struct {
	Href   string  `xml:"D:href"`
	Prop   davProp `xml:"D:propstat>D:prop"`
	Status string  `xml:"D:propstat>D:status"`
}

type davMultistatus struct {
	XMLName   xml.Name      `xml:"D:multistatus"`
	NS        string        `xml:"xmlns:D,attr"`
	Responses []davResponse `xml:"D:response"`
}

func hrefFor(share, rel string, dir bool) string {
	parts := []string{strings.TrimSuffix(davPrefix, "/")}
	if share != "" {
		parts = append(parts, url.PathEscape(share))
	}
	if rel != "" {
		for _, seg := range strings.Split(rel, "/") {
			parts = append(parts, url.PathEscape(seg))
		}
	}
	h := strings.Join(parts, "/")
	if dir {
		h += "/"
	}
	return h
}

func davEntry(href, name string, dir bool, size int64, mod time.Time) davResponse {
	p := davProp{Name: name, Modified: mod.UTC().Format(http.TimeFormat)}
	if dir {
		p.ResourceType.Collection = &struct{}{}
	} else {
		p.Length = &size
		p.ETag = fmt.Sprintf(`"%x-%x"`, mod.UnixNano(), size)
		p.ContentType = "application/octet-stream"
	}
	return davResponse{Href: href, Prop: p, Status: "HTTP/1.1 200 OK"}
}

func (s *Server) davPropfind(w http.ResponseWriter, r *http.Request, t davTarget) {
	// The body only says which properties to return; everything useful is
	// always returned, which clients accept.
	io.Copy(io.Discard, io.LimitReader(r.Body, 64<<10))
	depth := r.Header.Get("Depth")
	if depth != "0" && depth != "1" {
		httpError(w, http.StatusForbidden, "only depth 0 and 1 are supported")
		return
	}
	var out []davResponse
	u := userOf(r)

	switch {
	case t.share == "":
		root := davEntry(davPrefix, "", true, 0, time.Unix(0, 0))
		shares := s.mountableShares(r)
		// Shown as the drive's size. Shares can sit on different disks, so
		// this is the most that fits in any one of them.
		var avail, used int64
		for _, m := range shares {
			if m.c.has(perm.Upload) {
				avail = max(avail, s.spaceLeft(m.c, u.ID))
			}
			used += m.c.sh.Usage(u.ID)
		}
		root.Prop.QuotaAvail, root.Prop.QuotaUsed = &avail, &used
		out = append(out, root)
		if depth == "1" {
			for _, m := range shares {
				mod := time.Unix(0, 0)
				if st, err := m.c.sh.Stat(""); err == nil {
					mod = st.ModTime()
				}
				out = append(out, davEntry(hrefFor(m.share, "", true), m.share, true, 0, mod))
			}
		}
	default:
		st, err := t.c.sh.Stat(t.rel)
		if err != nil {
			davError(w, err)
			return
		}
		name := path.Base(t.rel)
		if t.rel == "" {
			name = t.share
		}
		dir := st.IsDir()
		self := davEntry(hrefFor(t.share, t.rel, dir), name, dir, st.Size(), st.ModTime())
		if t.rel == "" {
			left := s.spaceLeft(t.c, u.ID)
			if !t.c.has(perm.Upload) {
				left = 0
			}
			usage := t.c.sh.Usage(u.ID)
			self.Prop.QuotaAvail, self.Prop.QuotaUsed = &left, &usage
		}
		out = append(out, self)
		if depth == "1" && dir {
			ents, err := t.c.sh.List(t.rel)
			if err != nil {
				davError(w, err)
				return
			}
			for _, e := range ents {
				rel := e.Name
				if t.rel != "" {
					rel = t.rel + "/" + e.Name
				}
				out = append(out, davEntry(hrefFor(t.share, rel, e.Dir), e.Name, e.Dir, e.Size, e.Modified))
			}
		}
	}
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(207)
	io.WriteString(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Encode(davMultistatus{NS: "DAV:", Responses: out})
}

// GET and HEAD

func (s *Server) davGet(w http.ResponseWriter, r *http.Request, t davTarget) {
	if t.share == "" || t.rel == "" {
		httpError(w, http.StatusMethodNotAllowed, "is a folder")
		return
	}
	f, st, err := t.c.sh.OpenFile(t.rel)
	if err != nil {
		if errors.Is(err, store.ErrIsDir) {
			httpError(w, http.StatusMethodNotAllowed, "is a folder")
			return
		}
		davError(w, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", "attachment")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("ETag", fmt.Sprintf(`"%x-%x"`, st.ModTime().UnixNano(), st.Size()))
	// The drive reads in ranges; count one download per file opened from
	// the start rather than one per chunk.
	if r.Method == http.MethodGet {
		if rg := r.Header.Get("Range"); rg == "" || strings.HasPrefix(rg, "bytes=0-") {
			s.record(r, activity.Entry{Action: "download", Share: t.share, Path: t.rel, Bytes: st.Size(), Note: "drive"})
		}
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// PUT: staged like a web upload, so a dropped connection never leaves a
// half-written file, and an overwritten file goes to the trash.

func (s *Server) davPut(w http.ResponseWriter, r *http.Request, t davTarget) {
	if t.share == "" || t.rel == "" {
		forbid(w, "can't write here")
		return
	}
	u := userOf(r)
	dir, name := path.Split(t.rel)
	dir = strings.TrimSuffix(dir, "/")
	exists := false
	if st, err := t.c.sh.Stat(t.rel); err == nil {
		if st.IsDir() {
			httpError(w, http.StatusMethodNotAllowed, "is a folder")
			return
		}
		exists = true
	}
	if exists && !t.c.has(perm.Overwrite) {
		forbid(w, "not allowed to replace files")
		return
	}
	if !exists && !t.c.has(perm.Upload) {
		forbid(w, "not allowed to upload")
		return
	}
	if r.ContentLength < 0 {
		httpError(w, http.StatusLengthRequired, "size required")
		return
	}
	if msg := s.spaceProblem(t.c, u.ID, r.ContentLength); msg != "" {
		httpError(w, http.StatusInsufficientStorage, msg)
		return
	}
	start := time.Now()
	up, err := t.c.sh.CreateUpload(u.ID, dir, name, r.ContentLength)
	if err != nil {
		davError(w, err)
		return
	}
	n, err := t.c.sh.WriteChunk(up.ID, u.ID, 0, r.Body)
	if err != nil || n != r.ContentLength {
		t.c.sh.CancelUpload(up.ID, u.ID)
		if errors.Is(err, store.ErrTooLarge) {
			httpError(w, http.StatusRequestEntityTooLarge, "more data than declared")
			return
		}
		httpError(w, http.StatusBadRequest, "upload cut short")
		return
	}
	if err := t.c.sh.CompleteUploadExact(up.ID, u.ID, exists, u.Login); err != nil {
		t.c.sh.CancelUpload(up.ID, u.ID)
		davError(w, err)
		return
	}
	if v := r.Header.Get("X-OC-Mtime"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil && t.c.sh.SetModTime(t.rel, time.Unix(sec, 0)) == nil {
			w.Header().Set("X-OC-Mtime", "accepted")
		}
	}
	e := activity.Entry{Action: "upload", Share: t.share, Path: t.rel, Bytes: r.ContentLength, Seconds: time.Since(start).Seconds(), Note: "drive"}
	if exists {
		e.Note = "drive, replaced existing"
	}
	s.record(r, e)
	if exists {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

func (s *Server) davMkcol(w http.ResponseWriter, r *http.Request, t davTarget) {
	if t.share == "" || t.rel == "" {
		// The share itself always exists.
		httpError(w, http.StatusMethodNotAllowed, "already exists")
		return
	}
	if !t.c.has(perm.Mkdir) {
		forbid(w, "not allowed to create folders")
		return
	}
	if r.ContentLength > 0 {
		httpError(w, http.StatusUnsupportedMediaType, "no body expected")
		return
	}
	if _, err := t.c.sh.Stat(t.rel); err == nil {
		httpError(w, http.StatusMethodNotAllowed, "already exists")
		return
	}
	dir, name := path.Split(t.rel)
	err := t.c.sh.Mkdir(strings.TrimSuffix(dir, "/"), name, userOf(r).ID)
	switch {
	case errors.Is(err, store.ErrExists):
		httpError(w, http.StatusMethodNotAllowed, "already exists")
		return
	case errors.Is(err, store.ErrNotFound):
		httpError(w, http.StatusConflict, "parent folder missing")
		return
	case err != nil:
		davError(w, err)
		return
	}
	s.record(r, activity.Entry{Action: "mkdir", Share: t.share, Path: t.rel, Note: "drive"})
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) davDelete(w http.ResponseWriter, r *http.Request, t davTarget) {
	if t.share == "" || t.rel == "" {
		forbid(w, "can't delete a share")
		return
	}
	u := userOf(r)
	if !t.c.has(perm.Delete) && !(t.c.has(perm.DeleteOwn) && t.c.sh.OwnsAll(t.rel, u.ID)) {
		forbid(w, "not allowed to delete")
		return
	}
	if err := t.c.sh.Delete(t.rel, u.Login); err != nil {
		davError(w, err)
		return
	}
	s.record(r, activity.Entry{Action: "delete", Share: t.share, Path: t.rel, Note: "drive"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) davMove(w http.ResponseWriter, r *http.Request, t davTarget) {
	if t.share == "" || t.rel == "" {
		forbid(w, "can't move a share")
		return
	}
	dest, err := url.Parse(r.Header.Get("Destination"))
	if err != nil || dest.Path == "" {
		httpError(w, http.StatusBadRequest, "bad destination")
		return
	}
	if dest.Host != "" && !s.hosts[stripDefaultPort(dest.Host)] {
		httpError(w, http.StatusBadGateway, "destination is on another server")
		return
	}
	dt, ok := s.davResolve(w, r, dest.Path)
	if !ok {
		return
	}
	if dt.share != t.share {
		// Shares can be on different disks; the drive falls back to copying.
		httpError(w, http.StatusBadGateway, "can't move between shares")
		return
	}
	if dt.rel == "" {
		forbid(w, "bad destination")
		return
	}
	if !t.c.has(perm.Rename) {
		forbid(w, "not allowed to move or rename")
		return
	}
	src := strings.TrimSuffix(t.rel, "/")
	dst := strings.TrimSuffix(dt.rel, "/")
	dir, name := path.Split(dst)
	dir = strings.TrimSuffix(dir, "/")

	overwrite := r.Header.Get("Overwrite") != "F"
	existed := false
	if st, err := t.c.sh.Stat(dst); err == nil && dst != src {
		if !overwrite {
			httpError(w, http.StatusPreconditionFailed, "destination exists")
			return
		}
		if st.IsDir() {
			forbid(w, "won't replace a folder")
			return
		}
		if !t.c.has(perm.Overwrite) {
			forbid(w, "not allowed to replace files")
			return
		}
		existed = true
	}
	if existed {
		err = t.c.sh.RenameReplace(src, dir, name, userOf(r).Login)
	} else {
		err = t.c.sh.Rename(src, dir, name)
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		davError(w, err)
		return
	}
	if v := r.Header.Get("X-OC-Mtime"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil && t.c.sh.SetModTime(dst, time.Unix(sec, 0)) == nil {
			w.Header().Set("X-OC-Mtime", "accepted")
		}
	}
	e := activity.Entry{Action: "rename", Share: t.share, Path: src, To: dst, Note: "drive"}
	if existed {
		e.Note = "drive, replaced existing"
	}
	s.record(r, e)
	if existed {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}
