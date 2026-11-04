package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Sirsyorrz/tailshare/server/internal/config"
)

func TestRemovedShareIsCleanedUp(t *testing.T) {
	// All made before the server, so cleanup closes it before removing them.
	a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(a, "keep.txt"), []byte("x"), 0o644)
	meta := func(dir string) bool { _, err := os.Stat(filepath.Join(dir, ".tailshare")); return err == nil }
	s := newServer(t, Options{Hosts: []string{host}})

	s.Load(&config.Config{Shares: []config.Share{{Name: "a", Path: a}, {Name: "b", Path: b}}})
	if !meta(a) || !meta(b) {
		t.Fatal("shares should have their data folder")
	}
	// Renamed, same folder: kept.
	s.Load(&config.Config{Shares: []config.Share{{Name: "renamed", Path: a + string(filepath.Separator)}, {Name: "b", Path: b}}})
	if !meta(a) {
		t.Fatal("renaming a share must not wipe its trash")
	}
	// Removed: cleaned, the user's files stay.
	s.Load(&config.Config{Shares: []config.Share{{Name: "b", Path: b}}})
	if meta(a) {
		t.Error(".tailshare left behind in a folder that's no longer shared")
	}
	if _, err := os.Stat(filepath.Join(a, "keep.txt")); err != nil {
		t.Error("user file removed")
	}
	// Moved to another folder: the old one is cleaned.
	s.Load(&config.Config{Shares: []config.Share{{Name: "b", Path: c}}})
	if meta(b) || !meta(c) {
		t.Error("changing the folder should move the data folder")
	}
}

func TestCleanupDoesNotFollowSymlink(t *testing.T) {
	a, victim := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(victim, "precious"), []byte("x"), 0o644)
	s := newServer(t, Options{Hosts: []string{host}})
	s.Load(&config.Config{Shares: []config.Share{{Name: "a", Path: a}}})
	os.RemoveAll(filepath.Join(a, ".tailshare"))
	if err := os.Symlink(victim, filepath.Join(a, ".tailshare")); err != nil {
		t.Skip(err)
	}
	s.Load(&config.Config{})
	if _, err := os.Stat(filepath.Join(victim, "precious")); err != nil {
		t.Fatal("cleanup followed a symlink out of the share")
	}
}
