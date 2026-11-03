//go:build windows

package main

import "os"

// Windows has no SIGHUP/SIGUSR1; config changes go through the admin API.
func notifyReload(chan os.Signal) {}
func notifyDump(chan os.Signal)   {}
