//go:build unix

package thumb

import "os"

// ffmpeg reads the already-opened (and so already confined) file through
// an inherited descriptor, never by path, so a symlink swapped in between
// the check and the read can't point it elsewhere.
func ffmpegInput(f *os.File, _ string) (string, []*os.File) {
	return "/dev/fd/3", []*os.File{f}
}
