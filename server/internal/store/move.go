package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"strings"
)

// A share and its data live in different roots, often on different disks,
// so moving between them can't use os.Root.Rename. The parent folders are
// opened through their roots, which keeps them confined; only the final
// name reaches the OS call, and link/rename never follow it.

// ErrNoRoom means a move had to copy across disks and the destination
// doesn't have the space.
var ErrNoRoom = errors.New("not enough free space")

// copyMargin is left free on the destination disk by cross-disk copies.
const copyMargin = 1 << 30

func openParent(r *os.Root, rel string) (*os.File, string, error) {
	f, err := r.Open(path.Dir(rel))
	if err != nil {
		return nil, "", err
	}
	return f, path.Base(rel), nil
}

func across(a *os.Root, src string, b *os.Root, dst string, op func(sd *os.File, sn string, dd *os.File, dn string) error) error {
	sd, sn, err := openParent(a, src)
	if err != nil {
		return err
	}
	defer sd.Close()
	dd, dn, err := openParent(b, dst)
	if err != nil {
		return err
	}
	defer dd.Close()
	return op(sd, sn, dd, dn)
}

// linkAcross hard-links src in a to dst in b. It fails if dst exists.
func linkAcross(a *os.Root, src string, b *os.Root, dst string) error {
	return across(a, src, b, dst, linkat)
}

// renameAcross moves src in a to dst in b. Callers check dst is free first.
func renameAcross(a *os.Root, src string, b *os.Root, dst string) error {
	return across(a, src, b, dst, renameat)
}

// moveAcross is renameAcross that falls back to copy and delete when the
// roots are on different disks.
func moveAcross(a *os.Root, src string, b *os.Root, dst string) error {
	err := renameAcross(a, src, b, dst)
	if !crossDevice(err) {
		return err
	}
	if free, ferr := freeBytes(b.Name()); ferr == nil && treeSize(a, src)+copyMargin > free {
		return ErrNoRoom
	}
	if err := copyTree(a, src, b, dst); err != nil {
		b.RemoveAll(dst)
		return err
	}
	// The copy is complete, so a partial removal loses nothing.
	if err := a.RemoveAll(src); err != nil {
		log.Printf("moved %s to %s but couldn't remove the original: %v", src, dst, err)
	}
	return nil
}

func treeSize(r *os.Root, rel string) int64 {
	var n int64
	fs.WalkDir(r.FS(), rel, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// copyTree copies a file or folder without following symlinks; they are
// recreated as they were.
func copyTree(a *os.Root, src string, b *os.Root, dst string) error {
	return fs.WalkDir(a.FS(), src, func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		to := dst + strings.TrimPrefix(q, src)
		switch {
		case d.IsDir():
			return b.Mkdir(to, 0o775)
		case d.Type()&fs.ModeSymlink != 0:
			target, err := a.Readlink(q)
			if err != nil {
				return err
			}
			return b.Symlink(target, to)
		case d.Type().IsRegular():
			return copyFile(a, q, b, to)
		}
		return nil
	})
}

// copyFile never replaces an existing dst.
func copyFile(a *os.Root, src string, b *os.Root, dst string) error {
	in, err := a.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", src)
	}
	out, err := b.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		b.Remove(dst)
		return err
	}
	b.Chtimes(dst, st.ModTime(), st.ModTime())
	return nil
}
