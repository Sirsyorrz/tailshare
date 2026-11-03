//go:build windows

package store

import "golang.org/x/sys/windows"

// FreeBytes is space available to this user on the share's disk.
func (s *Share) FreeBytes() (int64, error) {
	p, err := windows.UTF16PtrFromString(s.dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return int64(avail), nil
}
