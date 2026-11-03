package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrNotDir   = errors.New("not a folder")
	ErrIsDir    = errors.New("is a folder")
)

const (
	uploadsDir = metaDir + "/uploads"
	trashDir   = metaDir + "/trash"
)

// Share is one shared folder. Every filesystem call goes through root, which
// the OS confines to the folder (no "..", no symlinks pointing outside).
type Share struct {
	Name string
	root *os.Root

	mu      sync.Mutex
	uploads map[string]*upload
}

type Entry struct {
	Name     string    `json:"name"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

func Open(name, dir string, writable bool) (*Share, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	s := &Share{Name: name, root: r, uploads: map[string]*upload{}}
	if writable {
		if err := r.MkdirAll(uploadsDir, 0o700); err != nil {
			r.Close()
			return nil, fmt.Errorf("share %s: %w", name, err)
		}
		if err := r.MkdirAll(trashDir, 0o700); err != nil {
			r.Close()
			return nil, fmt.Errorf("share %s: %w", name, err)
		}
		s.loadUploads()
	}
	return s, nil
}

func (s *Share) Close() error { return s.root.Close() }

// mapErr hides OS detail (including "path escapes from parent", which is
// what os.Root returns for a symlink out of the share) behind stable errors.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case errors.Is(err, fs.ErrExist):
		return ErrExists
	case errors.Is(err, ErrBadPath), errors.Is(err, ErrBadName):
		return err
	}
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Err.Error() == "path escapes from parent" {
		return ErrNotFound
	}
	return err
}

func (s *Share) List(p string) ([]Entry, error) {
	rel, err := cleanRel(p)
	if err != nil {
		return nil, err
	}
	f, err := s.root.Open(rel)
	if err != nil {
		return nil, mapErr(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, mapErr(err)
	}
	if !st.IsDir() {
		return nil, ErrNotDir
	}
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		if rel == "." && isMeta(de.Name()) {
			continue
		}
		// Stat through the root so symlinks are followed only when their
		// target is inside the share; anything else is silently omitted.
		info, err := s.root.Stat(join(rel, de.Name()))
		if err != nil {
			continue
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		out = append(out, Entry{Name: de.Name(), Dir: info.IsDir(), Size: info.Size(), Modified: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Share) OpenFile(p string) (*os.File, fs.FileInfo, error) {
	rel, err := cleanRel(p)
	if err != nil {
		return nil, nil, err
	}
	if rel == "." {
		return nil, nil, ErrIsDir
	}
	f, err := s.root.Open(rel)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, mapErr(err)
	}
	if st.IsDir() {
		f.Close()
		return nil, nil, ErrIsDir
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, ErrNotFound
	}
	return f, st, nil
}

func (s *Share) dirExists(rel string) error {
	st, err := s.root.Stat(rel)
	if err != nil {
		return mapErr(err)
	}
	if !st.IsDir() {
		return ErrNotDir
	}
	return nil
}

func (s *Share) Mkdir(dir, name string) error {
	rel, err := cleanRel(dir)
	if err != nil {
		return err
	}
	if err := validName(name); err != nil {
		return err
	}
	if err := s.dirExists(rel); err != nil {
		return err
	}
	return mapErr(s.root.Mkdir(join(rel, name), 0o775))
}

// Rename moves a file or folder within the share. newDir and newName
// describe the destination; existing targets are never replaced.
func (s *Share) Rename(from, newDir, newName string) error {
	src, err := cleanRel(from)
	if err != nil {
		return err
	}
	if src == "." {
		return ErrBadPath
	}
	dst, err := cleanRel(newDir)
	if err != nil {
		return err
	}
	if err := validName(newName); err != nil {
		return err
	}
	if err := s.dirExists(dst); err != nil {
		return err
	}
	if _, err := s.root.Lstat(src); err != nil {
		return mapErr(err)
	}
	target := join(dst, newName)
	if target == src {
		return nil
	}
	if dst == src || len(dst) > len(src) && dst[:len(src)+1] == src+"/" {
		return ErrBadPath
	}
	// os.Root has no rename-without-replace, so a same-name race between two
	// clients can still replace. Acceptable for now: both need Rename.
	if _, err := s.root.Lstat(target); err == nil {
		return ErrExists
	}
	return mapErr(s.root.Rename(src, target))
}

type trashRecord struct {
	Path string    `json:"path"`
	By   string    `json:"by"`
	At   time.Time `json:"at"`
}

// Delete moves a file or folder into the share's trash.
func (s *Share) Delete(p, by string) error {
	rel, err := cleanRel(p)
	if err != nil {
		return err
	}
	if rel == "." {
		return ErrBadPath
	}
	if _, err := s.root.Lstat(rel); err != nil {
		return mapErr(err)
	}
	return s.toTrash(rel, by)
}

func (s *Share) toTrash(rel, by string) error {
	id := strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + randID(4)
	slot := trashDir + "/" + id
	if err := s.root.Mkdir(slot, 0o700); err != nil {
		return mapErr(err)
	}
	if err := s.root.Rename(rel, slot+"/"+path.Base(rel)); err != nil {
		s.root.Remove(slot)
		return mapErr(err)
	}
	rec, _ := json.Marshal(trashRecord{Path: rel, By: by, At: time.Now()})
	return s.root.WriteFile(slot+".json", rec, 0o600)
}

// PurgeTrash permanently removes trash entries older than maxAge.
func (s *Share) PurgeTrash(maxAge time.Duration) {
	des, err := fs.ReadDir(s.root.FS(), trashDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		info, err := de.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		s.root.RemoveAll(trashDir + "/" + de.Name())
		s.root.Remove(trashDir + "/" + de.Name() + ".json")
	}
}

func randID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
