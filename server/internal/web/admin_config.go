package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Sirsyorrz/tailshare/server/internal/activity"
	"github.com/Sirsyorrz/tailshare/server/internal/config"
)

func (s *Server) adminConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/config", s.withAdmin(s.adminGetConfig))
	mux.HandleFunc("PUT /api/admin/config", s.withAdmin(s.adminPutConfig))
	mux.HandleFunc("GET /api/admin/browse", s.withAdmin(s.adminBrowse))
}

// Configure sets where admin config edits are saved and which folders
// shares may live in. Call before serving.
func (s *Server) Configure(configPath string, roots []string) {
	s.opt.ConfigPath = configPath
	s.opt.Roots = roots
}

// Reload re-reads the config file, e.g. after SIGHUP.
func (s *Server) Reload() error {
	s.cfgWrite.Lock()
	defer s.cfgWrite.Unlock()
	cfg, err := config.Load(s.opt.ConfigPath)
	if err != nil {
		return err
	}
	s.Load(cfg)
	return nil
}

func (s *Server) adminGetConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	w.Header().Set("ETag", `"`+cfg.Version()+`"`)
	writeJSON(w, struct {
		Config  *config.Config `json:"config"`
		Version string         `json:"version"`
		Roots   []string       `json:"roots"`
	}{cfg, cfg.Version(), s.roots()})
}

// adminPutConfig replaces the whole config. The client must send the
// version it edited (If-Match) so two admins can't silently overwrite each
// other, and the result must keep the caller an admin.
func (s *Server) adminPutConfig(w http.ResponseWriter, r *http.Request) {
	if s.opt.ConfigPath == "" {
		httpError(w, http.StatusConflict, "this server has no config file to write")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		httpError(w, http.StatusBadRequest, "bad request body")
		return
	}
	next, err := config.Parse(body)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid config: "+err.Error())
		return
	}
	for _, sh := range next.Shares {
		if err := s.checkShareFolder(sh.Path); err != nil {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("share %q: %v", sh.Name, err))
			return
		}
	}
	u := userOf(r)
	if !next.IsAdmin(u.ID, u.Login) {
		httpError(w, http.StatusBadRequest, "this change would remove your own admin access")
		return
	}

	s.cfgWrite.Lock()
	defer s.cfgWrite.Unlock()
	s.mu.RLock()
	cur := s.cfg.Version()
	s.mu.RUnlock()
	if strings.Trim(r.Header.Get("If-Match"), `"`) != cur {
		httpError(w, http.StatusPreconditionFailed, "the config changed since you loaded it; reload and try again")
		return
	}
	if err := next.Save(s.opt.ConfigPath); err != nil {
		storeError(w, err)
		return
	}
	s.Load(next)
	s.record(r, activity.Entry{Action: "config", Note: fmt.Sprintf("%d share(s)", len(next.Shares))})
	w.Header().Set("ETag", `"`+next.Version()+`"`)
	writeJSON(w, map[string]string{"version": next.Version()})
}

// roots are the folders shares may live in, with symlinks resolved.
func (s *Server) roots() []string {
	out := []string{}
	for _, r := range s.opt.Roots {
		if real, err := filepath.EvalSymlinks(r); err == nil {
			out = append(out, real)
		}
	}
	return out
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveInRoots resolves symlinks first so a link inside a root can't point
// a share somewhere else.
func (s *Server) resolveInRoots(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", errors.New("path must be absolute")
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", errors.New("folder does not exist")
	}
	for _, root := range s.roots() {
		if within(real, root) {
			return real, nil
		}
	}
	return "", errors.New("folder is outside the allowed roots")
}

func (s *Server) checkShareFolder(p string) error {
	real, err := s.resolveInRoots(p)
	if err != nil {
		return err
	}
	st, err := os.Stat(real)
	if err != nil || !st.IsDir() {
		return errors.New("not a folder")
	}
	return nil
}

type browseResult struct {
	Path   string   `json:"path"`
	Parent string   `json:"parent,omitempty"`
	Dirs   []string `json:"dirs"`
}

// adminBrowse lists subfolders for the share folder picker. With no path it
// returns the roots themselves.
func (s *Server) adminBrowse(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeJSON(w, browseResult{Dirs: s.roots()})
		return
	}
	real, err := s.resolveInRoots(p)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	des, err := os.ReadDir(real)
	if err != nil {
		httpError(w, http.StatusBadRequest, "can't read folder")
		return
	}
	hidden := r.URL.Query().Get("hidden") == "1"
	out := browseResult{Path: real, Dirs: []string{}}
	for _, de := range des {
		if !hidden && strings.HasPrefix(de.Name(), ".") {
			continue
		}
		full := filepath.Join(real, de.Name())
		if st, err := os.Stat(full); err == nil && st.IsDir() {
			out.Dirs = append(out.Dirs, de.Name())
		}
	}
	sort.Strings(out.Dirs)
	if parent := filepath.Dir(real); parent != real {
		if _, err := s.resolveInRoots(parent); err == nil {
			out.Parent = parent
		}
	}
	writeJSON(w, out)
}
