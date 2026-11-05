package secrets

import (
	"bytes"
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
// creation rule lists them by alias.
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
	return &Config{root: doc.Content[0]}, nil
}

// Marshal writes c as YAML, indented the way nix-secrets is.
func (c *Config) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{c.root}}
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("write %s: %w", ConfigFile, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("write %s: %w", ConfigFile, err)
	}
	return buf.Bytes(), nil
}

// HostAnchor names the recipient of host's own age key.
func HostAnchor(host string) string { return "host_" + host }

// UserAnchor names the recipient of user's age key on host.
func UserAnchor(host, user string) string { return "user_" + user + "_" + host }

// HostRule is the path_regex of host's own file.
func HostRule(host string) string { return `^hosts/` + regexp.QuoteMeta(host) + `\.yaml$` }

// UserRule is the path_regex of user's file on host.
func UserRule(host, user string) string {
	return `^host-users/` + regexp.QuoteMeta(host) + "-" + regexp.QuoteMeta(user) + `\.yaml$`
}

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
// recipients, one rule per file with exactly its recipients. Rules and anchors
// of users no longer on the host are removed; it returns those users, sorted.
// An anchor that a rule of another host still uses stays.
func (c *Config) Apply(h Host) []string {
	for _, a := range h.anchors() {
		c.setKey(a.name, a.recipient)
	}
	c.setRule(HostRule(h.Name), HostAnchor(h.Name))
	for _, u := range slices.Sorted(maps.Keys(h.Users)) {
		c.setRule(UserRule(h.Name, u), HostAnchor(h.Name), UserAnchor(h.Name, u))
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

// Recipients returns the recipients of the rule sops uses for file, the first
// whose path_regex matches it.
func (c *Config) Recipients(file string) ([]string, error) {
	for _, rule := range c.seq("creation_rules").Content {
		regex := value(rule, "path_regex")
		if regex == nil {
			continue
		}
		re, err := regexp.Compile(regex.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: path_regex %q: %w", ConfigFile, regex.Value, err)
		}
		if !re.MatchString(file) {
			continue
		}
		var out []string
		for _, alias := range ageAliases(rule) {
			r, ok := c.Key(alias)
			if !ok {
				return nil, fmt.Errorf("%s: rule %q uses *%s, which is not a key", ConfigFile, regex.Value, alias)
			}
			out = append(out, r)
		}
		return out, nil
	}
	return nil, errors.New(ConfigFile + ": no creation rule matches " + file)
}

// Key returns the recipient anchored as anchor in keys.
func (c *Config) Key(anchor string) (string, bool) {
	for _, k := range c.seq("keys").Content {
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

// setRule makes the rule for regex list exactly anchors, in one age key group.
// A missing rule goes before the first catch-all, because sops uses the first
// rule that matches. A rule that already says this is left as it is.
func (c *Config) setRule(regex string, anchors ...string) {
	rules := c.seq("creation_rules")
	groups := keyGroups(anchors)
	if i := c.ruleIndex(regex); i >= 0 {
		if !slices.Equal(ageAliases(rules.Content[i]), anchors) || len(value(rules.Content[i], "key_groups").Content) != 1 {
			setValue(rules.Content[i], "key_groups", groups)
		}
		return
	}
	rule := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setValue(rule, "path_regex", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: regex})
	setValue(rule, "key_groups", groups)
	at := slices.IndexFunc(rules.Content, isCatchAll)
	if at < 0 {
		at = len(rules.Content)
	}
	rules.Content = slices.Insert(rules.Content, at, rule)
}

func (c *Config) removeRule(regex string) {
	if i := c.ruleIndex(regex); i >= 0 {
		rules := c.seq("creation_rules")
		rules.Content = slices.Delete(rules.Content, i, i+1)
	}
}

func (c *Config) ruleIndex(regex string) int {
	return slices.IndexFunc(c.seq("creation_rules").Content, func(r *yaml.Node) bool {
		v := value(r, "path_regex")
		return v != nil && v.Value == regex
	})
}

// uses reports whether any rule lists anchor.
func (c *Config) uses(anchor string) bool {
	return slices.ContainsFunc(c.seq("creation_rules").Content, func(r *yaml.Node) bool {
		return slices.Contains(ageAliases(r), anchor)
	})
}

// userName is what a user file's rule may name: a user name without dashes or
// regex syntax, so a rule of a host whose name starts like host's is never read
// as one of host's.
var userName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// ruleUsers returns the users that have a rule for a file of host and whose
// rule lists the host's key.
func (c *Config) ruleUsers(host string) []string {
	prefix, suffix := `^host-users/`+regexp.QuoteMeta(host)+"-", `\.yaml$`
	var users []string
	for _, r := range c.seq("creation_rules").Content {
		v := value(r, "path_regex")
		if v == nil || !strings.HasPrefix(v.Value, prefix) || !strings.HasSuffix(v.Value, suffix) {
			continue
		}
		u := strings.TrimSuffix(strings.TrimPrefix(v.Value, prefix), suffix)
		if userName.MatchString(u) && slices.Contains(ageAliases(r), HostAnchor(host)) {
			users = append(users, u)
		}
	}
	return users
}

// seq returns the top-level sequence under key, adding an empty one when it is missing.
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

// keyGroups is a key_groups value with one age group listing anchors by alias.
func keyGroups(anchors []string) *yaml.Node {
	age := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, a := range anchors {
		age.Content = append(age.Content, &yaml.Node{Kind: yaml.AliasNode, Value: a})
	}
	group := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setValue(group, "age", age)
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{group}}
}

// ageAliases returns the anchors the age groups of rule list, in order.
func ageAliases(rule *yaml.Node) []string {
	var out []string
	groups := value(rule, "key_groups")
	if groups == nil {
		return nil
	}
	for _, g := range groups.Content {
		age := value(g, "age")
		if age == nil {
			continue
		}
		for _, a := range age.Content {
			if a.Kind == yaml.AliasNode {
				out = append(out, a.Value)
			}
		}
	}
	return out
}

// isCatchAll reports whether rule may match files of any host: it has no
// path_regex, or one that is not anchored at the start.
func isCatchAll(rule *yaml.Node) bool {
	v := value(rule, "path_regex")
	return v == nil || !strings.HasPrefix(v.Value, "^")
}
