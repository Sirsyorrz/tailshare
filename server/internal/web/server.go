package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Sirsyorrz/tailshare/server/internal/activity"
	"github.com/Sirsyorrz/tailshare/server/internal/config"
	"github.com/Sirsyorrz/tailshare/server/internal/identity"
	"github.com/Sirsyorrz/tailshare/server/internal/reach"
	"github.com/Sirsyorrz/tailshare/server/internal/store"
)

//go:embed ui
var uiFS embed.FS

type Options struct {
	Resolver identity.Resolver
	// Hosts are the exact Host header values the server answers to; anything
	// else is rejected to stop DNS rebinding.
	Hosts    []string
	HTTPS    bool
	Activity *activity.Log
	// Reach reports which peers the Tailscale policy lets connect. Nil in
	// dev mode.
	Reach func(context.Context) ([]reach.Peer, error)
	// ConfigPath is where admin edits are saved. Empty disables editing.
	ConfigPath string
	// Roots are the only folders shares may be created in.
	Roots []string
	// RootsFile lists more roots, one per line, re-read on use.
	RootsFile string
	Version   string
}

type Server struct {
	opt   Options
	hosts map[string]bool

	// cfgWrite serialises config file writes and reloads.
	cfgWrite sync.Mutex

	mu     sync.RWMutex
	cfg    *config.Config
	shares map[string]*store.Share

	seen sync.Map
}

func New(opt Options) *Server {
	s := &Server{opt: opt, hosts: map[string]bool{}, shares: map[string]*store.Share{}, cfg: &config.Config{}}
	for _, h := range opt.Hosts {
		s.hosts[h] = true
	}
	return s
}

// Load swaps in a new config. Shares that fail to open are logged and left
// out rather than taking the whole server down.
func (s *Server) Load(cfg *config.Config) {
	next := map[string]*store.Share{}
	for _, sc := range cfg.Shares {
		sh, err := store.Open(sc.Name, sc.Path, !sc.ReadOnly)
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

// WatchReach logs when someone who has been given access can't connect
// because of the Tailscale policy, and when someone accepts the share but
// has no access yet. Each state is logged once, when it changes.
func (s *Server) WatchReach(ctx context.Context) {
	if s.opt.Reach == nil {
		return
	}
	last := map[string]string{}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.checkReach(ctx, last)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// checkReach is advisory, so a failure in it is logged rather than allowed
// to take down the file server.
func (s *Server) checkReach(ctx context.Context, last map[string]string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("policy check crashed: %v", r)
		}
	}()
	peers, err := s.opt.Reach(ctx)
	if err != nil {
		log.Printf("policy check: %v", err)
		return
	}
	for _, p := range peers {
		key := p.Login + "|" + p.Node
		state := ""
		hasAccess := s.hasAnyAccess(p.UserID, p.Login)
		switch {
		case hasAccess && !p.CanReach:
			state = "blocked"
		case p.External && !hasAccess && p.CanReach:
			state = "no-access"
		}
		if state == last[key] {
			continue
		}
		last[key] = state
		switch state {
		case "blocked":
			src := p.Login
			if p.External {
				src = "autogroup:shared"
			}
			log.Printf("WARNING: %s (%s) has access to shares, but the tailnet policy blocks their device from port 443. "+
				"Add a grant like {\"src\": [%q], \"dst\": [\"<this node>\"], \"ip\": [\"443\"]}.", p.Login, p.Node, src)
		case "no-access":
			log.Printf("%s (id:%d) can reach tailshare but has no access to any share yet", p.Login, p.UserID)
		}
	}
}

func (s *Server) hasAnyAccess(userID int64, login string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.IsAdmin(userID, login) && len(s.cfg.Shares) > 0 {
		return true
	}
	now := time.Now()
	for i := range s.cfg.Shares {
		if s.cfg.Shares[i].GrantFor(userID, login, now).Perms != 0 {
			return true
		}
	}
	return false
}

func (s *Server) Handler() http.Handler {
	ui, _ := fs.Sub(uiFS, "ui")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(ui))
	// Lets the desktop app tell tailshare nodes apart from other web servers
	// on the tailnet.
	mux.HandleFunc("GET /api/hello", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"app": "tailshare", "version": s.opt.Version})
	})
	s.fileRoutes(mux)
	s.adminRoutes(mux)
	s.adminConfigRoutes(mux)
	return s.guard(mux)
}

type ctxKey struct{}

func userOf(r *http.Request) identity.User { return r.Context().Value(ctxKey{}).(identity.User) }

func (s *Server) record(r *http.Request, e activity.Entry) {
	u := userOf(r)
	e.UserID, e.Login, e.Node, e.External = u.ID, u.Login, u.Node, u.External
	s.opt.Activity.Add(e)
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
