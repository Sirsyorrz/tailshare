//go:build unix

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func linkat(sd *os.File, sn string, dd *os.File, dn string) error {
	if err := unix.Linkat(int(sd.Fd()), sn, int(dd.Fd()), dn, 0); err != nil {
		return &os.LinkError{Op: "link", Old: sn, New: dn, Err: err}
	}
	return nil
}

func renameat(sd *os.File, sn string, dd *os.File, dn string) error {
	if err := unix.Renameat(int(sd.Fd()), sn, int(dd.Fd()), dn); err != nil {
		return &os.LinkError{Op: "rename", Old: sn, New: dn, Err: err}
	}
	return nil
}

func crossDevice(err error) bool { return errors.Is(err, unix.EXDEV) }

func freeBytes(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
