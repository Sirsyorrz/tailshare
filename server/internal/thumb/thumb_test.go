package thumb

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeImage(t *testing.T, name string, w, h int) *os.File {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.RGBA{255, 0, 0, 255})
	}
	p := filepath.Join(t.TempDir(), name)
	f, _ := os.Create(p)
	png.Encode(f, img)
	f.Seek(0, 0)
	t.Cleanup(func() { f.Close() })
	return f
}

func TestImageFits(t *testing.T) {
	f := writeImage(t, "a.png", 1000, 500)
	b, err := Make(context.Background(), f, "a.png", f.Name(), 320)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != 320 || img.Bounds().Dy() != 160 {
		t.Fatalf("got %v %v", img.Bounds(), err)
	}
}

func TestRejectsHugeImage(t *testing.T) {
	// A header claiming 20000x20000: DecodeConfig sees it without decoding.
	var buf bytes.Buffer
	png.Encode(&buf, image.NewGray(image.Rect(0, 0, 11000, 11000)))
	p := filepath.Join(t.TempDir(), "big.png")
	os.WriteFile(p, buf.Bytes(), 0o644)
	f, _ := os.Open(p)
	defer f.Close()
	if _, err := Make(context.Background(), f, "big.png", p, 320); err == nil {
		t.Fatal("huge image decoded")
	}
}

func TestOrientation(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	src.Set(0, 0, color.RGBA{255, 0, 0, 255})
	out := orient(src, 6) // rotate 90 cw: top-left ends up top-right
	if out.Bounds().Dx() != 2 || out.Bounds().Dy() != 4 {
		t.Fatalf("bounds %v", out.Bounds())
	}
	if r, _, _, _ := out.At(1, 0).RGBA(); r == 0 {
		t.Fatal("pixel not rotated to top-right")
	}
}

func TestVideo(t *testing.T) {
	if ffmpegPath() == "" {
		t.Skip("no ffmpeg")
	}
	p := filepath.Join(t.TempDir(), "clip.mp4")
	if err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=640x360:rate=10", p).Run(); err != nil {
		t.Skip("can't make test video:", err)
	}
	f, _ := os.Open(p)
	defer f.Close()
	b, err := Make(context.Background(), f, "clip.mp4", p, 320)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := jpeg.Decode(bytes.NewReader(b))
	if img.Bounds().Dx() != 320 {
		t.Fatalf("video thumb %v", img.Bounds())
	}
}
