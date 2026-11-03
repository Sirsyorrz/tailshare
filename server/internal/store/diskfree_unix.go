//go:build unix

package store

import "golang.org/x/sys/unix"

// FreeBytes is space available to unprivileged users on the share's disk.
func (s *Share) FreeBytes() (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(s.dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
