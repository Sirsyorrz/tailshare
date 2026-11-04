//go:build windows

package thumb

import (
	"os/exec"
	"syscall"
)

// The server is built as a GUI program with no console, so without this
// every ffmpeg run opens its own console window.
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
