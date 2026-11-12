package secrets

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is a .sops.yaml as a yaml.v3 node tree, so anchors, aliases and
// comments survive an edit. Recipients are anchored entries of `keys`; a
// creation rule lists them by alias, or literally when an operator wrote one in.
type Config struct {
	root *yaml.Node // the top-level mapping
}

// ParseConfig reads a .sops.yaml. An empty file is an empty configuration.
func ParseConfig(data []byte) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ConfigFile, err)
	}
	if doc.Kind == 0 {
		return &Config{root: &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}}, nil
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse %s: not a mapping", ConfigFile)
	}
	root := doc.Content[0]
	// The editor reads and writes these as flat lists. Any other shape, such as
	// keys grouped in a mapping, would be lost along with the anchors in it.
	for _, key := range []string{"keys", "creation_rules"} {
		if v := value(root, key); v != nil && v.Kind != yaml.SequenceNode && v.Tag != "!!null" {
			return nil, fmt.Errorf("parse %s: %s is not a list; the installer edits only a flat list there", ConfigFile, key)
		}
	}
	return &Config{root: root}, nil
}

// Marshal writes c as YAML, indented the way nix-secrets is.
func (c *Config) Marshal() ([]byte, error) {
	out, err := marshal(c.root)
	if err != nil {
		return nil, fmt.Errorf("write %s: %w", ConfigFile, err)
	}
	return out, nil
}

// HostAnchor names the recipient of host's own age key.
func HostAnchor(host string) string { return "host_" + host }

// UserAnchor names the recipient of user's age key on host.
func UserAnchor(host, user string) string { return "user_" + user + "_" + host }

// FileRule is the path_regex of the rule of file, relative to the repository:
// it matches that file only.
func FileRule(file string) string { return "^" + regexp.QuoteMeta(file) + "$" }

// HostRule is the path_regex of host's own file.
func HostRule(host string) string { return FileRule(HostFile(host)) }

// UserRule is the path_regex of user's file on host.
func UserRule(host, user string) string { return FileRule(UserFile(host, user)) }

// Host is what .sops.yaml must say about one host's files: its own file is
// encrypted to the host, a user's file to the host and that user's key on it.
type Host struct {
	Name      string
	Recipient string            // the host's age recipient
	Users     map[string]string // each user's age recipient on the host
}

// anchor is an anchored key of .sops.yaml.
type anchor struct{ name, recipient string }

// anchors returns every anchor h needs: the host's first, then its users' by
// name, so new keys are always added in the same order.
func (h Host) anchors() []anchor {
	want := []anchor{{HostAnchor(h.Name), h.Recipient}}
	for _, u := range slices.Sorted(maps.Keys(h.Users)) {
		want = append(want, anchor{UserAnchor(h.Name, u), h.Users[u]})
	}
	return want
}

// Replaced is an anchor that exists with another recipient than the one wanted.
type Replaced struct{ Anchor, Old, New string }

// Replacements lists the anchors of h that Apply would give another
// recipient: the host's first, then its users' by name.
func (c *Config) Replacements(h Host) []Replaced {
	var out []Replaced
	for _, a := range h.anchors() {
		if have, ok := c.Key(a.name); ok && have != a.recipient {
			out = append(out, Replaced{Anchor: a.name, Old: have, New: a.recipient})
		}
	}
	return out
}

// Apply makes c say exactly h about h's files: the anchors with their
// recipients, one rule per file with exactly its recipients. A recipient an
// operator wrote literally into such a rule stays. Rules and anchors of users
// no longer on the host are removed; it returns those users, sorted. An anchor
// that a rule of another host still uses stays. sops uses the first rule that
// matches a file: a missing rule is added before the first rule that matches
// its file, but check with Shadowed first that no rule comes before an
// existing one.
func (c *Config) Apply(h Host) []string {
	for _, a := range h.anchors() {
		c.setKey(a.name, a.recipient)
	}
	c.setRule(HostFile(h.Name), HostAnchor(h.Name))
	for _, u := range slices.Sorted(maps.Keys(h.Users)) {
		c.setRule(UserFile(h.Name, u), HostAnchor(h.Name), UserAnchor(h.Name, u))
	}

	var removed []string
	for _, u := range c.ruleUsers(h.Name) {
		if _, ok := h.Users[u]; ok {
			continue
		}
		c.removeRule(UserRule(h.Name, u))
		if !c.uses(UserAnchor(h.Name, u)) {
			c.removeKey(UserAnchor(h.Name, u))
		}
		removed = append(removed, u)
	}
	return removed
}

