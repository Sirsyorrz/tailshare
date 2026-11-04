package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	Reach func(context.Context) (reach.Report, error)
	// ConfigPath is where admin edits are saved. Empty disables editing.
	ConfigPath string
	// Roots are the only folders shares may be created in.
	Roots []string
	// RootsFile lists more roots, one per line, re-read on use.
	RootsFile string
	Version   string
	// ThumbCache is where read-only shares cache previews. Empty means
	// they don't.
	ThumbCache string
}

type Server struct {
	opt   Options
	hosts atomic.Pointer[map[string]bool]

	// cfgWrite serialises config file writes and reloads.
	cfgWrite sync.Mutex

	mu     sync.RWMutex
	cfg    *config.Config
	shares map[string]*store.Share

	changes *changes

	seen sync.Map
	// lastSeen maps a source address to when it last made a request. It
	// settles "can they connect" when the policy check can't tell.
	lastSeen sync.Map
}

// recentlySeen is how long a request counts as proof that a device can
// connect, in case the policy check gets a rule form wrong.
const recentlySeen = 15 * time.Minute

func (s *Server) seenAt(ips []string) time.Time {
	var latest time.Time
	for _, ip := range ips {
		if v, ok := s.lastSeen.Load(ip); ok && v.(time.Time).After(latest) {
			latest = v.(time.Time)
		}
	}
	return latest
}

// observedReach overrides the policy check with what actually happened.
func (s *Server) observedReach(p *reach.Peer) (seen time.Time) {
	seen = s.seenAt(p.IPs)
	if !p.CanReach && !seen.IsZero() && time.Since(seen) < recentlySeen {
		p.CanReach = true
	}
	return seen
}

func New(opt Options) *Server {
	s := &Server{opt: opt, shares: map[string]*store.Share{}, cfg: &config.Config{}, changes: newChanges()}
	s.SetHosts(opt.Hosts...)
	return s
}

// SetHosts replaces the names the server answers to, e.g. after the node
// was renamed in the Tailscale admin console.
func (s *Server) SetHosts(hosts ...string) {
	m := map[string]bool{}
	for _, h := range hosts {
		m[h] = true
	}
	s.hosts.Store(&m)
}

func (s *Server) isHost(h string) bool {
	return (*s.hosts.Load())[stripDefaultPort(h)]
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
		if sc.ReadOnly && s.opt.ThumbCache != "" {
			sum := sha256.Sum256([]byte(sameDir(sc.Path)))
			if err := sh.CacheThumbsIn(filepath.Join(s.opt.ThumbCache, hex.EncodeToString(sum[:8]))); err != nil {
				log.Printf("share %q: no preview cache: %v", sc.Name, err)
			}
		}
		next[sc.Name] = sh
	}
	s.mu.Lock()
	old := s.shares
	s.cfg, s.shares = cfg, next
	s.mu.Unlock()
	inUse := map[string]bool{}
	for _, sc := range cfg.Shares {
		inUse[sameDir(sc.Path)] = true
	}
	for _, sh := range old {
		sh.Close()
		// Only on a live change: a share removed while the server was down
		// leaves its folder alone, since we can't tell it apart from one
		// that's merely offline.
		if !inUse[sameDir(sh.Dir())] {
			if err := store.RemoveMeta(sh.Dir()); err != nil {
				log.Printf("cleaning up %s: %v", sh.Dir(), err)
			} else {
				log.Printf("removed tailshare data from %s (no longer shared)", sh.Dir())
			}
		}
	}
	s.changes.bump(true)
	log.Printf("loaded %d share(s)", len(next))
}

// Close releases every share's folder. Windows can't delete or rename a
// folder while it's held open.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sh := range s.shares {
		sh.Close()
	}
	s.shares = nil
}

// sameDir normalises a folder path so two spellings of it compare equal.
func sameDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

func (s *Server) Housekeep(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.mu.RLock()
		for _, sh := range s.shares {
			sh.PurgeUploads(7 * 24 * time.Hour)
			sh.PurgeTrash(30 * 24 * time.Hour)
			sh.PurgeThumbs(60 * 24 * time.Hour)
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
	rep, err := s.opt.Reach(ctx)
	if err != nil {
		log.Printf("policy check: %v", err)
		return
	}
	for _, p := range rep.Peers {
		policySays := p.CanReach
		s.observedReach(&p)
		key := p.Login + "|" + p.Node
		state := ""
		hasAccess := s.hasAnyAccess(p.UserID, p.Login)
		switch {
		case !policySays && p.CanReach:
			state = "mismatch"
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
		case "mismatch":
			rules, _ := json.Marshal(rep.Rules)
			log.Printf("policy check: %s (%s, %v) connected although no rule seemed to allow it; please report. rules=%s",
				p.Login, p.Node, p.IPs, rules)
		case "blocked":
			grant := reach.Grant(rep.Self)
			if !p.External {
				grant = strings.Replace(grant, `"autogroup:shared"`, strconv.Quote(p.Login), 1)
			}
			log.Printf("WARNING: %s (%s) has access to shares, but the tailnet policy blocks their device from port 443. "+
				"Add this to \"grants\" in the tailnet policy file: %s", p.Login, p.Node, grant)
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
	files := http.FileServerFS(ui)
	// Embedded files have no modtime, so nothing to validate against; the
	// UI is small and a stale mix of old and new modules after an upgrade
	// would break the page.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	// Lets the desktop app tell tailshare nodes apart from other web servers
	// on the tailnet.
	mux.HandleFunc("GET /api/hello", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"app": "tailshare", "version": s.opt.Version})
	})
	mux.HandleFunc("GET /api/changes", s.changesHandler)
	s.fileRoutes(mux)
	s.adminRoutes(mux)
	s.adminConfigRoutes(mux)
	// WebDAV uses methods (PROPFIND, MOVE...) the mux's method patterns
	// can't express next to "GET /", so it's split off first.
	return s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, davPrefix) || r.URL.Path+"/" == davPrefix {
			s.dav(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

type ctxKey struct{}

func userOf(r *http.Request) identity.User { return r.Context().Value(ctxKey{}).(identity.User) }

func (s *Server) record(r *http.Request, e activity.Entry) {
	u := userOf(r)
	e.UserID, e.Login, e.Node, e.External = u.ID, u.Login, u.Node, u.External
	s.opt.Activity.Add(e)
	s.changes.bump(false)
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
