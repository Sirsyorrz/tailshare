package store

import (
	"errors"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// metaDir lives at the top of every share so uploads and trash stay on the
// same filesystem as their destination (renames are atomic, no multi-GB
// copies). Clients can never address it.
const metaDir = ".tailshare"

var (
	ErrBadPath = errors.New("bad path")
	ErrBadName = errors.New("bad file name")
)

// cleanRel turns a client path ("a/b", "/a/b/", "") into a root-relative
// path. It rejects anything that is not a plain downward path rather than
// normalising it, so "a/../b" is an error, not "b".
func cleanRel(p string) (string, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return ".", nil
	}
	if !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00") {
		return "", ErrBadPath
	}
	parts := strings.Split(p, "/")
	for _, s := range parts {
		if s == "" || s == "." || s == ".." {
			return "", ErrBadPath
		}
	}
	if isMeta(parts[0]) {
		return "", ErrBadPath
	}
	return p, nil
}

func isMeta(name string) bool { return strings.EqualFold(name, metaDir) }

var windowsDevices = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM0": true, "COM1": true, "COM2": true, "COM3": true, "COM4": true,
	"COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT0": true, "LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true,
	"LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// validName checks a single new file or folder name. Rules are the union of
// Linux and Windows limits, since shares get mounted on both.
func validName(n string) error {
	if n == "" || len(n) > 255 || !utf8.ValidString(n) || n == "." || n == ".." {
		return ErrBadName
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\<>:"|?*`, r) {
			return ErrBadName
		}
	}
	if strings.HasSuffix(n, " ") || strings.HasSuffix(n, ".") {
		return ErrBadName
	}
	base, _, _ := strings.Cut(n, ".")
	if windowsDevices[strings.ToUpper(strings.TrimSpace(base))] {
		return ErrBadName
	}
	if strings.HasPrefix(strings.ToLower(n), metaDir) {
		return ErrBadName
	}
	return nil
}

func join(dir, name string) string {
	if dir == "." {
		return name
	}
	return path.Join(dir, name)
}

func numbered(name string, n int) string {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		stem, ext = name, ""
	}
	return stem + " (" + strconv.Itoa(n) + ")" + ext
}
