//go:build windows

package store

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Windows has no *at calls, so an opened parent is turned back into a path.
// It comes from the handle, so it is where the root actually opened.
func handlePath(f *os.File) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

func paths(sd *os.File, sn string, dd *os.File, dn string) (string, string, error) {
	s, err := handlePath(sd)
	if err != nil {
		return "", "", err
	}
	d, err := handlePath(dd)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(s, sn), filepath.Join(d, dn), nil
}

func linkat(sd *os.File, sn string, dd *os.File, dn string) error {
	s, d, err := paths(sd, sn, dd, dn)
	if err != nil {
		return err
	}
	return os.Link(s, d)
}

func renameat(sd *os.File, sn string, dd *os.File, dn string) error {
	s, d, err := paths(sd, sn, dd, dn)
	if err != nil {
		return err
	}
	return os.Rename(s, d)
}

func crossDevice(err error) bool { return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) }

func freeBytes(dir string) (int64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return int64(avail), nil
}
