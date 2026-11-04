package store

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlyThumbsCachedOutsideShare(t *testing.T) {
	dir, cache := t.TempDir(), t.TempDir()
	f, err := os.Create(filepath.Join(dir, "a.png"))
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	f.Close()

	s, err := Open("t", dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CacheThumbsIn(cache); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Thumbnail(context.Background(), "a.png", 320); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, metaDir)); !os.IsNotExist(err) {
		t.Fatalf("read-only share was written to: %v", err)
	}
	cached, _ := filepath.Glob(filepath.Join(cache, "thumbs", "*", "*.jpg"))
	if len(cached) != 1 {
		t.Fatalf("want 1 cached preview, got %v", cached)
	}
}
