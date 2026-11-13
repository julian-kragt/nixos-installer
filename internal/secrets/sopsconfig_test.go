package secrets

import (
	"bytes"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// The fixture is nix-secrets' .sops.yaml as it was before the installer wrote
// to it: myhost still has its previous host key and the SSH-derived user key.
const (
	oldMyhost = "age10nwtg9l3pjdfn8k8mtnhsekfdaq6f807l0qgmultvxddfszh6ayqflkgka"
	oldUser   = "age10myc9vc0jgjk7c5l6seeawngqvzvu2g8tmsdqkz5hne7rsa284wsh0yr9p"
	myhostKey = "age1myhost"
	jdoeAt    = "age1jdoemyhost"
)

var myhost = Host{Name: "myhost", Recipient: myhostKey, Users: map[string]string{"jdoe": jdoeAt}}

func fixture(t *testing.T) *Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sops.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func marshalConfig(t *testing.T, c *Config) []byte {
	t.Helper()
	out, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// ref is the layout of the fixture: per-host user files.
var ref = ReferenceLayout

// apply is c.Apply(ref, h) on a config in which no rule comes before an
// existing rule of h's files, as 2.2 checks. It returns the removed users.
func apply(t *testing.T, c *Config, h Host) []string {
	t.Helper()
	return applyIn(t, c, ref, h).Removed
}

// applyIn is apply in layout l.
func applyIn(t *testing.T, c *Config, l Layout, h Host) Applied {
	t.Helper()
	files := slices.DeleteFunc(l.Files(h.Name, slices.Sorted(maps.Keys(h.Users))), func(f string) bool { return !c.HasRule(f) })
	if shadowed, err := c.Shadowed(files); err != nil || len(shadowed) != 0 {
		t.Fatalf("shadowed = %+v, %v", shadowed, err)
	}
	return c.Apply(l, h)
}

// golden compares got with testdata/<name>.golden, or rewrites it with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestRoundTripKeepsTheFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sops.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := marshalConfig(t, fixture(t)); !bytes.Equal(got, data) {
		t.Errorf("round trip changed the file:\n%s", got)
	}
}

func TestChangedHostKey(t *testing.T) {
	c := fixture(t)
	got := c.Replacements(myhost)
	want := []Replaced{{Anchor: "host_myhost", Old: oldMyhost, New: myhostKey}}
	if !slices.Equal(got, want) {
		t.Fatalf("replacements = %+v, want %+v", got, want)
	}
	if removed := apply(t, c, myhost); len(removed) != 0 {
		t.Errorf("removed = %q, want none", removed)
	}
	out := marshalConfig(t, c)
	golden(t, "myhost", out)
	// The SSH-derived key leaves myhost' rule but stays for workstation.
	if r, _ := c.Recipients(ref.HostUserFile("myhost", "jdoe")); !slices.Equal(r, []string{myhostKey, jdoeAt}) {
		t.Errorf("myhost-jdoe recipients = %q", r)
	}
	if r, _ := c.Recipients(ref.HostUserFile("workstation", "jdoe")); !slices.Contains(r, oldUser) {
		t.Errorf("workstation-jdoe recipients = %q, want the shared key kept", r)
	}
}

func TestSameHostAgainChangesNothing(t *testing.T) {
	c := fixture(t)
	apply(t, c, myhost)
	first := marshalConfig(t, c)

	again, err := ParseConfig(first)
	if err != nil {
		t.Fatal(err)
	}
	if r := again.Replacements(myhost); len(r) != 0 {
		t.Errorf("replacements = %+v, want none", r)
	}
	apply(t, again, myhost)
	if second := marshalConfig(t, again); !bytes.Equal(first, second) {
		t.Errorf("second apply changed the file:\n%s", second)
	}
}

