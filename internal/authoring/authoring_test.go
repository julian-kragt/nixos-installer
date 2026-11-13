package authoring

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/julian-kragt/nixos-installer/internal/commands/sops"
	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/runner"
	"github.com/julian-kragt/nixos-installer/internal/secrets"
)

func TestArg(t *testing.T) {
	for _, f := range []File{
		{Path: "hosts/myhost.yaml", Template: secrets.ReferenceLayout.HostTemplate},
		{Path: "host-users/myhost-jdoe.yaml", Template: secrets.ReferenceLayout.HostUserTemplate, User: "jdoe"},
	} {
		got, err := ParseArg(f.Arg())
		if err != nil || got != f {
			t.Errorf("ParseArg(%q) = %+v, %v", f.Arg(), got, err)
		}
	}
	for _, bad := range []string{"", "hosts/myhost.yaml", "=templates/host.yaml", "hosts/myhost.yaml="} {
		if _, err := ParseArg(bad); err == nil {
			t.Errorf("ParseArg(%q): want an error", bad)
		}
	}
}

func TestLabel(t *testing.T) {
	user := File{Path: "host-users/myhost-jdoe.yaml", User: "jdoe"}
	if got := label("myhost", user, secrets.Path{"hashedPassword"}); got != "Login password for jdoe on myhost" {
		t.Errorf("user password: %q", got)
	}
	host := File{Path: "hosts/myhost.yaml"}
	if got := label("myhost", host, secrets.Path{"db", "hashedPassword"}); got != "db.hashedPassword for hosts/myhost.yaml" {
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
	cfg.Apply(secrets.ReferenceLayout, secrets.Host{Name: "myhost", Recipient: recipient, Users: map[string]string{"jdoe": recipient}})
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
	write(secrets.ReferenceLayout.HostUserTemplate, "# the login password\nhashedPassword: change_me\n")

	const password = "c0rrect-horse"
	ui := runner.New(runner.Options{Out: &bytes.Buffer{}, In: strings.NewReader(password + "\n" + password + "\n")})
	defer ui.Close()
	f := File{Path: secrets.ReferenceLayout.HostUserFile("myhost", "jdoe"), Template: secrets.ReferenceLayout.HostUserTemplate, User: "jdoe"}
	if err := Author(runner.NewContext(context.Background(), ui), machine.Local(), dir, "myhost", f); err != nil {
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