// Shadow is a file that sops would encrypt with another rule than its own,
// because that rule comes first and matches it too.
type Shadow struct {
	File string
	Rule string // the rule sops would use, as the operator finds it in .sops.yaml
}

func (s Shadow) String() string {
	return fmt.Sprintf("%s: sops would use %s, not %s", s.File, s.Rule, FileRule(s.File))
}

// Shadowed returns the files, relative to the repository, whose first matching
// rule is not their own (FileRule), in the order given. A file that no rule
// matches is not one. A file without a rule of its own that another rule
// matches is one: sops would use that rule.
func (c *Config) Shadowed(files []string) ([]Shadow, error) {
	var out []Shadow
	seen := map[string]bool{}
	for _, file := range files {
		if seen[file] {
			continue
		}
		seen[file] = true
		rule, i, err := c.firstRule(file)
		if err != nil {
			return nil, err
		}
		if rule == nil {
			continue
		}
		v := value(rule, "path_regex")
		switch {
		case v == nil:
			out = append(out, Shadow{File: file, Rule: fmt.Sprintf("creation rule %d, which has no path_regex", i+1)})
		case v.Value != FileRule(file):
			out = append(out, Shadow{File: file, Rule: "path_regex " + v.Value})
		}
	}
	return out, nil
}

// HasRule reports whether file, relative to the repository, has a rule of its
// own (FileRule), wherever it is.
func (c *Config) HasRule(file string) bool { return c.ruleIndex(FileRule(file)) >= 0 }

// Recipients returns the recipients of the rule sops uses for file, the first
// that matches it: those it lists by alias, then those it lists literally.
func (c *Config) Recipients(file string) ([]string, error) {
	rule, _, err := c.firstRule(file)
	if err != nil {
		return nil, err
	}
	if rule == nil {
		return nil, errors.New(ConfigFile + ": no creation rule matches " + file)
	}
	aliases, literals := ageKeys(rule)
	var out []string
	for _, alias := range aliases {
		r, ok := c.Key(alias)
		if !ok {
			return nil, fmt.Errorf("%s: the rule for %s uses *%s, which is not a key", ConfigFile, file, alias)
		}
		out = append(out, r)
	}
	return append(out, literals...), nil
}

// firstRule returns the rule sops uses for file and its index, or nil and -1
// when no rule matches it.
func (c *Config) firstRule(file string) (*yaml.Node, int, error) {
	for i, rule := range c.list("creation_rules") {
		ok, err := matches(rule, file)
		if err != nil {
			return nil, -1, err
		}
		if ok {
			return rule, i, nil
		}
	}
	return nil, -1, nil
}

// Key returns the recipient anchored as anchor in keys.
func (c *Config) Key(anchor string) (string, bool) {
	for _, k := range c.list("keys") {
		if k.Anchor == anchor {
			return k.Value, true
		}
	}
	return "", false
}

// setKey gives anchor the recipient, adding the key when it is missing.
func (c *Config) setKey(anchor, recipient string) {
	keys := c.seq("keys")
	for _, k := range keys.Content {
		if k.Anchor == anchor {
			k.Value = recipient
			return
		}
	}
	keys.Content = append(keys.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: recipient, Anchor: anchor})
}

func (c *Config) removeKey(anchor string) {
	keys := c.seq("keys")
	keys.Content = slices.DeleteFunc(keys.Content, func(k *yaml.Node) bool { return k.Anchor == anchor })
}

// setRule makes the rule of file, relative to the repository, list exactly
// anchors by alias, in one age key group, which keeps the rule's literal
// recipients. Every copy of an existing rule is changed where it is. A
// missing rule is added before the first rule that matches file, so sops uses
// it, or at the end. A rule that already says this is left as it is.
func (c *Config) setRule(file string, anchors ...string) {
	regex := FileRule(file)
	found := false
	for _, rule := range c.list("creation_rules") {
		if v := value(rule, "path_regex"); v == nil || v.Value != regex {
			continue
		}
		found = true
		aliases, literals := ageKeys(rule)
		if groups := value(rule, "key_groups"); !slices.Equal(aliases, anchors) || groups == nil || len(groups.Content) != 1 {
			setValue(rule, "key_groups", keyGroups(anchors, literals))
		}
	}
	if found {
		return
	}
	rule := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setValue(rule, "path_regex", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: regex})
	setValue(rule, "key_groups", keyGroups(anchors, nil))
	rules := c.seq("creation_rules")
	// The rules were matched by Shadowed or Recipients before, so a broken
	// regex was reported there; here it only counts as no match.
	at := slices.IndexFunc(rules.Content, func(r *yaml.Node) bool {
		ok, err := matches(r, file)
		return ok && err == nil
	})
	if at < 0 {
		at = len(rules.Content)
	}
	rules.Content = slices.Insert(rules.Content, at, rule)
}