func TestChangedUserKey(t *testing.T) {
	c := fixture(t)
	apply(t, c, myhost)
	changed := Host{Name: "myhost", Recipient: myhostKey, Users: map[string]string{"jdoe": "age1new"}}
	want := []Replaced{{Anchor: "user_jdoe_myhost", Old: jdoeAt, New: "age1new"}}
	if got := c.Replacements(changed); !slices.Equal(got, want) {
		t.Fatalf("replacements = %+v, want %+v", got, want)
	}
	apply(t, c, changed)
	if r, _ := c.Recipients(ref.HostUserFile("myhost", "jdoe")); !slices.Equal(r, []string{myhostKey, "age1new"}) {
		t.Errorf("recipients = %q", r)
	}
}

func TestNewHostAddedAndRemovedUser(t *testing.T) {
	c := fixture(t)
	nexthost := Host{Name: "nexthost", Recipient: "age1nexthost", Users: map[string]string{"jdoe": "age1jb", "media": "age1mb"}}
	if r := c.Replacements(nexthost); len(r) != 0 {
		t.Errorf("replacements = %+v, want none", r)
	}
	apply(t, c, nexthost)
	golden(t, "nexthost", marshalConfig(t, c))

	nexthost.Users = map[string]string{"jdoe": "age1jb"}
	removed := apply(t, c, nexthost)
	if !slices.Equal(removed, []string{"media"}) {
		t.Errorf("removed = %q, want [media]", removed)
	}
	if _, ok := c.Key("user_media_nexthost"); ok {
		t.Error("anchor of the removed user is still there")
	}
	if _, err := c.Recipients(ref.HostUserFile("nexthost", "media")); err == nil {
		t.Error("rule of the removed user is still there")
	}
}

func TestSameUserOnTwoHosts(t *testing.T) {
	c := fixture(t)
	apply(t, c, myhost)
	apply(t, c, Host{Name: "nexthost", Recipient: "age1nexthost", Users: map[string]string{"jdoe": "age1jb"}})
	for file, want := range map[string][]string{
		ref.HostUserFile("myhost", "jdoe"):   {myhostKey, jdoeAt},
		ref.HostUserFile("nexthost", "jdoe"): {"age1nexthost", "age1jb"},
	} {
		if got, err := c.Recipients(file); err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: recipients = %q, %v, want %q", file, got, err, want)
		}
	}
}

func TestRuleOfAHostWithALongerNameIsNotTouched(t *testing.T) {
	c := fixture(t)
	apply(t, c, Host{Name: "myhost-dev", Recipient: "age1dev", Users: map[string]string{"x": "age1x"}})
	if removed := apply(t, c, Host{Name: "myhost", Recipient: myhostKey}); slices.Contains(removed, "dev-x") {
		t.Errorf("removed = %q, took myhost-dev's user for myhost'", removed)
	}
	if _, err := c.Recipients(ref.HostUserFile("myhost-dev", "x")); err != nil {
		t.Errorf("myhost-dev's rule is gone: %v", err)
	}
}

func TestNewRuleGoesLast(t *testing.T) {
	c := fixture(t)
	apply(t, c, Host{Name: "nexthost", Recipient: "age1nexthost"})
	rules := c.seq("creation_rules").Content
	if v := value(rules[len(rules)-1], "path_regex"); v == nil || v.Value != FileRule(ref.HostFile("nexthost")) {
		t.Errorf("last rule = %v, want nexthost's", v)
	}
}

// sops uses the first rule that matches a file, so a new rule must come before
// a catch-all that would match its file.
func TestNewRuleGoesBeforeACatchAll(t *testing.T) {
	c, err := ParseConfig([]byte("keys:\n  - &everyone age1all\ncreation_rules:\n  - key_groups:\n      - age:\n          - *everyone\n"))
	if err != nil {
		t.Fatal(err)
	}
	apply(t, c, Host{Name: "myhost", Recipient: myhostKey, Users: map[string]string{"jdoe": jdoeAt}})
	for file, want := range map[string][]string{
		ref.HostFile("myhost"):             {myhostKey},
		ref.HostUserFile("myhost", "jdoe"): {myhostKey, jdoeAt},
	} {
		if got, err := c.Recipients(file); err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: recipients = %q, %v; the catch-all shadows the new rule", file, got, err)
		}
	}
	if got, err := c.Recipients("templates/user.yaml"); err != nil || !slices.Equal(got, []string{"age1all"}) {
		t.Errorf("catch-all recipients = %q, %v", got, err)
	}
}

