package hostidentity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"installer/internal/machine"
)

const (
	fakeSecret    = "AGE-SECRET-KEY-FAKE"
	fakeRecipient = "age1fake"
)

// fakeKeygen puts an age-keygen on PATH with the given script body.
func fakeKeygen(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(bin, "age-keygen"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

// workingKeygen acts like the real tool: -o writes a key and fails on an existing
// file, -y prints the recipient of a key file.
func workingKeygen(t *testing.T) {
	t.Helper()
	fakeKeygen(t, `case "$1" in
-o) test ! -e "$2" && echo `+fakeSecret+` > "$2" ;;
-y) test -s "$2" && echo `+fakeRecipient+` ;;
*) exit 2 ;;
esac`)
}

func TestRecipientGeneratesAMissingKey(t *testing.T) {
	workingKeygen(t)
	path := filepath.Join(t.TempDir(), "sops-nix", "key.txt")

	got, err := Recipient(context.Background(), machine.Loopback(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got != fakeRecipient {
		t.Errorf("recipient = %q, want %q", got, fakeRecipient)
	}
	for file, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", file, info.Mode().Perm(), want)
		}
	}
}

func TestRecipientKeepsAnExistingKey(t *testing.T) {
	workingKeygen(t)
	path := filepath.Join(t.TempDir(), "key.txt")
	const existing = "AGE-SECRET-KEY-EXISTING\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if _, err := Recipient(context.Background(), machine.Loopback(), path); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != existing {
		t.Errorf("key file = %q, want it unchanged", got)
	}
}

func TestRecipientFailsWhenTheToolFails(t *testing.T) {
	fakeKeygen(t, "exit 1")
	path := filepath.Join(t.TempDir(), "key.txt")
	if _, err := Recipient(context.Background(), machine.Loopback(), path); err == nil {
		t.Error("want an error when age-keygen fails")
	}
}

func TestRecipientRefusesOutputAndKeepsItOutOfTheError(t *testing.T) {
	fakeKeygen(t, "echo "+fakeSecret)
	path := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Recipient(context.Background(), machine.Loopback(), path)
	if err == nil || !strings.Contains(err.Error(), "not an age recipient") {
		t.Fatalf("err = %v, want a refused recipient", err)
	}
	if strings.Contains(err.Error(), fakeSecret) {
		t.Errorf("err = %v, shows what the tool printed", err)
	}
}

// fakeRunuser puts a runuser on PATH that checks it is called as
// `runuser -u <user> -- cmd...` and runs cmd as the current user.
func fakeRunuser(t *testing.T, user string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
test "$1" = -u && test "$2" = '` + user + `' && test "$3" = -- || exit 9
shift 3
exec "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "runuser"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

func TestUserRecipientKeepsItsKeyAcrossRuns(t *testing.T) {
	workingKeygen(t)
	fakeRunuser(t, "alice")
	home := t.TempDir()
	file := filepath.Join(home, UserKeyFile)

	var keys []string
	for range 2 {
		got, err := UserRecipient(context.Background(), machine.Loopback(), "alice", home)
		if err != nil {
			t.Fatal(err)
		}
		if got != fakeRecipient {
			t.Errorf("recipient = %q, want %q", got, fakeRecipient)
		}
		key, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, string(key))
	}
	if keys[0] != keys[1] {
		t.Errorf("key changed between runs: %q, then %q", keys[0], keys[1])
	}
	for f, want := range map[string]os.FileMode{file: 0o600, filepath.Join(home, ".config"): 0o700} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", f, info.Mode().Perm(), want)
		}
	}
}

func TestUserRecipientNamesTheUserOnFailure(t *testing.T) {
	fakeKeygen(t, "exit 1")
	fakeRunuser(t, "alice")
	_, err := UserRecipient(context.Background(), machine.Loopback(), "alice", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("err = %v, want one naming alice", err)
	}
}
