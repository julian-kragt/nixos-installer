package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"installer/internal/runner"
)

// HostTemplate and UserTemplate are the schemas of a host's own file and of a
// user's file, relative to the repository.
const (
	HostTemplate = "templates/host.yaml"
	UserTemplate = "templates/user.yaml"
)

// passwordKey holds a crypt hash of the user's login password.
const passwordKey = "hashedPassword"

// metadataKey is where sops keeps its metadata in an encrypted file.
const metadataKey = "sops"

// Path is where a value sits in a secret file: the keys from the top down.
type Path []string

// String is the path as the operator reads it: db.password.
func (p Path) String() string { return strings.Join(p, ".") }

// Extract is the path as sops' --extract takes it: ["db"]["password"].
func (p Path) Extract() string {
	var b strings.Builder
	for _, k := range p {
		b.WriteString(`["` + k + `"]`)
	}
	return b.String()
}

func (p Path) equal(q Path) bool { return slices.Equal(p, q) }

// Leaf is a value in a secret file: any node that is not a mapping with keys,
// so a scalar, a multi-line scalar or a list.
type Leaf struct {
	Path Path
	Node *yaml.Node
}

// Leaves returns the leaves below the mapping m, in file order. A key that
// sops cannot encrypt or its --extract cannot address is an error.
func Leaves(m *yaml.Node) ([]Leaf, error) {
	var out []Leaf
	var walk func(m *yaml.Node, prefix Path) error
	walk = func(m *yaml.Node, prefix Path) error {
		for i := 0; i+1 < len(m.Content); i += 2 {
			k, v := m.Content[i].Value, m.Content[i+1]
			if m.Content[i].ShortTag() != "!!str" {
				return fmt.Errorf("key %s under %q: sops takes only string keys; quote it", k, prefix.String())
			}
			if k == "" || strings.ContainsAny(k, `"]\`) {
				return fmt.Errorf("key %q under %q: keys cannot be empty or hold \", ] or \\", k, prefix.String())
			}
			p := append(slices.Clone(prefix), k)
			if v.Kind == yaml.MappingNode && len(v.Content) > 0 {
				if err := walk(v, p); err != nil {
					return err
				}
				continue
			}
			out = append(out, Leaf{Path: p, Node: v})
		}
		return nil
	}
	return out, walk(m, nil)
}

// Kind says where the value of a template leaf comes from.
type Kind int

const (
	Literal  Kind = iota // the template's value, kept unless the operator edits it
	Secret               // a placeholder or empty value the operator replaces in the editor
	Password             // asked with a prompt, stored as a crypt hash
)

// TemplateKey is one leaf of a template.
type TemplateKey struct {
	Path Path
	Kind Kind
}

// Template is the schema of a secret file: a YAML mapping, nested to any depth.
type Template struct {
	root *yaml.Node // the top-level mapping
	Keys []TemplateKey
}

// ParseTemplate reads a template. A leaf named hashedPassword is a password; a
// leaf holding change_me or CHANGEME, or nothing, is a secret; any other leaf
// is a literal.
func ParseTemplate(data []byte) (*Template, error) {
	root, err := parseMapping(data)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	leaves, err := Leaves(root)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	t := &Template{root: root}
	for _, l := range leaves {
		k := TemplateKey{Path: l.Path, Kind: Literal}
		switch {
		case l.Path[len(l.Path)-1] == passwordKey:
			k.Kind = Password
		case slices.Contains([]Class{ClassPlaceholder, ClassEmpty}, Classify(text(l.Node))):
			k.Kind = Secret
		}
		t.Keys = append(t.Keys, k)
	}
	return t, nil
}

// Passwords returns the paths of the template's password leaves.
func (t *Template) Passwords() []Path {
	var out []Path
	for _, k := range t.Keys {
		if k.Kind == Password {
			out = append(out, k.Path)
		}
	}
	return out
}

// NeedsEditor reports whether the operator has values to write besides the
// passwords, which are asked for.
func (t *Template) NeedsEditor() bool {
	return slices.ContainsFunc(t.Keys, func(k TemplateKey) bool { return k.Kind == Secret })
}

// Content is the template's mapping without its password leaves: what the
// file holds when there is nothing to edit, and what the editor starts from.
func (t *Template) Content() *yaml.Node {
	m := clone(t.root)
	for _, p := range t.Passwords() {
		remove(m, p)
	}
	return m
}

// Buffer is the text the editor opens for file: Content under a comment that
// says what to do. Comments are not kept in the file.
func (t *Template) Buffer(file string) ([]byte, error) {
	m := t.Content()
	m.HeadComment = "Secrets for " + file + ". Replace every change_me / CHANGEME and fill in every empty value.\n" +
		"Save and quit (:wq) to encrypt, or :cq to abort. Comments are not kept.\n" +
		"Passwords are asked for separately."
	return marshal(m)
}

// CheckEdited parses what the operator wrote for the template's file and
// checks it like Check, leaving out the passwords, which are not set yet.
func (t *Template) CheckEdited(data []byte) (*yaml.Node, error) {
	m, err := parseMapping(data)
	if err != nil {
		return nil, err
	}
	fields, err := FieldsOf(m)
	if err != nil {
		return nil, err
	}
	keys := slices.DeleteFunc(slices.Clone(t.Keys), func(k TemplateKey) bool { return k.Kind == Password })
	return m, Check(keys, fields)
}

// Assignment is a value set at a path.
type Assignment struct {
	Path  Path
	Value string
}

// Document returns the YAML of a secret file: content, a mapping, without its
// comments, with the assignments set. sops' own metadata key is refused.
func Document(content *yaml.Node, set ...Assignment) ([]byte, error) {
	m := clone(content)
	stripComments(m)
	if value(m, metadataKey) != nil {
		return nil, fmt.Errorf("the top-level key %q is sops' own", metadataKey)
	}
	for _, a := range set {
		setPath(m, a.Path, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: a.Value})
	}
	return marshal(m)
}

// Class is what a decrypted value looks like, without the value itself.
type Class string

const (
	ClassPlaceholder Class = "placeholder"
	ClassEmpty       Class = "empty"
	ClassCrypt       Class = "crypt" // a crypt(3) hash: yescrypt, sha-512, bcrypt …
	ClassValue       Class = "value"
)

// cryptPrefixes start the crypt(3) hashes Classify recognises.
var cryptPrefixes = []string{"$y$", "$gy$", "$7$", "$2a$", "$2b$", "$2y$", "$5$", "$6$"}

// Classify says what v looks like. The target classifies with the same rules
// in shell (internal/commands/sops), so both must change together.
func Classify(v string) Class {
	lower := strings.ToLower(v)
	switch {
	case strings.Contains(lower, "change_me") || strings.Contains(lower, "changeme"):
		return ClassPlaceholder
	case v == "":
		return ClassEmpty
	case slices.ContainsFunc(cryptPrefixes, func(p string) bool { return strings.HasPrefix(v, p) }):
		return ClassCrypt
	}
	return ClassValue
}

// Field is one leaf of a decrypted secret file and the class of its value.
type Field struct {
	Path  Path
	Class Class
}

// FieldsOf returns the fields of the plaintext mapping m.
func FieldsOf(m *yaml.Node) ([]Field, error) {
	leaves, err := Leaves(m)
	if err != nil {
		return nil, err
	}
	fields := make([]Field, len(leaves))
	for i, l := range leaves {
		fields[i] = Field{Path: l.Path, Class: Classify(text(l.Node))}
	}
	return fields, nil
}

// Check fails unless fields, from a decrypted file, has exactly the leaves of
// keys, no placeholder or empty value, and a crypt hash as each password. The
// problems name paths, never values.
func Check(keys []TemplateKey, fields []Field) error {
	var problems []string
	for _, k := range keys {
		i := slices.IndexFunc(fields, func(f Field) bool { return f.Path.equal(k.Path) })
		if i < 0 {
			problems = append(problems, k.Path.String()+" is missing")
			continue
		}
		switch c := fields[i].Class; {
		case c == ClassPlaceholder:
			problems = append(problems, k.Path.String()+" still holds a placeholder")
		case c == ClassEmpty:
			problems = append(problems, k.Path.String()+" is empty")
		case k.Kind == Password && c != ClassCrypt:
			problems = append(problems, k.Path.String()+" is not a crypt hash")
		}
	}
	for _, f := range fields {
		if !slices.ContainsFunc(keys, func(k TemplateKey) bool { return k.Path.equal(f.Path) }) {
			problems = append(problems, f.Path.String()+" is not in the template")
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// AskValue asks for the value called label until two non-empty answers agree.
// The answer is never shown or logged.
func AskValue(ctx context.Context, label string) (string, error) {
	for {
		first, second, err := runner.AskSecretTwice(ctx, label+": ", "Repeat: ")
		if err != nil {
			return "", fmt.Errorf("%s: %w", label, err)
		}
		switch {
		case first == "":
			runner.Warn(ctx, "The value cannot be empty")
		case first != second:
			runner.Warn(ctx, "The values do not match")
		default:
			return first, nil
		}
	}
}

// parseMapping parses a YAML document that must be a mapping with keys.
func parseMapping(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || len(doc.Content[0].Content) == 0 {
		return nil, errors.New("not a mapping with keys")
	}
	return doc.Content[0], nil
}

// text is the value of a leaf as sops --extract prints it: a scalar's value,
// anything else as YAML.
func text(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	out, err := yaml.Marshal(n)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func marshal(m *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// clone returns a deep copy of n.
func clone(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = clone(child)
	}
	return &c
}

func stripComments(n *yaml.Node) {
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	for _, c := range n.Content {
		stripComments(c)
	}
}

// remove deletes the leaf at p from mapping m, and any mapping it leaves empty.
func remove(m *yaml.Node, p Path) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != p[0] {
			continue
		}
		if child := m.Content[i+1]; len(p) > 1 && child.Kind == yaml.MappingNode {
			remove(child, p[1:])
			if len(child.Content) > 0 {
				return
			}
		}
		m.Content = slices.Delete(m.Content, i, i+2)
		return
	}
}

// setPath sets the value at p in mapping m to v, adding mappings on the way.
func setPath(m *yaml.Node, p Path, v *yaml.Node) {
	for _, k := range p[:len(p)-1] {
		next := value(m, k)
		if next == nil || next.Kind != yaml.MappingNode {
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setValue(m, k, next)
		}
		m = next
	}
	setValue(m, p[len(p)-1], v)
}