// The legacy script wrote the same rule more than once: every copy changes,
// and a removed user is removed, and reported, once.
func TestDuplicateRules(t *testing.T) {
	const gone = "  - path_regex: ^host-users/myhost-gone\\.yaml$\n    key_groups:\n      - age:\n          - *host_myhost\n          - *user_gone_myhost\n"
	const user = "  - path_regex: ^host-users/myhost-jdoe\\.yaml$\n    key_groups:\n      - age:\n          - *host_myhost\n          - *user_jdoe\n"
	c, err := ParseConfig([]byte("keys:\n  - &host_myhost age1myhost\n  - &user_jdoe age1old\n  - &user_gone_myhost age1gone\ncreation_rules:\n" + gone + user + gone + user))
	if err != nil {
		t.Fatal(err)
	}
	if removed := apply(t, c, myhost); !slices.Equal(removed, []string{"gone"}) {
		t.Errorf("removed = %q, want [gone]", removed)
	}
	again, err := ParseConfig(marshalConfig(t, c))
	if err != nil {
		t.Fatal(err)
	}
	var regexes []string
	for _, r := range again.list("creation_rules") {
		regexes = append(regexes, value(r, "path_regex").Value)
		if aliases, _ := ageKeys(r); slices.Contains(aliases, "user_jdoe") {
			t.Errorf("%s still lists *user_jdoe", value(r, "path_regex").Value)
		}
	}
	if want := []string{FileRule(ref.HostUserFile("myhost", "jdoe")), FileRule(ref.HostUserFile("myhost", "jdoe")), FileRule(ref.HostFile("myhost"))}; !slices.Equal(regexes, want) {
		t.Errorf("rules = %q, want %q", regexes, want)
	}
}

// An anchor of a removed user that something besides a creation rule still
// refers to stays, so the file keeps parsing.
func TestAnchorAliasedOutsideTheRulesStays(t *testing.T) {
	const gone = "  - path_regex: ^host-users/myhost-gone\\.yaml$\n    key_groups:\n      - age:\n          - *host_myhost\n          - *user_gone_myhost\n"
	c, err := ParseConfig([]byte("keys:\n  - &host_myhost age1myhost\n  - &user_gone_myhost age1gone\ncreation_rules:\n" + gone +
		"destination_rules:\n  - s3_bucket: backup\n    key_groups:\n      - age:\n          - *user_gone_myhost\n"))
	if err != nil {
		t.Fatal(err)
	}
	apply(t, c, Host{Name: "myhost", Recipient: myhostKey})
	if _, ok := c.Key("user_gone_myhost"); !ok {
		t.Error("anchor removed although destination_rules uses it")
	}
	if _, err := ParseConfig(marshalConfig(t, c)); err != nil {
		t.Errorf("result does not parse: %v", err)
	}
}

// The editor edits keys and creation_rules as flat lists; it refuses any other
// shape instead of replacing it and the anchors in it.
func TestParseRefusesKeysThatAreNotAList(t *testing.T) {
	for _, data := range []string{
		"keys:\n  hosts:\n    - &host_myhost age1myhost\ncreation_rules: []\n",
		"keys: []\ncreation_rules:\n  path_regex: x\n",
	} {
		if _, err := ParseConfig([]byte(data)); err == nil || !strings.Contains(err.Error(), "not a list") {
			t.Errorf("%q: err = %v, want 'not a list'", data, err)
		}
	}
	if _, err := ParseConfig([]byte("keys:\ncreation_rules:\n")); err != nil {
		t.Errorf("empty keys and creation_rules: %v", err)
	}
}

