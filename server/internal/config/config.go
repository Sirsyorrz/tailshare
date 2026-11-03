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

	"github.com/Sirsyorrz/tailshare/server/internal/perm"
)

type Config struct {
	// Admins are Tailscale login names or "id:<userid>". Unused until the
	// admin API exists, but validated now so configs stay forward compatible.
	Admins []string `json:"admins"`
	Shares []Share  `json:"shares"`
}

type Share struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly"`
	// Access keys are Tailscale login names (case-insensitive) or
	// "id:<userid>". Login names are display data on Tailscale's side, so
	// the id form is preferred once the GUI writes this file.
	Access map[string]perm.Set `json:"access"`
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

func (c *Config) validate() error {
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
	}
	for _, a := range c.Admins {
		if err := validKey(a); err != nil {
			return fmt.Errorf("admins: %w", err)
		}
	}
	return nil
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

func (s *Share) PermsFor(userID int64, login string) perm.Set {
	var p perm.Set
	idKey := "id:" + strconv.FormatInt(userID, 10)
	for k, v := range s.Access {
		if k == idKey || (login != "" && strings.EqualFold(k, login)) {
			p |= v
		}
	}
	if s.ReadOnly {
		p = p.ReadOnly()
	}
	return p
}
