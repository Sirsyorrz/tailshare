//go:build !windows

package thumb

import "os/exec"

func hideWindow(*exec.Cmd) {}