// Reading a configuration never changes it.
func TestReadsDoNotChangeTheConfig(t *testing.T) {
	const data = "keys:\n  - &host_myhost age1myhost\n"
	c, err := ParseConfig([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	c.Recipients(ref.HostFile("myhost"))
	c.Shadowed([]string{ref.HostFile("myhost")})
	c.HasRule(ref.HostFile("myhost"))
	c.Replacements(myhost)
	if got := string(marshalConfig(t, c)); got != data {
		t.Errorf("after reads:\n%s\nwant:\n%s", got, data)
	}
}

func TestEmptyConfig(t *testing.T) {
	c, err := ParseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, c, myhost)
	golden(t, "empty", marshalConfig(t, c))
}

func TestRecipientsOfAnUncoveredFile(t *testing.T) {
	if _, err := fixture(t).Recipients("hosts/nowhere.yaml"); err == nil || !strings.Contains(err.Error(), "no creation rule") {
		t.Errorf("err = %v", err)
	}
}

func TestFileRule(t *testing.T) {
	if got, want := FileRule(ref.HostFile("myhost-dev")), `^hosts/myhost-dev\.yaml$`; got != want {
		t.Errorf("HostRule = %s, want %s", got, want)
	}
	if got, want := FileRule(ref.HostUserFile("myhost", "jdoe")), `^host-users/myhost-jdoe\.yaml$`; got != want {
		t.Errorf("UserRule = %s, want %s", got, want)
	}
}

func TestNothingShadowedInTheFixture(t *testing.T) {
	files := []string{ref.HostFile("myhost"), ref.HostFile("workstation"), ref.HostUserFile("myhost", "jdoe"), ref.HostUserFile("workstation", "jdoe"), ref.HostFile("nexthost")}
	if shadowed, err := fixture(t).Shadowed(files); err != nil || len(shadowed) != 0 {
		t.Errorf("shadowed = %+v, %v, want none", shadowed, err)
	}
}

// sops uses the first rule that matches a file, so a broader rule before a
// file's own rule, or before where Apply adds it, would win.
func TestShadowed(t *testing.T) {
	const broad = "  - path_regex: ^hosts/.*\\.yaml$\n    key_groups:\n      - age:\n          - *admin\n"
	const noRegex = "  - key_groups:\n      - age:\n          - *admin\n"
	const own = "  - path_regex: ^hosts/myhost\\.yaml$\n    key_groups:\n      - age:\n          - *host_myhost\n"
	const other = "  - path_regex: ^hosts/workstation\\.yaml$\n    key_groups:\n      - age:\n          - *admin\n"
	files := []string{ref.HostFile("myhost"), ref.HostUserFile("myhost", "jdoe"), ref.HostFile("myhost")}
	tests := []struct {
		name, rules string
		want        []Shadow
	}{
		{"broad rule before the own one", broad + own, []Shadow{{ref.HostFile("myhost"), `path_regex ^hosts/.*\.yaml$`}}},
		{"broad rule and no own one", broad, []Shadow{{ref.HostFile("myhost"), `path_regex ^hosts/.*\.yaml$`}}},
		{"rule without path_regex", noRegex + own, []Shadow{
			{ref.HostFile("myhost"), "creation rule 1, which has no path_regex"},
			{ref.HostUserFile("myhost", "jdoe"), "creation rule 1, which has no path_regex"},
		}},
		{"own rule first", own + broad + noRegex, []Shadow{{ref.HostUserFile("myhost", "jdoe"), "creation rule 3, which has no path_regex"}}},
		{"another host's rule only", other, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseConfig([]byte("keys:\n  - &admin age1admin\n  - &host_myhost age1old\ncreation_rules:\n" + tt.rules))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := c.Shadowed(files); err != nil || !slices.Equal(got, tt.want) {
				t.Errorf("shadowed = %+v, %v, want %+v", got, err, tt.want)
			}
		})
	}
}

