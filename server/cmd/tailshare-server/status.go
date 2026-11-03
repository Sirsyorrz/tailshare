package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// statusDoc is what the desktop app reads to show a server it didn't start
// itself (background hosting). Updated is refreshed every few seconds so a
// crashed server is recognisable by a stale file.
type statusDoc struct {
	Phase    string `json:"phase"`
	URL      string `json:"url,omitempty"`
	LoginURL string `json:"loginUrl,omitempty"`
	// Notice says why it's waiting (signed out, key expired, approval).
	Notice   string    `json:"notice,omitempty"`
	Warning  string    `json:"warning,omitempty"`
	Error    string    `json:"error,omitempty"`
	Hostname string    `json:"hostname"`
	Version  string    `json:"version"`
	PID      int       `json:"pid"`
	Updated  time.Time `json:"updated"`
}

type statusWriter struct {
	mu   sync.Mutex
	path string
	doc  statusDoc
}

// status is nil unless -status-file is set; its methods are nil-safe.
var status *statusWriter

func newStatusWriter(path string) *statusWriter {
	w := &statusWriter{path: path, doc: statusDoc{Version: version, PID: os.Getpid()}}
	go func() {
		for range time.Tick(5 * time.Second) {
			w.set(func(*statusDoc) {})
		}
	}()
	return w
}

func (w *statusWriter) set(f func(*statusDoc)) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	f(&w.doc)
	w.doc.Updated = time.Now().UTC()
	b, _ := json.Marshal(w.doc)
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		os.Rename(tmp, w.path)
	}
}

// openLogFile sends the log to path as well as stderr. A big old log is
// moved aside rather than growing forever.
func openLogFile(path string) error {
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		os.Rename(path, path+".old")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	return nil
}
