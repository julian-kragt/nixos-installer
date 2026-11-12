// Package sops runs sops. The console helper encrypts, to public keys only; the
// target, which holds the host's key, checks that a file decrypts and reports
// what its values look like, never the values.
package sops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"installer/internal/machine"
	"installer/internal/secrets"
)

// Repo is the nix-secrets repository at Dir on Machine.
type Repo struct {
	Machine machine.Machine
	Dir     string
}

// encrypt encrypts stdin with the creation rule of $2, relative to the
// repository at $1, and writes the result to $2.
const encrypt = `cd "$1" && mkdir -p "$(dirname "$2")" && sops encrypt --filename-override "$2" --input-type yaml --output-type yaml --output "$2"`

// Encrypt writes file, relative to Dir, as doc encrypted to the recipients
// .sops.yaml gives it. doc goes over stdin and nothing is printed, because it
// holds the plaintext.
func (r Repo) Encrypt(ctx context.Context, file string, doc []byte) error {
	if _, err := r.Machine.Quiet().RunStdin(ctx, bytes.NewReader(doc), "sh", "-c", encrypt, "sh", r.Dir, file); err != nil {
		return fmt.Errorf("encrypt %s: %w (sops's message is hidden, since it could show the plaintext)", file, err)
	}
	return nil
}

// ErrCannotDecrypt means the key on the target does not decrypt the file.
var ErrCannotDecrypt = errors.New("the target's key does not decrypt it")

// The exit codes of fields when it cannot classify every value.
const (
	undecryptable = 3 // sops cannot retrieve the data key with the age key (its exit 128)
	unreadableKey = 4 // the age key file cannot be read
	sopsFailed    = 5 // sops failed otherwise; its exit code is printed
	unextractable = 6 // sops cannot extract a path; the classes before it are printed
)

// fields decrypts each path in $2… of the sops file on stdin with the age key
// in $1 and prints the class of its value, one line per path, by the rules of
// secrets.Classify. The whole file is decrypted first, to tell a key that
// does not decrypt it from other failures. The ciphertext goes to a temp file,
// as sops decrypts once per path; each value stays in a shell variable and
// only meets builtins. sops' messages are dropped: they could hold a value.
var fields = `f=$(mktemp) || exit 1
trap 'rm -f "$f"' EXIT
cat > "$f"
key=$1
shift
test -r "$key" || exit 4
SOPS_AGE_KEY_FILE="$key" sops decrypt --input-type yaml --output-type yaml "$f" >/dev/null 2>&1
rc=$?
case $rc in
0) ;;
128) exit 3 ;;
*) echo "$rc"; exit 5 ;;
esac
for p in "$@"; do
	v=$(SOPS_AGE_KEY_FILE="$key" sops decrypt --input-type yaml --output-type yaml --extract "$p" "$f" 2>/dev/null) || exit 6
	case $v in
	` + classPatterns + `
	esac
done`

// classPatterns are the branches of a shell case on a value that print its
// class by the rules of secrets.Classify, in the same order.
var classPatterns = strings.Join([]string{
	patterns(secrets.Placeholders, func(p string) string { return "*" + anyCase(p) + "*" }) + ") echo " + string(secrets.ClassPlaceholder) + " ;;",
	"'') echo " + string(secrets.ClassEmpty) + " ;;",
	patterns(secrets.CryptPrefixes, func(p string) string { return quote(p) + "*" }) + ") echo " + string(secrets.ClassCrypt) + " ;;",
	"*) echo " + string(secrets.ClassValue) + " ;;",
}, "\n\t")

// patterns joins the case patterns pattern makes of each of values.
func patterns(values []string, pattern func(string) string) string {
	ps := make([]string, len(values))
	for i, v := range values {
		ps[i] = pattern(v)
	}
	return strings.Join(ps, " | ")
}

// anyCase is a case pattern matching s in any case: [Cc][Hh]… for its
// letters, everything else quoted.
func anyCase(s string) string {
	var b strings.Builder
	for _, r := range s {
		if up, low := unicode.ToUpper(r), unicode.ToLower(r); up != low {
			b.WriteString("[" + string(up) + string(low) + "]")
		} else {
			b.WriteString(quote(string(r)))
		}
	}
	return b.String()
}

// quote is s as a literal in a shell case pattern.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Fields decrypts ciphertext on target with the age key at keyFile and returns
// each leaf with the class of its value. The paths come from the ciphertext,
// whose keys sops leaves readable; only the classes come from the target. It is
// ErrCannotDecrypt when that key cannot decrypt the file.
func Fields(ctx context.Context, target machine.Machine, keyFile string, ciphertext []byte) ([]secrets.Field, error) {
	paths, err := leafPaths(ciphertext)
	if err != nil {
		return nil, err
	}
	args := []string{"-c", fields, "sh", keyFile}
	for _, p := range paths {
		args = append(args, p.Extract())
	}
	out, err := target.Quiet().RunStdin(ctx, bytes.NewReader(ciphertext), "sh", args...)
	if err != nil {
		return nil, fieldsError(err, keyFile, strings.Fields(out), paths)
	}
	classes := strings.Fields(out)
	if len(classes) != len(paths) {
		return nil, fmt.Errorf("decrypt on the target: %d classes for %d values", len(classes), len(paths))
	}
	fs := make([]secrets.Field, len(paths))
	for i, p := range paths {
		fs[i] = secrets.Field{Path: p, Class: secrets.Class(classes[i])}
	}
	return fs, nil
}

