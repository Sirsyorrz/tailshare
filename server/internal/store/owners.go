package store

import (
	"encoding/json"
	"io/fs"
	"log"
	"strings"
)

const ownersFile = "owners.json"

// Owner records who put a file or folder into the share through tailshare.
// Files the host copies in directly have no owner.
type Owner struct {
	ID   int64 `json:"id"`
	Size int64 `json:"size,omitempty"`
}

func (s *Share) loadOwners() {
	s.owners = map[string]Owner{}
	b, err := s.data.ReadFile(ownersFile)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &s.owners); err != nil {
		log.Printf("share %s: ignoring corrupt %s: %v", s.Name, ownersFile, err)
		s.owners = map[string]Owner{}
	}
}

// saveOwnersLocked writes via rename so a crash never leaves a torn file.
// Caller holds s.mu.
func (s *Share) saveOwnersLocked() {
	b, _ := json.Marshal(s.owners)
	tmp := ownersFile + ".tmp"
	if err := s.data.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("share %s: saving owners: %v", s.Name, err)
		return
	}
	if err := s.data.Rename(tmp, ownersFile); err != nil {
		log.Printf("share %s: saving owners: %v", s.Name, err)
	}
}

func (s *Share) setOwner(rel string, o Owner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owners[rel] = o
	s.saveOwnersLocked()
}

func under(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// takeOwners removes and returns the records for rel and everything below it.
func (s *Share) takeOwners(rel string) map[string]Owner {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Owner{}
	for p, o := range s.owners {
		if under(p, rel) {
			out[p] = o
			delete(s.owners, p)
		}
	}
	if len(out) > 0 {
		s.saveOwnersLocked()
	}
	return out
}

// putOwners re-adds records taken from oldRoot under newRoot.
func (s *Share) putOwners(recs map[string]Owner, oldRoot, newRoot string) {
	if len(recs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for p, o := range recs {
		s.owners[newRoot+strings.TrimPrefix(p, oldRoot)] = o
	}
	s.saveOwnersLocked()
}

func (s *Share) ownerOf(rel string) (Owner, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.owners[rel]
	return o, ok
}

// OwnsAll reports whether user owns p and, for a folder, everything in it.
func (s *Share) OwnsAll(p string, user int64) bool {
	rel, err := cleanRel(p)
	if err != nil || rel == "." {
		return false
	}
	if o, ok := s.ownerOf(rel); !ok || o.ID != user {
		return false
	}
	st, err := s.root.Lstat(rel)
	if err != nil || !st.IsDir() {
		return err == nil
	}
	owned := true
	fs.WalkDir(s.root.FS(), rel, func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			owned = false
			return fs.SkipAll
		}
		if o, ok := s.ownerOf(q); !ok || o.ID != user {
			owned = false
			return fs.SkipAll
		}
		return nil
	})
	return owned
}

// Usage is the total size of files user uploaded that are still in the share.
func (s *Share) Usage(user int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, o := range s.owners {
		if o.ID == user {
			n += o.Size
		}
	}
	return n
}
