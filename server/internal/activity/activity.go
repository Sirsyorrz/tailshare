package activity

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

type Entry struct {
	Time     time.Time `json:"time"`
	UserID   int64     `json:"userId"`
	Login    string    `json:"login"`
	Node     string    `json:"node"`
	External bool      `json:"external,omitempty"`
	Action   string    `json:"action"`
	Share    string    `json:"share"`
	Path     string    `json:"path,omitempty"`
	To       string    `json:"to,omitempty"`
	Bytes    int64     `json:"bytes,omitempty"`
	Seconds  float64   `json:"seconds,omitempty"`
	Note     string    `json:"note,omitempty"`
}

// Log appends one JSON object per line. The file is opened O_APPEND and
// never rewritten, so entries can be added but not edited by the server.
type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Log{path: path, f: f}, nil
}

func (l *Log) Add(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b, _ := json.Marshal(e)
	log.Printf("activity %s", b)
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(append(b, '\n')); err != nil {
		log.Printf("activity log write failed: %v", err)
	}
}

// Recent returns up to n newest entries, newest first.
func (l *Log) Recent(n int) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ring := make([]Entry, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, e)
	}
	for i, j := 0, len(ring)-1; i < j; i, j = i+1, j-1 {
		ring[i], ring[j] = ring[j], ring[i]
	}
	return ring, sc.Err()
}

func (l *Log) Close() error { return l.f.Close() }
