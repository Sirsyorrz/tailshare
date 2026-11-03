package store

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanRel(t *testing.T) {
	ok := map[string]string{"": ".", "/": ".", "a": "a", "/a/b/": "a/b", "a/.tailshare": "a/.tailshare"}
	for in, want := range ok {
		got, err := cleanRel(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"..", "a/../b", "a//b", "./a", `a\b`, "a\x00", ".tailshare", ".TailShare/uploads"} {
		if _, err := cleanRel(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"a.txt", "Clip 01.mov", "résumé.pdf", ".hidden"} {
		if err := validName(n); err != nil {
			t.Errorf("%q rejected", n)
		}
	}
	for _, n := range []string{"", ".", "..", "a/b", `a\b`, "con", "CON.txt", "lpt1.log", "x?", "trail.", "trail ", ".tailshare", strings.Repeat("a", 256)} {
		if err := validName(n); err == nil {
			t.Errorf("%q accepted", n)
		}
	}
}

func newShare(t *testing.T) (*Share, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open("t", dir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func doUpload(t *testing.T, s *Share, owner int64, name, body string, replace bool) string {
	t.Helper()
	u, err := s.CreateUpload(owner, "", name, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	half := len(body) / 2
	off, err := s.WriteChunk(u.ID, owner, 0, strings.NewReader(body[:half]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteChunk(u.ID, owner, off, strings.NewReader(body[half:])); err != nil {
		t.Fatal(err)
	}
	final, err := s.CompleteUpload(u.ID, owner, replace, "tester")
	if err != nil {
		t.Fatal(err)
	}
	return final
}

func TestUploadNoReplaceNumbers(t *testing.T) {
	s, dir := newShare(t)
	if got := doUpload(t, s, 1, "clip.mov", "first", false); got != "clip.mov" {
		t.Fatalf("got %q", got)
	}
	if got := doUpload(t, s, 1, "clip.mov", "second", false); got != "clip (1).mov" {
		t.Fatalf("got %q", got)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "clip.mov"))
	if string(b) != "first" {
		t.Fatalf("original overwritten: %q", b)
	}
	if left, _ := os.ReadDir(filepath.Join(dir, uploadsDir)); len(left) != 0 {
		t.Fatalf("upload leftovers: %d", len(left))
	}
}

func TestUploadReplaceTrashesOld(t *testing.T) {
	s, dir := newShare(t)
	doUpload(t, s, 1, "a.txt", "old", false)
	if got := doUpload(t, s, 1, "a.txt", "new", true); got != "a.txt" {
		t.Fatalf("got %q", got)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "new" {
		t.Fatalf("got %q", b)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, trashDir, "*", "a.txt"))
	if len(matches) != 1 {
		t.Fatalf("old file not in trash: %v", matches)
	}
}

func TestUploadGuards(t *testing.T) {
	s, _ := newShare(t)
	u, err := s.CreateUpload(1, "", "f.bin", 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteChunk(u.ID, 2, 0, strings.NewReader("ab")); !errors.Is(err, ErrNotFound) {
		t.Errorf("other owner: %v", err)
	}
	if _, err := s.WriteChunk(u.ID, 1, 3, strings.NewReader("ab")); !errors.Is(err, ErrOffset) {
		t.Errorf("bad offset: %v", err)
	}
	if _, err := s.WriteChunk(u.ID, 1, 0, strings.NewReader("abcdef")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
	if st, _ := s.UploadStatus(u.ID, 1); st.Offset != 0 {
		t.Errorf("oversize chunk not rolled back: %d", st.Offset)
	}
	if _, err := s.CompleteUpload(u.ID, 1, false, "x"); !errors.Is(err, ErrIncomplete) {
		t.Errorf("incomplete: %v", err)
	}
}

func TestUploadsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open("t", dir, true)
	u, _ := s.CreateUpload(1, "", "f.bin", 4)
	s.WriteChunk(u.ID, 1, 0, strings.NewReader("ab"))
	s.Close()

	s2, _ := Open("t", dir, true)
	defer s2.Close()
	st, err := s2.UploadStatus(u.ID, 1)
	if err != nil || st.Offset != 2 {
		t.Fatalf("got %+v %v", st, err)
	}
}

func TestSymlinkOutsideHidden(t *testing.T) {
	s, dir := newShare(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600)
	os.Symlink(outside, filepath.Join(dir, "escape"))
	os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "secret"))

	ents, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name == "escape" || e.Name == "secret" || e.Name == metaDir {
			t.Errorf("listed %q", e.Name)
		}
	}
	if _, _, err := s.OpenFile("secret"); err == nil {
		t.Error("opened symlink target outside share")
	}
	if _, err := s.List("escape"); err == nil {
		t.Error("listed symlinked dir outside share")
	}
}

func TestRenameAndDelete(t *testing.T) {
	s, dir := newShare(t)
	doUpload(t, s, 1, "a.txt", "x", false)
	doUpload(t, s, 1, "b.txt", "y", false)
	if err := s.Mkdir("", "sub", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("a.txt", "", "b.txt"); !errors.Is(err, ErrExists) {
		t.Errorf("rename over existing: %v", err)
	}
	if err := s.Rename("a.txt", "sub", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("sub", "sub", "inner"); !errors.Is(err, ErrBadPath) {
		t.Errorf("move into itself: %v", err)
	}
	if err := s.Delete("sub", "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub")); !os.IsNotExist(err) {
		t.Error("deleted folder still present")
	}
	if err := s.Delete("", "tester"); err == nil {
		t.Error("deleted share root")
	}
}

func TestOwnersFollowRenameDeleteRestore(t *testing.T) {
	s, _ := newShare(t)
	if err := s.Mkdir("", "mine", 5); err != nil {
		t.Fatal(err)
	}
	u, _ := s.CreateUpload(5, "mine", "a.bin", 3)
	s.WriteChunk(u.ID, 5, 0, strings.NewReader("abc"))
	if _, err := s.CompleteUpload(u.ID, 5, false, "five"); err != nil {
		t.Fatal(err)
	}
	if s.Usage(5) != 3 || !s.OwnsAll("mine", 5) || s.OwnsAll("mine", 6) {
		t.Fatalf("ownership after upload: usage=%d", s.Usage(5))
	}
	if err := s.Rename("mine", "", "moved"); err != nil {
		t.Fatal(err)
	}
	if !s.OwnsAll("moved", 5) || s.OwnsAll("mine", 5) {
		t.Fatal("ownership did not follow rename")
	}
	if err := s.Delete("moved", "five"); err != nil {
		t.Fatal(err)
	}
	if s.Usage(5) != 0 {
		t.Fatal("deleted files still count toward usage")
	}
	items, _ := s.ListTrash()
	if len(items) != 1 || items[0].Path != "moved" || !items[0].Dir || items[0].Size != 3 {
		t.Fatalf("trash: %+v", items)
	}
	s.Mkdir("", "moved", 9)
	got, err := s.RestoreTrash(items[0].ID)
	if err != nil || got != "moved (1)" {
		t.Fatalf("restore: %q %v", got, err)
	}
	if !s.OwnsAll("moved (1)", 5) || s.Usage(5) != 3 {
		t.Fatal("ownership not restored")
	}
	if items, _ := s.ListTrash(); len(items) != 0 {
		t.Fatal("restored item still in trash")
	}
}

func TestOwnsAllNeedsEverythingInside(t *testing.T) {
	s, dir := newShare(t)
	s.Mkdir("", "d", 5)
	os.WriteFile(filepath.Join(dir, "d", "host-copied.txt"), []byte("x"), 0o644)
	if s.OwnsAll("d", 5) {
		t.Error("folder with someone else's file counted as owned")
	}
}

func TestTrashIDValidated(t *testing.T) {
	s, _ := newShare(t)
	for _, id := range []string{"../owners", "..", "1-zz", ""} {
		if _, err := s.RestoreTrash(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: %v", id, err)
		}
	}
}

func TestPendingCountsUnfinished(t *testing.T) {
	s, _ := newShare(t)
	u, _ := s.CreateUpload(5, "", "a", 10)
	s.WriteChunk(u.ID, 5, 0, strings.NewReader("abcd"))
	s.CreateUpload(6, "", "b", 7)
	all, mine := s.Pending(5)
	if all != 6+7 || mine != 10 {
		t.Fatalf("all=%d mine=%d", all, mine)
	}
}

func TestZip(t *testing.T) {
	s, dir := newShare(t)
	os.MkdirAll(filepath.Join(dir, "f", "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "f", "a.txt"), []byte("aaa"), 0o644)
	os.WriteFile(filepath.Join(dir, "f", "sub", "b.txt"), []byte("bb"), 0o644)
	os.Symlink("/etc/passwd", filepath.Join(dir, "f", "link"))

	var buf bytes.Buffer
	n, err := s.WriteZip(&buf, "f")
	if err != nil || n != 5 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	want := "f/ f/a.txt f/sub/ f/sub/b.txt"
	if strings.Join(names, " ") != want {
		t.Fatalf("got %v", names)
	}

	buf.Reset()
	s.WriteZip(&buf, "")
	zr, _ = zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	for _, f := range zr.File {
		if strings.Contains(f.Name, metaDir) {
			t.Fatalf("zip of share root includes %s", f.Name)
		}
	}
}
