package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type trashRecord struct {
	Path   string           `json:"path"`
	By     string           `json:"by"`
	At     time.Time        `json:"at"`
	Dir    bool             `json:"dir"`
	Size   int64            `json:"size"`
	Owners map[string]Owner `json:"owners,omitempty"`
}

type TrashItem struct {
	ID   string    `json:"id"`
	Path string    `json:"path"`
	By   string    `json:"by"`
	At   time.Time `json:"at"`
	Dir  bool      `json:"dir"`
	Size int64     `json:"size"`
}

var trashID = regexp.MustCompile(`^[0-9]+-[0-9a-f]{8}$`)

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
	st, err := s.root.Lstat(rel)
	if err != nil {
		return mapErr(err)
	}
	size := st.Size()
	if st.IsDir() {
		size = treeSize(s.root, rel)
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + randID(4)
	slot := trashDir + "/" + id
	if err := s.data.Mkdir(slot, 0o700); err != nil {
		return mapErr(err)
	}
	if err := moveAcross(s.root, rel, s.data, slot+"/"+path.Base(rel)); err != nil {
		s.data.Remove(slot)
		return mapErr(err)
	}
	rec := trashRecord{Path: rel, By: by, At: time.Now(), Dir: st.IsDir(), Size: size, Owners: s.takeOwners(rel)}
	b, _ := json.Marshal(rec)
	return s.data.WriteFile(slot+".json", b, 0o600)
}

func (s *Share) readTrash(id string) (trashRecord, error) {
	var rec trashRecord
	if !trashID.MatchString(id) {
		return rec, ErrNotFound
	}
	b, err := s.data.ReadFile(trashDir + "/" + id + ".json")
	if err != nil {
		return rec, mapErr(err)
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, err
	}
	return rec, nil
}

func (s *Share) ListTrash() ([]TrashItem, error) {
	des, err := fs.ReadDir(s.data.FS(), trashDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []TrashItem{}, nil
		}
		return nil, err
	}
	out := []TrashItem{}
	for _, de := range des {
		id, ok := strings.CutSuffix(de.Name(), ".json")
		if !ok {
			continue
		}
		rec, err := s.readTrash(id)
		if err != nil {
			continue
		}
		out = append(out, TrashItem{ID: id, Path: rec.Path, By: rec.By, At: rec.At, Dir: rec.Dir, Size: rec.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// RestoreTrash puts an item back where it was, recreating missing parent
// folders. If the name is taken it comes back as "name (n).ext".
func (s *Share) RestoreTrash(id string) (string, error) {
	rec, err := s.readTrash(id)
	if err != nil {
		return "", err
	}
	slot := trashDir + "/" + id
	src := slot + "/" + path.Base(rec.Path)
	parent := path.Dir(rec.Path)
	if parent != "." {
		if err := s.root.MkdirAll(parent, 0o775); err != nil {
			return "", mapErr(err)
		}
	}
	base := path.Base(rec.Path)
	target := rec.Path
	for n := 1; ; n++ {
		if _, err := s.root.Lstat(target); errors.Is(err, fs.ErrNotExist) {
			break
		}
		if n > maxNumbered {
			return "", ErrExists
		}
		target = join(parent, numbered(base, n))
	}
	if err := moveAcross(s.data, src, s.root, target); err != nil {
		return "", mapErr(err)
	}
	s.putOwners(rec.Owners, rec.Path, target)
	s.data.Remove(slot)
	s.data.Remove(slot + ".json")
	return target, nil
}

func (s *Share) DeleteTrash(id string) error {
	if _, err := s.readTrash(id); err != nil {
		return err
	}
	slot := trashDir + "/" + id
	if err := s.data.RemoveAll(slot); err != nil {
		return mapErr(err)
	}
	return mapErr(s.data.Remove(slot + ".json"))
}

// PurgeTrash permanently removes trash entries older than maxAge.
func (s *Share) PurgeTrash(maxAge time.Duration) {
	items, err := s.ListTrash()
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, it := range items {
		if it.At.Before(cutoff) {
			s.DeleteTrash(it.ID)
		}
	}
}
