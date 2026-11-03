package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Sirsyorrz/tailshare/server/internal/perm"
)

const defaultMinFreeGB = 5

type Config struct {
	// Admins are Tailscale login names or "id:<userid>".
	Admins []string `json:"admins"`
	// AllowExternalAdmins lets admins listed above use admin features from a
	// device shared in from another tailnet. Off by default: a share should
	// never be a path to administering the server.
	AllowExternalAdmins bool `json:"allowExternalAdmins,omitempty"`
	// MinFreeGB is free space every share's disk must keep after an upload.
	MinFreeGB *float64 `json:"minFreeGB,omitempty"`
	Shares    []Share  `json:"shares"`
}

type Share struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly,omitempty"`
	// Access keys are Tailscale login names (case-insensitive) or
	// "id:<userid>". Login names are display data on Tailscale's side, so
	// the id form is preferred once the GUI writes this file.
	Access map[string]perm.Set `json:"access"`
	// Expires ends a key's access after the given date ("2026-12-31",
	// inclusive, server local time) or RFC 3339 time.
	Expires map[string]string `json:"expires,omitempty"`
	// QuotaGB caps how much a key may upload into this share.
	QuotaGB map[string]float64 `json:"quotaGB,omitempty"`

	expires map[string]time.Time
}

var shareName = regexp.MustCompile(`^[a-z0-9_][a-z0-9_ ()-]{0,63}$`)

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) MinFreeBytes() int64 {
	gb := float64(defaultMinFreeGB)
	if c.MinFreeGB != nil {
		gb = *c.MinFreeGB
	}
	// Clamped so an absurd value can't overflow and wrap to "no floor".
	return int64(min(gb*1e9, 9e18))
}

func (c *Config) validate() error {
	if c.MinFreeGB != nil && *c.MinFreeGB < 0 {
		return fmt.Errorf("minFreeGB must not be negative")
	}
	seen := map[string]bool{}
	for i := range c.Shares {
		s := &c.Shares[i]
		if !shareName.MatchString(s.Name) {
			return fmt.Errorf("share %q: name must be lowercase a-z, 0-9, _, -, (), spaces", s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("share %q defined twice", s.Name)
		}
		seen[s.Name] = true
		if !filepath.IsAbs(s.Path) {
			return fmt.Errorf("share %q: path must be absolute", s.Name)
		}
		for k := range s.Access {
			if err := validKey(k); err != nil {
				return fmt.Errorf("share %q: %w", s.Name, err)
			}
		}
		s.expires = map[string]time.Time{}
		for k, v := range s.Expires {
			if _, ok := s.Access[k]; !ok {
				return fmt.Errorf("share %q: expires entry %q has no access entry", s.Name, k)
			}
			t, err := parseExpiry(v)
			if err != nil {
				return fmt.Errorf("share %q: expires %q: %w", s.Name, k, err)
			}
			s.expires[k] = t
		}
		for k, v := range s.QuotaGB {
			if _, ok := s.Access[k]; !ok {
				return fmt.Errorf("share %q: quotaGB entry %q has no access entry", s.Name, k)
			}
			if v < 0 {
				return fmt.Errorf("share %q: quotaGB for %q must not be negative", s.Name, k)
			}
		}
	}
	for _, a := range c.Admins {
		if err := validKey(a); err != nil {
			return fmt.Errorf("admins: %w", err)
		}
	}
	return nil
}

func parseExpiry(v string) (time.Time, error) {
	if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		return d.AddDate(0, 0, 1), nil
	}
	return time.Parse(time.RFC3339, v)
}

func validKey(k string) error {
	if id, ok := strings.CutPrefix(k, "id:"); ok {
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			return fmt.Errorf("bad user key %q", k)
		}
		return nil
	}
	if strings.TrimSpace(k) == "" || strings.ContainsAny(k, " \t") {
		return fmt.Errorf("bad user key %q", k)
	}
	return nil
}

func keyMatches(k string, userID int64, login string) bool {
	if id, ok := strings.CutPrefix(k, "id:"); ok {
		return id == strconv.FormatInt(userID, 10)
	}
	return login != "" && strings.EqualFold(k, login)
}

func (c *Config) IsAdmin(userID int64, login string) bool {
	for _, a := range c.Admins {
		if keyMatches(a, userID, login) {
			return true
		}
	}
	return false
}

type Grant struct {
	Perms perm.Set
	// QuotaBytes applies only when Limited is set.
	QuotaBytes int64
	Limited    bool
	Expires    time.Time
}

// GrantFor merges every access entry matching the user that hasn't expired.
// A key without a quota makes the result unlimited.
func (s *Share) GrantFor(userID int64, login string, now time.Time) Grant {
	var g Grant
	unlimited := false
	for k, v := range s.Access {
		if !keyMatches(k, userID, login) {
			continue
		}
		if exp, ok := s.expires[k]; ok {
			if !now.Before(exp) {
				continue
			}
			if g.Expires.IsZero() || exp.After(g.Expires) {
				g.Expires = exp
			}
		}
		g.Perms |= v
		if q, ok := s.QuotaGB[k]; ok {
			g.QuotaBytes = max(g.QuotaBytes, int64(q*1e9))
		} else {
			unlimited = true
		}
	}
	g.Limited = !unlimited && g.Perms != 0
	if !g.Limited {
		g.QuotaBytes = 0
	}
	if s.ReadOnly {
		g.Perms = g.Perms.ReadOnly()
	}
	return g
}
