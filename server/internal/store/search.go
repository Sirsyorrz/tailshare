package store

import (
	"context"
	"io/fs"
	"path"
	"strings"
)

type Hit struct {
	Entry
	// Path is relative to the share root.
	Path string `json:"path"`
}

// Search walks below p for names containing q (case-insensitive). It stops
// at limit hits or when ctx ends, reporting whether it stopped early.
func (s *Share) Search(ctx context.Context, p, q string, limit int) ([]Hit, bool, error) {
	rel, err := cleanRel(p)
	if err != nil {
		return nil, false, err
	}
	if err := s.dirExists(rel); err != nil {
		return nil, false, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return []Hit{}, false, nil
	}
	hits := []Hit{}
	truncated := false
	err = fs.WalkDir(s.root.FS(), rel, func(qp string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			truncated = true
			return fs.SkipAll
		}
		if err != nil || qp == rel {
			return nil
		}
		if isMeta(d.Name()) && path.Dir(qp) == "." {
			return fs.SkipDir
		}
		if !strings.Contains(strings.ToLower(d.Name()), q) {
			return nil
		}
		info, err := s.root.Stat(qp)
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return nil
		}
		h := Hit{Entry: Entry{Name: d.Name(), Dir: info.IsDir(), Size: info.Size(), Modified: info.ModTime()}, Path: qp}
		if o, ok := s.ownerOf(qp); ok {
			h.Owner = o.ID
		}
		hits = append(hits, h)
		if len(hits) >= limit {
			truncated = true
			return fs.SkipAll
		}
		return nil
	})
	return hits, truncated, err
}
