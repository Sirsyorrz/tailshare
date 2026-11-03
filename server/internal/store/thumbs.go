package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sirsyorrz/tailshare/server/internal/thumb"
)

const thumbsDir = metaDir + "/thumbs"

var ErrNoPreview = errors.New("no preview")

// Thumbnail returns a cached JPEG preview of the file at p, making it if
// needed. Files that can't be previewed are remembered with an empty cache
// entry so they aren't retried on every listing.
func (s *Share) Thumbnail(ctx context.Context, p string, size int) ([]byte, time.Time, error) {
	f, st, err := s.OpenFile(p)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	if !thumb.Supported(st.Name()) {
		return nil, time.Time{}, ErrNoPreview
	}
	rel, _ := cleanRel(p)
	sum := sha256.Sum256(fmt.Appendf(nil, "%d|%s|%d|%d|%d", thumb.Version, rel, st.Size(), st.ModTime().UnixNano(), size))
	key := hex.EncodeToString(sum[:12])
	cached := thumbsDir + "/" + key[:2] + "/" + key + ".jpg"
	if b, err := s.readMeta(cached); err == nil {
		if len(b) == 0 {
			return nil, time.Time{}, ErrNoPreview
		}
		return b, st.ModTime(), nil
	}

	real, err := s.realPath(rel)
	if err != nil {
		return nil, time.Time{}, err
	}
	b, err := thumb.Make(ctx, f, st.Name(), real, size)
	if err != nil {
		if ctx.Err() == nil {
			s.writeMeta(cached, nil)
		}
		return nil, time.Time{}, ErrNoPreview
	}
	s.writeMeta(cached, b)
	return b, st.ModTime(), nil
}

// realPath is the file's OS path, verified to resolve inside the share.
// Only ffmpeg on Windows uses it; everything else goes through root.
func (s *Share) realPath(rel string) (string, error) {
	base, err := filepath.EvalSymlinks(s.dir)
	if err != nil {
		return "", err
	}
	p, err := filepath.EvalSymlinks(filepath.Join(base, filepath.FromSlash(rel)))
	if err != nil {
		return "", mapErr(err)
	}
	r, err := filepath.Rel(base, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", ErrNotFound
	}
	return p, nil
}

func (s *Share) readMeta(p string) ([]byte, error) {
	f, err := s.root.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	b := make([]byte, st.Size())
	_, err = f.ReadAt(b, 0)
	if err != nil && st.Size() > 0 {
		return nil, err
	}
	return b, nil
}

// writeMeta is best effort: read-only shares have no meta dir and just
// don't cache.
func (s *Share) writeMeta(p string, b []byte) {
	if !s.writable {
		return
	}
	if err := s.root.MkdirAll(path.Dir(p), 0o700); err != nil {
		return
	}
	tmp := p + "." + randID(4) + ".tmp"
	f, err := s.root.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr != nil || cerr != nil {
		s.root.Remove(tmp)
		return
	}
	if err := s.root.Rename(tmp, p); err != nil {
		s.root.Remove(tmp)
	}
}

// PurgeThumbs drops cached previews not made within maxAge. Previews for
// files that were since changed or deleted are never read again, so age
// is enough.
func (s *Share) PurgeThumbs(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	fs.WalkDir(s.root.FS(), thumbsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
			s.root.Remove(p)
		}
		return nil
	})
}
