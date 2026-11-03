//go:build windows

package thumb

import "os"

// Windows has no /dev/fd; the caller has verified realPath resolves inside
// the share.
func ffmpegInput(_ *os.File, realPath string) (string, []*os.File) {
	return realPath, nil
}
