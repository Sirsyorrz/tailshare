// Package thumb makes small JPEG previews of images and videos.
package thumb

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Version is part of cache keys; bump it when output changes.
const Version = 1

var ErrUnsupported = errors.New("no preview for this file type")

// maxPixels caps what gets decoded. Image headers are cheap to lie in, and
// a 50000x50000 PNG would need 10 GB of RAM.
const maxPixels = 120_000_000

var goImage = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true, ".tif": true, ".tiff": true}

// ffmpeg handles video and the image formats Go can't decode.
var viaFFmpeg = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true, ".wmv": true, ".flv": true,
	".mts": true, ".m2ts": true, ".ts": true, ".mpg": true, ".mpeg": true, ".3gp": true,
	".heic": true, ".heif": true, ".avif": true,
}

// Supported reports whether a preview can be attempted for name. Video
// needs ffmpeg on the server.
func Supported(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return goImage[ext] || (viaFFmpeg[ext] && ffmpegPath() != "")
}

var ffmpeg struct {
	sync.Mutex
	preferred string
	found     string
	checked   time.Time
}

// SetFFmpeg makes path win over the one on PATH. It may not exist yet: the
// desktop app passes where it would install ffmpeg, and the lookup below
// is repeated so an install takes effect without a restart.
func SetFFmpeg(path string) {
	ffmpeg.Lock()
	defer ffmpeg.Unlock()
	ffmpeg.preferred = path
	ffmpeg.checked = time.Time{}
}

func ffmpegPath() string {
	ffmpeg.Lock()
	defer ffmpeg.Unlock()
	if time.Since(ffmpeg.checked) < 30*time.Second {
		return ffmpeg.found
	}
	ffmpeg.checked = time.Now()
	ffmpeg.found = ""
	if ffmpeg.preferred != "" {
		if st, err := os.Stat(ffmpeg.preferred); err == nil && !st.IsDir() {
			ffmpeg.found = ffmpeg.preferred
			return ffmpeg.found
		}
	}
	ffmpeg.found, _ = exec.LookPath("ffmpeg")
	return ffmpeg.found
}

// HasFFmpeg reports whether video and HEIC/AVIF previews are possible.
func HasFFmpeg() bool { return ffmpegPath() != "" }

// sem keeps a folder full of 40 MP photos from pinning every core.
var sem = make(chan struct{}, 3)

// videoSem runs one ffmpeg at a time: footage often lives on a spinning
// disk, where parallel seeks across big files thrash it to 100%.
var videoSem = make(chan struct{}, 1)

// Make returns a JPEG no larger than size on its longest side. f must be
// the opened file; realPath is only used for ffmpeg on systems without
// /dev/fd.
func Make(ctx context.Context, f *os.File, name, realPath string, size int) ([]byte, error) {
	ext := strings.ToLower(filepath.Ext(name))
	slots := sem
	if viaFFmpeg[ext] {
		slots = videoSem
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-slots }()

	var img image.Image
	var err error
	rot := 1
	switch {
	case goImage[ext]:
		img, rot, err = decodeImage(f)
	case viaFFmpeg[ext] && ffmpegPath() != "":
		img, err = ffmpegFrame(ctx, f, realPath, size)
	default:
		return nil, ErrUnsupported
	}
	if err != nil {
		return nil, err
	}
	// Rotate after shrinking: it's a per-pixel copy.
	return encode(orient(fit(img, size), rot))
}

func decodeImage(f *os.File) (image.Image, int, error) {
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, 0, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, 0, errors.New("image too large to preview")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, 0, err
	}
	rot := 1
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		rot = exifOrientation(f)
	}
	return img, rot, nil
}

func fit(img image.Image, size int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= size && h <= size {
		return img
	}
	if w >= h {
		h, w = max(1, h*size/w), size
	} else {
		w, h = max(1, w*size/h), size
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// encode flattens transparency onto the UI's background colour; JPEG has
// no alpha and would otherwise turn it black.
func encode(img image.Image) ([]byte, error) {
	b := img.Bounds()
	flat := image.NewRGBA(b)
	draw.Draw(flat, b, &image.Uniform{color.RGBA{0x14, 0x19, 0x20, 0xff}}, image.Point{}, draw.Src)
	draw.Draw(flat, b, img, b.Min, draw.Over)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var errTimeout = errors.New("ffmpeg took too long")

// ffmpegFrame grabs a representative frame a few seconds in (the first
// frame is often black), falling back to the start for short clips. The
// first try decodes only the keyframe nearest the seek point, so ffmpeg
// reads a few MB instead of a whole GOP of 4K footage.
func ffmpegFrame(ctx context.Context, f *os.File, realPath string, size int) (image.Image, error) {
	img, err := runFFmpeg(ctx, f, realPath, size, "3", true)
	if err == nil {
		return img, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// A file that timed out once will time out again; don't read it twice.
	if errors.Is(err, errTimeout) {
		return nil, err
	}
	return runFFmpeg(ctx, f, realPath, size, "0", false)
}

func runFFmpeg(ctx context.Context, f *os.File, realPath string, size int, at string, keyframeOnly bool) (image.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	input, extra := ffmpegInput(f, realPath)
	scale := "scale='min(" + itoa(size) + ",iw)':'min(" + itoa(size) + ",ih)':force_original_aspect_ratio=decrease"
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if keyframeOnly {
		args = append(args, "-skip_frame", "nokey", "-noaccurate_seek")
	}
	args = append(args, "-ss", at, "-i", input,
		"-map", "0:v:0", "-frames:v", "1", "-vf", scale,
		"-f", "image2pipe", "-vcodec", "mjpeg", "-q:v", "4", "pipe:1")
	cmd := exec.CommandContext(ctx, ffmpegPath(), args...)
	cmd.ExtraFiles = extra
	hideWindow(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errTimeout
		}
		return nil, err
	}
	if out.Len() == 0 {
		return nil, ErrUnsupported
	}
	return jpeg.Decode(&out)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