// fieldsError says why fields failed, from its exit code and what it printed.
func fieldsError(err error, keyFile string, printed []string, paths []secrets.Path) error {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return fmt.Errorf("decrypt on the target: %w", err)
	}
	switch exit.ExitCode() {
	case undecryptable:
		return ErrCannotDecrypt
	case unreadableKey:
		return fmt.Errorf("decrypt on the target: cannot read the age key %s", keyFile)
	case sopsFailed:
		if len(printed) == 1 && printed[0] == "127" {
			return errors.New("decrypt on the target: sops is not installed there")
		}
		return fmt.Errorf("decrypt on the target: sops failed (exit %s)", strings.Join(printed, " "))
	case unextractable:
		if len(printed) < len(paths) {
			return fmt.Errorf("decrypt on the target: sops cannot extract %s", paths[len(printed)])
		}
	}
	return fmt.Errorf("decrypt on the target: %w", err)
}

// leafPaths returns the paths of the values in a sops file, without its metadata.
func leafPaths(ciphertext []byte) ([]secrets.Path, error) {
	top, err := secrets.ParseMapping(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("read sops file: %w", err)
	}
	leaves, err := secrets.Leaves(top)
	if err != nil {
		return nil, fmt.Errorf("read sops file: %w", err)
	}
	var paths []secrets.Path
	for _, l := range leaves {
		if l.Path[0] != "sops" {
			paths = append(paths, l.Path)
		}
	}
	return paths, nil
}

// metadata is the part of a sops file that is not encrypted.
type metadata struct {
	Sops struct {
		Age []struct {
			Recipient string `yaml:"recipient"`
		} `yaml:"age"`
	} `yaml:"sops"`
}

// Recipients returns the age recipients a sops file is encrypted to.
func Recipients(ciphertext []byte) ([]string, error) {
	var m metadata
	if err := yaml.Unmarshal(ciphertext, &m); err != nil {
		return nil, fmt.Errorf("read sops metadata: %w", err)
	}
	var out []string
	for _, a := range m.Sops.Age {
		out = append(out, a.Recipient)
	}
	return out, nil
}

// CheckCiphertext fails unless data is a sops file: it has sops metadata and
// every value outside it is encrypted.
func CheckCiphertext(data []byte) error {
	top, err := secrets.ParseMapping(data)
	if err != nil {
		return fmt.Errorf("not a sops file: %w", err)
	}
	hasMetadata := false
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "sops" {
			hasMetadata = true
		} else if hasPlain(top.Content[i+1]) {
			return fmt.Errorf("%s is not encrypted", top.Content[i].Value)
		}
	}
	if !hasMetadata {
		return errors.New("not a sops file: no sops metadata")
	}
	return nil
}

// hasPlain reports whether a scalar below n is not an ENC[…] value.
func hasPlain(n *yaml.Node) bool {
	switch n.Kind {
	case yaml.ScalarNode:
		return !strings.HasPrefix(n.Value, "ENC[") || !strings.HasSuffix(n.Value, "]")
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			if hasPlain(n.Content[i]) {
				return true
			}
		}
	case yaml.SequenceNode:
		return slices.ContainsFunc(n.Content, hasPlain)
	}
	return false
}

// StaticProblem is the part of Problem that needs no key: it says why a secret
// file is not one encrypted to exactly want, or "" when it is. ciphertext is
// nil for a missing file.
func StaticProblem(ciphertext []byte, want []string) (string, error) {
	if ciphertext == nil {
		return "missing", nil
	}
	if err := CheckCiphertext(ciphertext); err != nil {
		return err.Error(), nil
	}
	have, err := Recipients(ciphertext)
	if err != nil {
		return "", err
	}
	if !slices.Equal(slices.Sorted(slices.Values(have)), slices.Sorted(slices.Values(want))) {
		return "encrypted to other recipients than " + secrets.ConfigFile + " gives it", nil
	}
	return "", nil
}

// Problem says why a secret file needs its values asked for again, or "" when
// it is fine: it exists, is encrypted to exactly want, the key at keyFile on
// target decrypts it and its fields pass secrets.Check against keys.
// ciphertext is nil for a missing file.
func Problem(ctx context.Context, target machine.Machine, keyFile string, ciphertext []byte, want []string, keys []secrets.TemplateKey) (string, error) {
	if problem, err := StaticProblem(ciphertext, want); problem != "" || err != nil {
		return problem, err
	}
	fields, err := Fields(ctx, target, keyFile, ciphertext)
	if errors.Is(err, ErrCannotDecrypt) {
		return ErrCannotDecrypt.Error(), nil
	}
	if err != nil {
		return "", err
	}
	if err := secrets.Check(keys, fields); err != nil {
		return err.Error(), nil
	}
	return "", nil
}
