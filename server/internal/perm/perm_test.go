package perm

import (
	"encoding/json"
	"testing"
)

func TestUnmarshal(t *testing.T) {
	cases := map[string]Set{
		`"contributor"`:       View | Download | Upload | Mkdir,
		`["viewer","upload"]`: View | Download | Upload,
		`["Delete"]`:          Delete,
		`"dropbox"`:           Upload,
		`[]`:                  0,
	}
	for in, want := range cases {
		var got Set
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Errorf("%s: got %v want %v", in, got.Names(), want.Names())
		}
	}
	var s Set
	if err := json.Unmarshal([]byte(`"admin"`), &s); err == nil {
		t.Error("unknown preset accepted")
	}
}

func TestReadOnlyStripsWrites(t *testing.T) {
	if got := Presets["editor"].ReadOnly(); got != View|Download {
		t.Errorf("got %v", got.Names())
	}
}
