// Package sops runs sops. The provider encrypts, to public keys only; the
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

// undecryptable is the exit code of fields when sops cannot decrypt.
const undecryptable = 3

// fields decrypts each path in $2… of the sops file on stdin with the age key
// in $1 and prints the class of its value, one line per path, by the rules of
// secrets.Classify. The ciphertext goes to a temp file, as sops decrypts once
// per path; each value stays in a shell variable and only meets builtins.
const fields = `f=$(mktemp) || exit 1
trap 'rm -f "$f"' EXIT
cat > "$f"
key=$1
shift
for p in "$@"; do
	v=$(SOPS_AGE_KEY_FILE="$key" sops decrypt --input-type yaml --output-type yaml --extract "$p" "$f" 2>/dev/null) || exit 3
	case $v in
	*[Cc][Hh][Aa][Nn][Gg][Ee]_[Mm][Ee]* | *[Cc][Hh][Aa][Nn][Gg][Ee][Mm][Ee]*) echo placeholder ;;
	'') echo empty ;;
	'$y$'* | '$gy$'* | '$7$'* | '$2a$'* | '$2b$'* | '$2y$'* | '$5$'* | '$6$'*) echo crypt ;;
	*) echo value ;;
	esac
done`

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
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == undecryptable {
		return nil, ErrCannotDecrypt
	}
	if err != nil {
		return nil, fmt.Errorf("decrypt on the target: %w", err)
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

// leafPaths returns the paths of the values in a sops file, without its metadata.
func leafPaths(ciphertext []byte) ([]secrets.Path, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(ciphertext, &doc); err != nil {
		return nil, fmt.Errorf("read sops file: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("read sops file: no top-level mapping")
	}
	leaves, err := secrets.Leaves(doc.Content[0])
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
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("not YAML: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("not a sops file: no top-level mapping")
	}
	top, hasMetadata := doc.Content[0], false
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

// Problem says why a secret file needs its values asked for again, or "" when
// it is fine: it exists, is encrypted to exactly want, the key at keyFile on
// target decrypts it and its fields pass secrets.Check against keys.
// ciphertext is nil for a missing file.
func Problem(ctx context.Context, target machine.Machine, keyFile string, ciphertext []byte, want []string, keys []secrets.TemplateKey) (string, error) {
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
