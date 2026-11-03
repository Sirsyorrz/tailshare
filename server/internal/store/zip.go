package store

import (
	"archive/zip"
	"io"
	"io/fs"
	"path"
	"strings"
)

// ZipName is the folder name used inside the archive and for the file.
func (s *Share) ZipName(p string) (string, error) {
	rel, err := cleanRel(p)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return s.Name, nil
	}
	return path.Base(rel), nil
}

// WriteZip streams folder p as a zip and returns the file bytes written.
// Entries are stored, not deflated: footage and photos are already
// compressed, and storing keeps it as fast as a plain download. Symlinks are
// skipped.
func (s *Share) WriteZip(w io.Writer, p string) (int64, error) {
	rel, err := cleanRel(p)
	if err != nil {
		return 0, err
	}
	if err := s.dirExists(rel); err != nil {
		return 0, err
	}
	base, _ := s.ZipName(p)
	zw := zip.NewWriter(w)
	var total int64
	err = fs.WalkDir(s.root.FS(), rel, func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if rel == "." && isMeta(strings.SplitN(q, "/", 2)[0]) {
			return fs.SkipDir
		}
		name := base
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
	if err != nil {
		return total, err
	}
	return total, zw.Close()
}
