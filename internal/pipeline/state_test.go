package pipeline

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadMissingReturnsFreshState(t *testing.T) {
	st, err := LoadState(t.TempDir(), "myhost")
	if err != nil {
		t.Fatal(err)
	}
	if st.Host != "myhost" || len(st.Completed) != 0 {
		t.Errorf("state: %+v", st)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	want, err := LoadState(dir, "myhost")
	if err != nil {
		t.Fatal(err)
	}
	want.Completed = []string{"1.0"}
	if err := want.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(dir, "myhost")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if want.Path() != filepath.Join(dir, "myhost.json") {
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
	if err := os.WriteFile(filepath.Join(dir, "myhost.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(dir, "myhost"); err == nil {
		t.Error("want error for corrupt state")
	}
}

func TestSaveWithoutFileFails(t *testing.T) {
	if err := (&State{Host: "myhost"}).Save(); err == nil {
		t.Error("a State that was not loaded has nowhere to save to")
	}
}

func TestRemoveState(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"myhost.json", "myhost.known_hosts", "nexthost.json", "nexthost.known_hosts"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveState(dir, "myhost"); err != nil {
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
	if want := []string{"nexthost.json", "nexthost.known_hosts"}; !reflect.DeepEqual(left, want) {
		t.Errorf("left = %v, want %v", left, want)
	}
	if err := RemoveState(dir, "myhost"); err != nil {
		t.Errorf("nothing to remove: %v", err)
	}
}

// A reinstall pins to its own file; once the wipe is confirmed that pin and a
// fresh state take the place of what was saved.
func TestConfirmReinstall(t *testing.T) {
	dir := t.TempDir()
	old := NewState(dir, "myhost")
	old.Completed = []string{"0.1", "0.2"}
	if err := old.Save(); err != nil {
		t.Fatal(err)
	}
	pinFile := filepath.Join(dir, "myhost.known_hosts")
	if err := os.WriteFile(pinFile, []byte("installed"), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := StartReinstall(dir, "myhost")
	if err != nil {
		t.Fatal(err)
	}
	env := NewEnv(Inputs{Host: "myhost", Target: "192.0.2.10", Reinstall: true}, st)
	if env.Pin.File != pinFile+".reinstall" {
		t.Fatalf("pin = %s, want the reinstall pin", env.Pin.File)
	}
	if err := os.WriteFile(env.Pin.File, []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := env.ConfirmReinstall(); err != nil {
		t.Fatal(err)
	}
	if env.Reinstall || env.Pin.File != pinFile {
		t.Errorf("env: reinstall %v, pin %s", env.Reinstall, env.Pin.File)
	}
	if data, err := os.ReadFile(pinFile); err != nil || string(data) != "iso" {
		t.Errorf("pin = %q, %v, want the reinstall's", data, err)
	}
	if _, err := os.Stat(old.Path()); !os.IsNotExist(err) {
		t.Errorf("old state: %v, want it deleted", err)
	}
	if err := env.ConfirmReinstall(); err != nil {
		t.Errorf("a second call: %v", err)
	}
}
