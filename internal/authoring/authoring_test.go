package authoring

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"installer/internal/commands/sops"
	"installer/internal/machine"
	"installer/internal/runner"
	"installer/internal/secrets"
)

func TestArg(t *testing.T) {
	for _, f := range []File{
		{Path: "hosts/atlas.yaml", Template: secrets.HostTemplate},
		{Path: "host-users/atlas-jkragt.yaml", Template: secrets.UserTemplate, User: "jkragt"},
	} {
		got, err := ParseArg(f.Arg())
		if err != nil || got != f {
			t.Errorf("ParseArg(%q) = %+v, %v", f.Arg(), got, err)
		}
	}
	for _, bad := range []string{"", "hosts/atlas.yaml", "=templates/host.yaml", "hosts/atlas.yaml="} {
		if _, err := ParseArg(bad); err == nil {
			t.Errorf("ParseArg(%q): want an error", bad)
		}
	}
}

func TestLabel(t *testing.T) {
	user := File{Path: "host-users/atlas-jkragt.yaml", User: "jkragt"}
	if got := label("atlas", user, secrets.Path{"hashedPassword"}); got != "Login password for jkragt on atlas" {
		t.Errorf("user password: %q", got)
	}
	host := File{Path: "hosts/atlas.yaml"}
	if got := label("atlas", host, secrets.Path{"db", "hashedPassword"}); got != "db.hashedPassword for hosts/atlas.yaml" {
		t.Errorf("nested password: %q", got)
	}
}

// TestAuthorHashesAndEncrypts writes a user file whose template has only a
// password, so no editor opens: the password is asked, hashed and encrypted.
func TestAuthorHashesAndEncrypts(t *testing.T) {
	for _, tool := range []string{"sops", "age-keygen", "mkpasswd"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not in PATH")
		}
	}
	keyFile := filepath.Join(t.TempDir(), "key.txt")
	if out, err := exec.Command("age-keygen", "-o", keyFile).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v: %s", err, out)
	}
	out, err := exec.Command("age-keygen", "-y", keyFile).Output()
	if err != nil {
		t.Fatal(err)
	}
	recipient := strings.TrimSpace(string(out))

	dir := t.TempDir()
	cfg, err := secrets.ParseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Apply(secrets.Host{Name: "atlas", Recipient: recipient, Users: map[string]string{"jkragt": recipient}})
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(secrets.ConfigFile, string(data))
	write(secrets.UserTemplate, "# the login password\nhashedPassword: change_me\n")

	const password = "c0rrect-horse"
	ui := runner.New(runner.Options{Out: &bytes.Buffer{}, In: strings.NewReader(password + "\n" + password + "\n")})
	defer ui.Close()
	f := File{Path: secrets.UserFile("atlas", "jkragt"), Template: secrets.UserTemplate, User: "jkragt"}
	if err := Author(runner.NewContext(context.Background(), ui), machine.Local(), dir, "atlas", f); err != nil {
		t.Fatal(err)
	}

	ciphertext, err := os.ReadFile(filepath.Join(dir, f.Path))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), password) || strings.Contains(string(ciphertext), "$y$") {
		t.Fatal("plaintext in the encrypted file")
	}
	if err := sops.CheckCiphertext(ciphertext); err != nil {
		t.Errorf("CheckCiphertext: %v", err)
	}
	fields, err := sops.Fields(context.Background(), machine.Loopback(), keyFile, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || fields[0].Class != secrets.ClassCrypt {
		t.Errorf("fields = %+v, want one crypt hash", fields)
	}
}
