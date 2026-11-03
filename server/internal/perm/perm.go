package perm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Set uint16

const (
	View Set = 1 << iota
	Download
	Upload
	Mkdir
	Overwrite
	Rename
	Delete
)

const writeMask = Upload | Mkdir | Overwrite | Rename | Delete

var names = map[string]Set{
	"view":      View,
	"download":  Download,
	"upload":    Upload,
	"mkdir":     Mkdir,
	"overwrite": Overwrite,
	"rename":    Rename,
	"delete":    Delete,
}

var Presets = map[string]Set{
	"viewer":      View | Download,
	"contributor": View | Download | Upload | Mkdir,
	"editor":      View | Download | Upload | Mkdir | Overwrite | Rename | Delete,
	"dropbox":     Upload,
}

func (s Set) Has(p Set) bool { return s&p == p }

func (s Set) ReadOnly() Set { return s &^ writeMask }

func (s Set) Names() []string {
	out := []string{}
	for n, p := range names {
		if s.Has(p) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (s Set) MarshalJSON() ([]byte, error) { return json.Marshal(s.Names()) }

// UnmarshalJSON accepts a preset name ("contributor") or a list of permission
// names, where list entries may also be presets.
func (s *Set) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		v, err := Parse(one)
		*s = v
		return err
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("permissions must be a preset name or a list of names")
	}
	var out Set
	for _, n := range many {
		v, err := Parse(n)
		if err != nil {
			return err
		}
		out |= v
	}
	*s = out
	return nil
}

func Parse(name string) (Set, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if p, ok := Presets[n]; ok {
		return p, nil
	}
	if p, ok := names[n]; ok {
		return p, nil
	}
	return 0, fmt.Errorf("unknown permission or preset %q", name)
}