// removeRule removes every rule whose path_regex is regex.
func (c *Config) removeRule(regex string) {
	if c.ruleIndex(regex) < 0 {
		return
	}
	rules := c.seq("creation_rules")
	rules.Content = slices.DeleteFunc(rules.Content, func(r *yaml.Node) bool {
		v := value(r, "path_regex")
		return v != nil && v.Value == regex
	})
}

func (c *Config) ruleIndex(regex string) int {
	return slices.IndexFunc(c.list("creation_rules"), func(r *yaml.Node) bool {
		v := value(r, "path_regex")
		return v != nil && v.Value == regex
	})
}

// uses reports whether anything in the configuration refers to anchor by
// alias, in a creation rule or elsewhere.
func (c *Config) uses(anchor string) bool {
	var walk func(n *yaml.Node) bool
	walk = func(n *yaml.Node) bool {
		if n.Kind == yaml.AliasNode && n.Value == anchor {
			return true
		}
		return slices.ContainsFunc(n.Content, walk)
	}
	return walk(c.root)
}

// ruleUsers returns the users, sorted and once each, that have a rule for a
// file of host and whose rule lists the host's key.
func (c *Config) ruleUsers(host string) []string {
	prefix, suffix := `^host-users/`+regexp.QuoteMeta(host)+"-", `\.yaml$`
	var users []string
	for _, r := range c.list("creation_rules") {
		v := value(r, "path_regex")
		if v == nil || !strings.HasPrefix(v.Value, prefix) || !strings.HasSuffix(v.Value, suffix) {
			continue
		}
		u := strings.TrimSuffix(strings.TrimPrefix(v.Value, prefix), suffix)
		if aliases, _ := ageKeys(r); userName.MatchString(u) && slices.Contains(aliases, HostAnchor(host)) {
			users = append(users, u)
		}
	}
	slices.Sort(users)
	return slices.Compact(users)
}

// list returns the items of the top-level sequence under key, or nil when
// there is none. Unlike seq it never changes c.
func (c *Config) list(key string) []*yaml.Node {
	if v := value(c.root, key); v != nil && v.Kind == yaml.SequenceNode {
		return v.Content
	}
	return nil
}

// seq returns the top-level sequence under key, adding an empty one when it is
// missing or null. ParseConfig refuses any other kind of value there.
func (c *Config) seq(key string) *yaml.Node {
	if v := value(c.root, key); v != nil && v.Kind == yaml.SequenceNode {
		return v
	}
	s := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	setValue(c.root, key, s)
	return s
}

// value returns the value of key in mapping m, or nil.
func value(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setValue sets key in mapping m to v, adding it at the end when it is missing.
func setValue(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

// keyGroups is a key_groups value with one age group listing anchors by alias,
// then the recipients literals.
func keyGroups(anchors, literals []string) *yaml.Node {
	age := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, a := range anchors {
		age.Content = append(age.Content, &yaml.Node{Kind: yaml.AliasNode, Value: a})
	}
	for _, r := range literals {
		age.Content = append(age.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: r})
	}
	group := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setValue(group, "age", age)
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{group}}
}

// ageKeys returns what the age groups of rule list, in order: the anchors it
// lists by alias and the recipients it lists literally.
func ageKeys(rule *yaml.Node) (aliases, literals []string) {
	groups := value(rule, "key_groups")
	if groups == nil {
		return nil, nil
	}
	for _, g := range groups.Content {
		age := value(g, "age")
		if age == nil {
			continue
		}
		for _, a := range age.Content {
			switch a.Kind {
			case yaml.AliasNode:
				aliases = append(aliases, a.Value)
			case yaml.ScalarNode:
				literals = append(literals, a.Value)
			}
		}
	}
	return aliases, literals
}

// matches reports whether sops takes rule for file: its path_regex matches
// file, or it has none and so matches every file.
func matches(rule *yaml.Node, file string) (bool, error) {
	v := value(rule, "path_regex")
	if v == nil {
		return true, nil
	}
	re, err := regexp.Compile(v.Value)
	if err != nil {
		return false, fmt.Errorf("%s: path_regex %q: %w", ConfigFile, v.Value, err)
	}
	return re.MatchString(file), nil
}
