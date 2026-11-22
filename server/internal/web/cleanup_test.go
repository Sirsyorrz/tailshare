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
	s := newServer(t, Options{Hosts: []string{host}})
	data := func(dir string) bool { _, err := os.Stat(s.dataDir(dir)); return err == nil }

	s.Load(&config.Config{Shares: []config.Share{{Name: "a", Path: a}, {Name: "b", Path: b}}})
	if !data(a) || !data(b) {
		t.Fatal("shares should have their data folder")
	}
	for _, dir := range []string{a, b} {
		if _, err := os.Stat(filepath.Join(dir, ".tailshare")); err == nil {
			t.Fatalf("data written into the shared folder %s", dir)
		}
	}
	// Renamed, same folder: kept.
	s.Load(&config.Config{Shares: []config.Share{{Name: "renamed", Path: a + string(filepath.Separator)}, {Name: "b", Path: b}}})
	if !data(a) {
		t.Fatal("renaming a share must not wipe its trash")
	}
	// Removed: cleaned, the user's files stay.
	s.Load(&config.Config{Shares: []config.Share{{Name: "b", Path: b}}})
	if data(a) {
		t.Error("data left behind for a folder that's no longer shared")
	}
	if _, err := os.Stat(filepath.Join(a, "keep.txt")); err != nil {
		t.Error("user file removed")
	}
	// Moved to another folder: the old one is cleaned.
	s.Load(&config.Config{Shares: []config.Share{{Name: "b", Path: c}}})
	if data(b) || !data(c) {
		t.Error("changing the folder should move the data folder")
	}
}

func TestLegacyDataMovedOut(t *testing.T) {
	a := t.TempDir()
	legacy := filepath.Join(a, ".tailshare")
	os.MkdirAll(filepath.Join(legacy, "trash", "1-0000abcd"), 0o700)
	os.WriteFile(filepath.Join(legacy, "trash", "1-0000abcd", "old.txt"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(legacy, "trash", "1-0000abcd.json"), []byte(`{"path":"old.txt","by":"x"}`), 0o600)
	os.WriteFile(filepath.Join(legacy, "owners.json"), []byte(`{"f.txt":{"id":7}}`), 0o600)
	os.MkdirAll(filepath.Join(legacy, "thumbs"), 0o700)
	s := newServer(t, Options{Hosts: []string{host}})
	s.Load(&config.Config{Shares: []config.Share{{Name: "a", Path: a}}})

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("old data folder still in the share: %v", err)
	}
	d := s.dataDir(a)
	for _, p := range []string{"owners.json", "trash/1-0000abcd/old.txt", "trash/1-0000abcd.json"} {
		if _, err := os.Stat(filepath.Join(d, p)); err != nil {
			t.Errorf("%s not moved: %v", p, err)
		}
	}
}

func TestLegacySymlinkNotFollowed(t *testing.T) {
	a, victim := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(victim, "owners.json"), []byte("{}"), 0o644)
	if err := os.Symlink(victim, filepath.Join(a, ".tailshare")); err != nil {
		t.Skip(err)
	}
	s := newServer(t, Options{Hosts: []string{host}})
	s.Load(&config.Config{Shares: []config.Share{{Name: "a", Path: a}}})
	s.Load(&config.Config{})
	if _, err := os.Stat(filepath.Join(victim, "owners.json")); err != nil {
		t.Fatal("followed a symlink out of the share")
	}
}
