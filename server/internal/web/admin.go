package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Sirsyorrz/tailshare/server/internal/activity"
	"github.com/Sirsyorrz/tailshare/server/internal/reach"
	"github.com/Sirsyorrz/tailshare/server/internal/store"
)

func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/status", s.withAdmin(s.adminStatus))
	mux.HandleFunc("GET /api/admin/activity", s.withAdmin(s.adminActivity))
	mux.HandleFunc("GET /api/admin/shares/{share}/trash", s.withAdmin(s.adminTrash))
	mux.HandleFunc("POST /api/admin/shares/{share}/trash/{id}/restore", s.withAdmin(s.adminRestore))
	mux.HandleFunc("DELETE /api/admin/shares/{share}/trash/{id}", s.withAdmin(s.adminPurge))
}

// withAdmin hides admin endpoints entirely (404) from everyone else.
func (s *Server) withAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(userOf(r)) {
			httpError(w, http.StatusNotFound, "not found")
			return
		}
		h(w, r)
	}
}

func (s *Server) adminShare(w http.ResponseWriter, r *http.Request) *store.Share {
	s.mu.RLock()
	sh := s.shares[r.PathValue("share")]
	s.mu.RUnlock()
	if sh == nil {
		httpError(w, http.StatusNotFound, "share not found")
	}
	return sh
}

type peerStatus struct {
	reach.Peer
	HasAccess bool `json:"hasAccess"`
}

type shareStatus struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	ReadOnly   bool   `json:"readOnly"`
	Online     bool   `json:"online"`
	FreeBytes  int64  `json:"freeBytes"`
	TrashCount int    `json:"trashCount"`
}

func (s *Server) adminStatus(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Shares       []shareStatus `json:"shares"`
		Peers        []peerStatus  `json:"peers"`
		PolicyKnown  bool          `json:"policyKnown"`
		MinFreeBytes int64         `json:"minFreeBytes"`
	}{Shares: []shareStatus{}, Peers: []peerStatus{}}

	s.mu.RLock()
	out.MinFreeBytes = s.cfg.MinFreeBytes()
	for _, sc := range s.cfg.Shares {
		st := shareStatus{Name: sc.Name, Path: sc.Path, ReadOnly: sc.ReadOnly}
		if sh := s.shares[sc.Name]; sh != nil {
			st.Online = true
			st.FreeBytes, _ = sh.FreeBytes()
			if items, err := sh.ListTrash(); err == nil {
				st.TrashCount = len(items)
			}
		}
		out.Shares = append(out.Shares, st)
	}
	s.mu.RUnlock()

	if s.opt.Reach != nil {
		if peers, err := s.opt.Reach(r.Context()); err == nil {
			out.PolicyKnown = true
			for _, p := range peers {
				out.Peers = append(out.Peers, peerStatus{Peer: p, HasAccess: s.hasAnyAccess(p.UserID, p.Login)})
			}
		}
	}
	writeJSON(w, out)
}

func (s *Server) adminActivity(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if err != nil || n <= 0 || n > 5000 {
		n = 200
	}
	if s.opt.Activity == nil {
		writeJSON(w, []activity.Entry{})
		return
	}
	entries, err := s.opt.Activity.Recent(n)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, entries)
}

func (s *Server) adminTrash(w http.ResponseWriter, r *http.Request) {
	sh := s.adminShare(w, r)
	if sh == nil {
		return
	}
	items, err := sh.ListTrash()
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, items)
}

func (s *Server) adminRestore(w http.ResponseWriter, r *http.Request) {
	sh := s.adminShare(w, r)
	if sh == nil {
		return
	}
	p, err := sh.RestoreTrash(r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	s.record(r, activity.Entry{Action: "restore", Share: sh.Name, Path: p})
	writeJSON(w, map[string]string{"path": p})
}

func (s *Server) adminPurge(w http.ResponseWriter, r *http.Request) {
	sh := s.adminShare(w, r)
	if sh == nil {
		return
	}
	if err := sh.DeleteTrash(r.PathValue("id")); err != nil {
		storeError(w, err)
		return
	}
	s.record(r, activity.Entry{Action: "purge", Share: sh.Name, Note: "trash item " + r.PathValue("id"), Time: time.Now()})
	w.WriteHeader(http.StatusNoContent)
}