func TestShadowedReportsABrokenRegex(t *testing.T) {
	c, err := ParseConfig([]byte("creation_rules:\n  - path_regex: ^hosts/(\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Shadowed([]string{ref.HostFile("myhost")}); err == nil || !strings.Contains(err.Error(), "path_regex") {
		t.Errorf("err = %v, want the broken path_regex named", err)
	}
}

// sops also encrypts to a recipient written literally into a rule, so it stays
// and counts, also when the rule's aliases change.
func TestLiteralRecipientsInARuleStay(t *testing.T) {
	c, err := ParseConfig([]byte("keys:\n  - &host_myhost age1myhost\ncreation_rules:\n" +
		"  - path_regex: ^hosts/myhost\\.yaml$\n    key_groups:\n      - age:\n          - *host_myhost\n          - age1admin\n" +
		"  - path_regex: ^host-users/myhost-jdoe\\.yaml$\n    key_groups:\n      - age:\n          - age1admin\n          - *host_myhost\n"))
	if err != nil {
		t.Fatal(err)
	}
	apply(t, c, myhost)
	again, err := ParseConfig(marshalConfig(t, c))
	if err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string][]string{
		ref.HostFile("myhost"):             {myhostKey, "age1admin"},
		ref.HostUserFile("myhost", "jdoe"): {myhostKey, jdoeAt, "age1admin"},
	} {
		if got, err := again.Recipients(file); err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: recipients = %q, %v, want %q", file, got, err, want)
		}
	}
}

// A new rule that goes before a rule matching its file takes that file from
// it, so it is listed; a catch-all without path_regex is not.
func TestInsertions(t *testing.T) {
	const keys = "keys:\n  - &admin age1admin\ncreation_rules:\n"
	const unescaped = "  - path_regex: ^hosts/myhost.yaml$\n    key_groups:\n      - age:\n          - *admin\n"
	const userGlob = "  - path_regex: ^host-users/myhost-.*\\.yaml$\n    key_groups:\n      - age:\n          - *admin\n"
	const own = "  - path_regex: ^hosts/myhost\\.yaml$\n    key_groups:\n      - age:\n          - *admin\n"
	const noRegex = "  - key_groups:\n      - age:\n          - *admin\n"
	h := Host{Name: "myhost", Recipient: "age1myhost", Users: map[string]string{"jdoe": "age1jdoe"}}
	tests := []struct {
		name, rules string
		want        []Inserted
	}{
		{"differently written rule", unescaped, []Inserted{{ref.HostFile("myhost"), "^hosts/myhost.yaml$"}}},
		{"rule for every user file", userGlob, []Inserted{{ref.HostUserFile("myhost", "jdoe"), `^host-users/myhost-.*\.yaml$`}}},
		{"own rule", own, nil},
		{"catch-all", noRegex, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseConfig([]byte(keys + tt.rules))
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Insertions(ref, h)
			if err != nil || !slices.Equal(got, tt.want) {
				t.Errorf("insertions = %+v, %v, want %+v", got, err, tt.want)
			}
		})
	}
}

