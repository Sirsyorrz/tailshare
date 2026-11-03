package store

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	ErrOffset     = errors.New("offset mismatch")
	ErrTooLarge   = errors.New("more data than the declared size")
	ErrIncomplete = errors.New("upload incomplete")
)

const maxNumbered = 1000

type upload struct {
	mu sync.Mutex

	ID      string    `json:"id"`
	Dir     string    `json:"dir"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Owner   int64     `json:"owner"`
	Created time.Time `json:"created"`
}

type UploadInfo struct {
	ID     string `json:"id"`
	Dir    string `json:"dir"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Offset int64  `json:"offset"`
}

func partPath(id string) string { return uploadsDir + "/" + id + ".part" }
func metaPath(id string) string { return uploadsDir + "/" + id + ".json" }

func (s *Share) loadUploads() {
	des, err := fs.ReadDir(s.root.FS(), uploadsDir)
	if err != nil {
		return
	}
	for _, de := range des {
		id, ok := strings.CutSuffix(de.Name(), ".json")
		if !ok {
			continue
		}
		b, err := s.root.ReadFile(metaPath(id))
		if err != nil {
			continue
		}
		u := &upload{}
		if json.Unmarshal(b, u) != nil || u.ID != id {
			continue
		}
		s.uploads[id] = u
	}
}

func (s *Share) CreateUpload(owner int64, dir, name string, size int64) (UploadInfo, error) {
	rel, err := cleanRel(dir)
	if err != nil {
		return UploadInfo{}, err
	}
	if err := validName(name); err != nil {
		return UploadInfo{}, err
	}
	if size < 0 {
		return UploadInfo{}, ErrBadPath
	}
	if err := s.dirExists(rel); err != nil {
		return UploadInfo{}, err
	}
	u := &upload{ID: randID(16), Dir: rel, Name: name, Size: size, Owner: owner, Created: time.Now()}
	f, err := s.root.OpenFile(partPath(u.ID), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o664)
	if err != nil {
		return UploadInfo{}, mapErr(err)
	}
	f.Close()
	meta, _ := json.Marshal(u)
	if err := s.root.WriteFile(metaPath(u.ID), meta, 0o600); err != nil {
		s.root.Remove(partPath(u.ID))
		return UploadInfo{}, mapErr(err)
	}
	s.mu.Lock()
	s.uploads[u.ID] = u
	s.mu.Unlock()
	return UploadInfo{ID: u.ID, Dir: rel, Name: name, Size: size}, nil
}

// get returns the upload only to its owner; anyone else sees ErrNotFound so
// upload ids cannot be probed.
func (s *Share) get(id string, owner int64) (*upload, error) {
	s.mu.Lock()
	u := s.uploads[id]
	s.mu.Unlock()
	if u == nil || u.Owner != owner {
		return nil, ErrNotFound
	}
	return u, nil
}

func (s *Share) offset(u *upload) (int64, error) {
	st, err := s.root.Stat(partPath(u.ID))
	if err != nil {
		return 0, mapErr(err)
	}
	return st.Size(), nil
}

func (s *Share) UploadStatus(id string, owner int64) (UploadInfo, error) {
	u, err := s.get(id, owner)
	if err != nil {
		return UploadInfo{}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	off, err := s.offset(u)
	return UploadInfo{ID: u.ID, Dir: u.Dir, Name: u.Name, Size: u.Size, Offset: off}, err
}

// WriteChunk appends r at offset. Whatever arrives before a dropped
// connection is kept, so the client resumes from UploadStatus.
func (s *Share) WriteChunk(id string, owner int64, offset int64, r io.Reader) (int64, error) {
	u, err := s.get(id, owner)
	if err != nil {
		return 0, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	cur, err := s.offset(u)
	if err != nil {
		return 0, err
	}
	if offset != cur {
		return cur, ErrOffset
	}
	f, err := s.root.OpenFile(partPath(u.ID), os.O_WRONLY, 0)
	if err != nil {
		return cur, mapErr(err)
	}
	defer f.Close()
	if _, err := f.Seek(cur, io.SeekStart); err != nil {
		return cur, err
	}
	room := u.Size - cur
	n, err := io.Copy(f, io.LimitReader(r, room+1))
	if n > room {
		f.Truncate(cur)
		return cur, ErrTooLarge
	}
	return cur + n, err
}

// CompleteUpload moves the finished file into place and returns its final
// name. Without replace, a name clash picks "name (n).ext"; with replace,
// the existing file goes to trash first.
func (s *Share) CompleteUpload(id string, owner int64, replace bool, by string) (string, error) {
	u, err := s.get(id, owner)
	if err != nil {
		return "", err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	off, err := s.offset(u)
	if err != nil {
		return "", err
	}
	if off != u.Size {
		return "", ErrIncomplete
	}
	if err := s.dirExists(u.Dir); err != nil {
		return "", err
	}
	part := partPath(u.ID)
	name := u.Name
	target := join(u.Dir, name)

	if replace {
		if st, err := s.root.Lstat(target); err == nil {
			if st.IsDir() {
				return "", ErrIsDir
			}
			if err := s.toTrash(target, by); err != nil {
				return "", err
			}
		}
	}

	for n := 1; ; n++ {
		err = s.placeNoReplace(part, target)
		if !errors.Is(err, ErrExists) || n > maxNumbered {
			break
		}
		name = numbered(u.Name, n)
		target = join(u.Dir, name)
	}
	if err != nil {
		return "", err
	}
	s.forget(u.ID)
	return name, nil
}

// placeNoReplace uses a hard link because link(2) fails if the target exists,
// unlike rename(2), making the no-overwrite rule atomic.
func (s *Share) placeNoReplace(part, target string) error {
	err := s.root.Link(part, target)
	if err == nil {
		s.root.Remove(part)
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return ErrExists
	}
	// Filesystems without hard links: check then rename (racy, last resort).
	if _, lerr := s.root.Lstat(target); lerr == nil {
		return ErrExists
	}
	return mapErr(s.root.Rename(part, target))
}

func (s *Share) CancelUpload(id string, owner int64) error {
	u, err := s.get(id, owner)
	if err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	s.root.Remove(partPath(u.ID))
	s.forget(u.ID)
	return nil
}

func (s *Share) forget(id string) {
	s.root.Remove(metaPath(id))
	s.mu.Lock()
	delete(s.uploads, id)
	s.mu.Unlock()
}

// PurgeUploads drops unfinished uploads not written to for maxAge.
func (s *Share) PurgeUploads(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	s.mu.Lock()
	ids := make([]string, 0, len(s.uploads))
	for id := range s.uploads {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		st, err := s.root.Stat(partPath(id))
		if err == nil && st.ModTime().After(cutoff) {
			continue
		}
		s.root.Remove(partPath(id))
		s.forget(id)
	}
}
