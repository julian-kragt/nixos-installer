package pipeline

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadMissingReturnsFreshState(t *testing.T) {
	st, err := LoadState(t.TempDir(), "atlas")
	if err != nil {
		t.Fatal(err)
	}
	if st.Host != "atlas" || len(st.Completed) != 0 {
		t.Errorf("state: %+v", st)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	want, err := LoadState(dir, "atlas")
	if err != nil {
		t.Fatal(err)
	}
	want.Completed = []string{"1.0"}
	if err := want.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(dir, "atlas")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if want.Path() != filepath.Join(dir, "atlas.json") {
		t.Errorf("path = %s", want.Path())
	}
	info, err := os.Stat(want.Path())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("state file: %v, %v", info, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

func TestLoadCorruptFileFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "atlas.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(dir, "atlas"); err == nil {
		t.Error("want error for corrupt state")
	}
}

func TestSaveWithoutFileFails(t *testing.T) {
	if err := (&State{Host: "atlas"}).Save(); err == nil {
		t.Error("a State that was not loaded has nowhere to save to")
	}
}

func TestRemoveState(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"atlas.json", "atlas.known_hosts", "broadway.json", "broadway.known_hosts"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveState(dir, "atlas"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if want := []string{"broadway.json", "broadway.known_hosts"}; !reflect.DeepEqual(left, want) {
		t.Errorf("left = %v, want %v", left, want)
	}
	if err := RemoveState(dir, "atlas"); err != nil {
		t.Errorf("nothing to remove: %v", err)
	}
}