// A broken path_regex is reported before Apply would add a rule past it.
func TestInsertionsReportsABrokenRegex(t *testing.T) {
	c, err := ParseConfig([]byte("creation_rules:\n  - path_regex: ^hosts/(\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Insertions(ref, Host{Name: "myhost", Recipient: "age1myhost"}); err == nil || !strings.Contains(err.Error(), "path_regex") {
		t.Errorf("err = %v, want the broken path_regex named", err)
	}
}

// sharedOnTwoHosts is an empty config after installing myhost and then
// nexthost, both with jdoe, in layout l.
func sharedOnTwoHosts(t *testing.T, l Layout) *Config {
	t.Helper()
	c, err := ParseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	applyIn(t, c, l, myhost)
	applyIn(t, c, l, Host{Name: "nexthost", Recipient: "age1nexthost", Users: map[string]string{"jdoe": "age1jb"}})
	return c
}

func TestSharedFileGoesToEveryHostOfItsUser(t *testing.T) {
	c := sharedOnTwoHosts(t, shared)
	if r, _ := c.Recipients("users/jdoe.yaml"); !slices.Equal(r, []string{myhostKey, jdoeAt, "age1nexthost", "age1jb"}) {
		t.Errorf("recipients = %q", r)
	}
	hosts, err := c.RuleHosts("users/jdoe.yaml")
	if want := []HostKey{{"myhost", myhostKey}, {"nexthost", "age1nexthost"}}; err != nil || !slices.Equal(hosts, want) {
		t.Errorf("hosts = %+v, %v, want %+v", hosts, err, want)
	}
	if c.HasRule("host-users/myhost-jdoe.yaml") {
		t.Error("shared layout got a per-host user rule")
	}

	before := marshalConfig(t, c)
	applyIn(t, c, shared, myhost)
	if after := marshalConfig(t, c); !bytes.Equal(before, after) {
		t.Errorf("same host again changed the config:\n%s", after)
	}
}

func TestUserLeavesASharedFile(t *testing.T) {
	c := sharedOnTwoHosts(t, shared)
	got := applyIn(t, c, shared, Host{Name: "nexthost", Recipient: "age1nexthost"})
	if want := (Applied{Removed: []string{"jdoe"}, Left: []string{"users/jdoe.yaml"}}); !slices.Equal(got.Removed, want.Removed) || !slices.Equal(got.Left, want.Left) || got.Emptied != nil {
		t.Errorf("applied = %+v, want %+v", got, want)
	}
	if r, _ := c.Recipients("users/jdoe.yaml"); !slices.Equal(r, []string{myhostKey, jdoeAt}) {
		t.Errorf("recipients = %q", r)
	}
	if _, ok := c.Key(UserAnchor("nexthost", "jdoe")); ok {
		t.Error("the anchor of jdoe on nexthost stayed")
	}

	got = applyIn(t, c, shared, Host{Name: "myhost", Recipient: myhostKey})
	if !slices.Equal(got.Emptied, []string{"users/jdoe.yaml"}) || got.Left != nil {
		t.Errorf("last host: applied = %+v", got)
	}
	if c.HasRule("users/jdoe.yaml") {
		t.Error("the rule of a file nobody reads stayed")
	}
}

func TestLiteralRecipientKeepsASharedFile(t *testing.T) {
	c, err := ParseConfig([]byte("creation_rules:\n  - path_regex: ^users/jdoe\\.yaml$\n    key_groups:\n      - age:\n          - age1admin\n"))
	if err != nil {
		t.Fatal(err)
	}
	applyIn(t, c, shared, myhost)
	if r, _ := c.Recipients("users/jdoe.yaml"); !slices.Equal(r, []string{myhostKey, jdoeAt, "age1admin"}) {
		t.Errorf("recipients = %q", r)
	}
	got := applyIn(t, c, shared, Host{Name: "myhost", Recipient: myhostKey})
	if !slices.Equal(got.Left, []string{"users/jdoe.yaml"}) || got.Emptied != nil {
		t.Errorf("applied = %+v", got)
	}
}

func TestBothModeHasBothRules(t *testing.T) {
	c := sharedOnTwoHosts(t, both)
	want := map[string][]string{
		"host-users/myhost-jdoe.yaml":   {myhostKey, jdoeAt},
		"host-users/nexthost-jdoe.yaml": {"age1nexthost", "age1jb"},
		"users/jdoe.yaml":               {myhostKey, jdoeAt, "age1nexthost", "age1jb"},
	}
	for file, w := range want {
		if r, _ := c.Recipients(file); !slices.Equal(r, w) {
			t.Errorf("%s: recipients = %q, want %q", file, r, w)
		}
	}
	got := applyIn(t, c, both, Host{Name: "nexthost", Recipient: "age1nexthost"})
	if !slices.Equal(got.Removed, []string{"jdoe"}) || !slices.Equal(got.Left, []string{"users/jdoe.yaml"}) {
		t.Errorf("applied = %+v", got)
	}
	if c.HasRule("host-users/nexthost-jdoe.yaml") {
		t.Error("the per-host rule of a removed user stayed")
	}
}
