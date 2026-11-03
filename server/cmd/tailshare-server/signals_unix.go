//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func notifyReload(c chan os.Signal) { signal.Notify(c, syscall.SIGHUP) }
func notifyDump(c chan os.Signal)   { signal.Notify(c, syscall.SIGUSR1) }
