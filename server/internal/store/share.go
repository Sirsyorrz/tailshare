package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
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
	Name     string
	dir      string
	root     *os.Root
	writable bool

	mu      sync.Mutex
	uploads map[string]*upload
	owners  map[string]Owner
}

type Entry struct {
	Name     string    `json:"name"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Owner    int64     `json:"-"`
}

func Open(name, dir string, writable bool) (*Share, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	s := &Share{Name: name, dir: dir, root: r, writable: writable, uploads: map[string]*upload{}, owners: map[string]Owner{}}
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
		s.loadOwners()
	}
	return s, nil
}

func (s *Share) Close() error { return s.root.Close() }

func (s *Share) Dir() string { return s.dir }

// RemoveMeta deletes a folder's .tailshare (unfinished uploads, trash,
// owners, thumbnails) once it no longer backs any share. Scoped to the
// folder, so a planted symlink can't point the removal elsewhere.
func RemoveMeta(dir string) error {
	r, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer r.Close()
	return r.RemoveAll(metaDir)
}

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
		e := Entry{Name: de.Name(), Dir: info.IsDir(), Size: info.Size(), Modified: info.ModTime()}
		if o, ok := s.ownerOf(join(rel, de.Name())); ok {
			e.Owner = o.ID
		}
		out = append(out, e)
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

func (s *Share) Mkdir(dir, name string, owner int64) error {
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
	p := join(rel, name)
	if err := s.root.Mkdir(p, 0o775); err != nil {
		return mapErr(err)
	}
	s.setOwner(p, Owner{ID: owner})
	return nil
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
	if err := s.root.Rename(src, target); err != nil {
		return mapErr(err)
	}
	s.putOwners(s.takeOwners(src), src, target)
	return nil
}

// RenameReplace is Rename that first moves an existing file at the
// destination to the trash. Editors save by writing a temporary file and
// renaming it over the original, so a mounted drive needs this.
func (s *Share) RenameReplace(from, newDir, newName, by string) error {
	dst, err := cleanRel(newDir)
	if err != nil {
		return err
	}
	if err := validName(newName); err != nil {
		return err
	}
	src, err := cleanRel(from)
	if err != nil {
		return err
	}
	target := join(dst, newName)
	if target != src {
		if st, err := s.root.Lstat(target); err == nil {
			if st.IsDir() {
				return ErrIsDir
			}
			if _, err := s.root.Lstat(src); err != nil {
				return mapErr(err)
			}
			if err := s.toTrash(target, by); err != nil {
				return err
			}
		}
	}
	return s.Rename(from, newDir, newName)
}

// SetModTime sets a file's modification time, so copies keep their dates.
func (s *Share) SetModTime(p string, t time.Time) error {
	rel, err := cleanRel(p)
	if err != nil {
		return err
	}
	if rel == "." {
		return ErrBadPath
	}
	return mapErr(s.root.Chtimes(rel, t, t))
}

func randID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Stat reports whether p exists inside the share.
func (s *Share) Stat(p string) (fs.FileInfo, error) {
	rel, err := cleanRel(p)
	if err != nil {
		return nil, err
	}
	st, err := s.root.Stat(rel)
	return st, mapErr(err)
}
