package store

import (
	"archive/zip"
	"io"
	"io/fs"
	"path"
	"strings"
)

// ZipName is the archive name (without .zip) for a set of paths: the item
// itself for one, otherwise the folder they're in.
func (s *Share) ZipName(paths ...string) (string, error) {
	if len(paths) == 0 {
		return "", ErrBadPath
	}
	rels := make([]string, len(paths))
	for i, p := range paths {
		rel, err := cleanRel(p)
		if err != nil {
			return "", err
		}
		rels[i] = rel
	}
	pick := rels[0]
	if len(rels) > 1 {
		pick = path.Dir(rels[0])
		for _, r := range rels[1:] {
			if path.Dir(r) != pick {
				return s.Name, nil
			}
		}
	}
	if pick == "." {
		return s.Name, nil
	}
	return path.Base(pick), nil
}

// WriteZip streams the given files and folders as one zip and returns the
// file bytes written. A single folder becomes the archive's top folder;
// several items sit side by side at the top. Entries are stored, not
// deflated: footage and photos are already compressed, and storing keeps
// it as fast as a plain download. Symlinks are skipped.
func (s *Share) WriteZip(w io.Writer, paths ...string) (int64, error) {
	if len(paths) == 0 {
		return 0, ErrBadPath
	}
	rels := make([]string, len(paths))
	for i, p := range paths {
		rel, err := cleanRel(p)
		if err != nil {
			return 0, err
		}
		if _, err := s.root.Stat(rel); err != nil {
			return 0, mapErr(err)
		}
		rels[i] = rel
	}
	zw := zip.NewWriter(w)
	used := map[string]bool{}
	var total int64
	for _, rel := range rels {
		top := path.Base(rel)
		if rel == "." {
			top = s.Name
		}
		// Items from different folders can share a name.
		for n := 1; used[top]; n++ {
			top = numbered(path.Base(rel), n)
		}
		used[top] = true
		n, err := s.addToZip(zw, rel, top)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, zw.Close()
}

func (s *Share) addToZip(zw *zip.Writer, rel, top string) (int64, error) {
	var total int64
	err := fs.WalkDir(s.root.FS(), rel, func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if isMeta(path.Base(q)) && (path.Dir(q) == "." || q == metaDir) {
			return fs.SkipDir
		}
		name := top
		if q != rel {
			if rel == "." {
				name += "/" + q
			} else {
				name += "/" + strings.TrimPrefix(q, rel+"/")
			}
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		switch {
		case d.IsDir():
			h, _ := zip.FileInfoHeader(info)
			h.Name = name + "/"
			_, err := zw.CreateHeader(h)
			return err
		case d.Type().IsRegular():
			h, _ := zip.FileInfoHeader(info)
			h.Name = name
			h.Method = zip.Store
			dst, err := zw.CreateHeader(h)
			if err != nil {
				return err
			}
			f, err := s.root.Open(q)
			if err != nil {
				return nil
			}
			n, err := io.Copy(dst, f)
			f.Close()
			total += n
			return err
		}
		return nil
	})
	return total, err
}
