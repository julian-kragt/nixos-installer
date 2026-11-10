package sops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"installer/internal/machine"
	"installer/internal/secrets"
)

const (
	hash   = "$y$j9T$abcdefghijklmnop$0123456789abcdefghijklmnopqrstuvwxyzABCDE"
	secret = "s3cr3t-value"
)

// needTools skips the test unless the real sops and age-keygen are on PATH, as
// in `nix develop`.
func needTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not in PATH")
		}
	}
}

// newKey creates an age key in dir and returns its file and recipient.
func newKey(t *testing.T, dir string) (string, string) {
	t.Helper()
	file := filepath.Join(dir, "key.txt")
	if out, err := exec.Command("age-keygen", "-o", file).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v: %s", err, out)
	}
	out, err := exec.Command("age-keygen", "-y", file).Output()
	if err != nil {
		t.Fatal(err)
	}
	return file, strings.TrimSpace(string(out))
}

// newRepo creates a nix-secrets repository whose .sops.yaml, written by the
// editor, encrypts atlas' files to recipient.
func newRepo(t *testing.T, recipient string) string {
	t.Helper()
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
	if err := os.WriteFile(filepath.Join(dir, secrets.ConfigFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "host-users"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestEncryptThenFieldsOnTheTarget(t *testing.T) {
	needTools(t)
	keyFile, recipient := newKey(t, t.TempDir())
	dir := newRepo(t, recipient)
	file := secrets.UserFile("atlas", "jkragt")
	doc := []byte("hashedPassword: '" + hash + "'\nnote: " + secret + "\nleft: change_me\n" +
		"db:\n  key: |\n    -----BEGIN KEY-----\n    " + secret + "\n  servers:\n    - a\n    - b\n  pending:\n    - change_me\n")

	if err := (Repo{machine.Local(), dir}).Encrypt(context.Background(), file, doc); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), secret) || strings.Contains(string(ciphertext), hash) {
		t.Fatal("plaintext in the encrypted file")
	}
	if err := CheckCiphertext(ciphertext); err != nil {
		t.Errorf("CheckCiphertext: %v", err)
	}
	if got, err := Recipients(ciphertext); err != nil || !slices.Equal(got, []string{recipient}) {
		t.Errorf("recipients = %q, %v, want [%s]", got, err, recipient)
	}

	fields, err := Fields(context.Background(), machine.Loopback(), keyFile, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	want := []secrets.Field{
		{Path: secrets.Path{"hashedPassword"}, Class: secrets.ClassCrypt},
		{Path: secrets.Path{"note"}, Class: secrets.ClassValue},
		{Path: secrets.Path{"left"}, Class: secrets.ClassPlaceholder},
		{Path: secrets.Path{"db", "key"}, Class: secrets.ClassValue},
		{Path: secrets.Path{"db", "servers"}, Class: secrets.ClassValue},
		{Path: secrets.Path{"db", "pending"}, Class: secrets.ClassPlaceholder},
	}
	if !slices.EqualFunc(fields, want, func(a, b secrets.Field) bool { return slices.Equal(a.Path, b.Path) && a.Class == b.Class }) {
		t.Errorf("fields = %+v\nwant %+v", fields, want)
	}
}

func TestFieldsWithAnotherKey(t *testing.T) {
	needTools(t)
	_, recipient := newKey(t, t.TempDir())
	otherKey, _ := newKey(t, t.TempDir())
	dir := newRepo(t, recipient)
	file := secrets.HostFile("atlas")
	if err := (Repo{machine.Local(), dir}).Encrypt(context.Background(), file, []byte("placeholder: unused\n")); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Fields(context.Background(), machine.Loopback(), otherKey, ciphertext); !errors.Is(err, ErrCannotDecrypt) {
		t.Errorf("err = %v, want ErrCannotDecrypt", err)
	}
}

func TestFieldsWithoutTheKeyFile(t *testing.T) {
	needTools(t)
	_, recipient := newKey(t, t.TempDir())
	dir := newRepo(t, recipient)
	file := secrets.HostFile("atlas")
	if err := (Repo{machine.Local(), dir}).Encrypt(context.Background(), file, []byte("placeholder: unused\n")); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "key.txt")
	_, err = Fields(context.Background(), machine.Loopback(), missing, ciphertext)
	if err == nil || errors.Is(err, ErrCannotDecrypt) || !strings.Contains(err.Error(), missing) {
		t.Errorf("err = %v, want the unreadable key file named", err)
	}
}

func TestFieldsErrorNamesTheCause(t *testing.T) {
	paths := []secrets.Path{{"a"}, {"db", "password"}}
	tests := []struct {
		code    int
		printed []string
		want    string
	}{
		{undecryptable, nil, ErrCannotDecrypt.Error()},
		{sopsFailed, []string{"127"}, "sops is not installed there"},
		{sopsFailed, []string{"2"}, "sops failed (exit 2)"},
		{unextractable, []string{"value"}, "cannot extract db.password"},
		{1, nil, "exit status 1"},
	}
	for _, tt := range tests {
		err := exec.Command("sh", "-c", "exit "+strconv.Itoa(tt.code)).Run()
		if got := fieldsError(err, "/key", tt.printed, paths); got == nil || !strings.Contains(got.Error(), tt.want) {
			t.Errorf("exit %d, printed %q: err = %v, want %q", tt.code, tt.printed, got, tt.want)
		}
	}
}

func TestEncryptWithoutARuleKeepsThePlaintextOutOfTheError(t *testing.T) {
	needTools(t)
	_, recipient := newKey(t, t.TempDir())
	dir := newRepo(t, recipient)
	err := (Repo{machine.Local(), dir}).Encrypt(context.Background(), "hosts/nowhere.yaml", []byte("x: "+secret+"\n"))
	if err == nil {
		t.Fatal("want an error for a file without creation rule")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("err = %v, shows the plaintext", err)
	}
}

func TestCheckCiphertext(t *testing.T) {
	const meta = "sops:\n  age:\n    - recipient: age1x\n"
	tests := []struct {
		name, data string
		ok         bool
	}{
		{"encrypted", "hashedPassword: ENC[AES256_GCM,data:abc,type:str]\n" + meta, true},
		{"nested encrypted", "a:\n  b: ENC[AES256_GCM,data:abc,type:str]\n  c:\n    - ENC[x]\n" + meta, true},
		{"plaintext", "hashedPassword: " + hash + "\n" + meta, false},
		{"nested plaintext", "a:\n  b: ENC[x]\n  c:\n    - plain\n" + meta, false},
		{"no metadata", "hashedPassword: ENC[x]\n", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckCiphertext([]byte(tt.data)); (err == nil) != tt.ok {
				t.Errorf("err = %v, want ok = %v", err, tt.ok)
			}
		})
	}
}

// The target classifies a value as secrets.Classify does.
func TestClassPatternsClassifyLikeGo(t *testing.T) {
	script := "for v in \"$@\"; do\n\tcase $v in\n\t" + classPatterns + "\n\tesac\ndone"
	values := []string{"", "x", "change_me", "a CHANGEME b", "Change_Me", "change-me", hash, "$6$salt$h", "$1$old", "it's", "*", "$y"}
	out, err := exec.Command("sh", append([]string{"-c", script, "sh"}, values...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(got) != len(values) {
		t.Fatalf("got %q for %d values", got, len(values))
	}
	for i, v := range values {
		if want := string(secrets.Classify(v)); got[i] != want {
			t.Errorf("%q: shell says %s, Classify %s", v, got[i], want)
		}
	}
}
