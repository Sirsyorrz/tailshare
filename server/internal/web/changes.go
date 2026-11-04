package web

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// changes lets clients wait for something to happen instead of polling
// every view. Counters start at the boot time so a restarted server never
// repeats a value a client is holding.
type changes struct {
	mu       sync.Mutex
	config   uint64
	activity uint64
	wake     chan struct{}
}

func newChanges() *changes {
	n := uint64(time.Now().UnixNano())
	return &changes{config: n, activity: n, wake: make(chan struct{})}
}

func (c *changes) bump(config bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if config {
		c.config++
	} else {
		c.activity++
	}
	close(c.wake)
	c.wake = make(chan struct{})
}

func (c *changes) now() (config, activity uint64, wake <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config, c.activity, c.wake
}

// Below the desktop app's 30 s request timeout.
const changesWait = 20 * time.Second

// GET /api/changes?config=X&activity=Y answers as soon as either differs
// from what the caller has, or after changesWait with the same values.
// Activity covers every recorded action, so only admins get to watch it.
func (s *Server) changesHandler(w http.ResponseWriter, r *http.Request) {
	admin := s.isAdmin(userOf(r))
	q := r.URL.Query()
	haveCfg, _ := strconv.ParseUint(q.Get("config"), 10, 64)
	haveAct, _ := strconv.ParseUint(q.Get("activity"), 10, 64)
	changed := func(cfg, act uint64) bool { return cfg != haveCfg || (admin && act != haveAct) }

	timeout := time.NewTimer(changesWait)
	defer timeout.Stop()
	for {
		cfg, act, wake := s.changes.now()
		if changed(cfg, act) {
			s.writeChanges(w, admin, cfg, act)
			return
		}
		select {
		case <-wake:
		case <-timeout.C:
			s.writeChanges(w, admin, cfg, act)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) writeChanges(w http.ResponseWriter, admin bool, cfg, act uint64) {
	out := map[string]string{"config": strconv.FormatUint(cfg, 10)}
	if admin {
		out["activity"] = strconv.FormatUint(act, 10)
	}
	writeJSON(w, out)
}
