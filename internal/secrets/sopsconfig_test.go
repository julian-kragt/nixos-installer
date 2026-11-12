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
// to it: atlas still has its previous host key and the SSH-derived user key.
const (
	oldAtlas = "age10nwtg9l3pjdfn8k8mtnhsekfdaq6f807l0qgmultvxddfszh6ayqflkgka"
	oldUser  = "age10myc9vc0jgjk7c5l6seeawngqvzvu2g8tmsdqkz5hne7rsa284wsh0yr9p"
	atlasKey = "age1atlas"
	jkragtAt = "age1jkragtatlas"
)

var atlas = Host{Name: "atlas", Recipient: atlasKey, Users: map[string]string{"jkragt": jkragtAt}}

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

// apply is c.Apply(h) on a config in which no rule comes before an existing
// rule of h's files, as 2.2 checks.
func apply(t *testing.T, c *Config, h Host) []string {
	t.Helper()
	files := slices.DeleteFunc(Files(h.Name, slices.Sorted(maps.Keys(h.Users))), func(f string) bool { return !c.HasRule(f) })
	if shadowed, err := c.Shadowed(files); err != nil || len(shadowed) != 0 {
		t.Fatalf("shadowed = %+v, %v", shadowed, err)
	}
	return c.Apply(h)
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
	got := c.Replacements(atlas)
	want := []Replaced{{Anchor: "host_atlas", Old: oldAtlas, New: atlasKey}}
	if !slices.Equal(got, want) {
		t.Fatalf("replacements = %+v, want %+v", got, want)
	}
	if removed := apply(t, c, atlas); len(removed) != 0 {
		t.Errorf("removed = %q, want none", removed)
	}
	out := marshalConfig(t, c)
	golden(t, "atlas", out)
	// The SSH-derived key leaves atlas' rule but stays for workhorse.
	if r, _ := c.Recipients(UserFile("atlas", "jkragt")); !slices.Equal(r, []string{atlasKey, jkragtAt}) {
		t.Errorf("atlas-jkragt recipients = %q", r)
	}
	if r, _ := c.Recipients(UserFile("workhorse", "jkragt")); !slices.Contains(r, oldUser) {
		t.Errorf("workhorse-jkragt recipients = %q, want the shared key kept", r)
	}
}

func TestSameHostAgainChangesNothing(t *testing.T) {
	c := fixture(t)
	apply(t, c, atlas)
	first := marshalConfig(t, c)

	again, err := ParseConfig(first)
	if err != nil {
		t.Fatal(err)
	}
	if r := again.Replacements(atlas); len(r) != 0 {
		t.Errorf("replacements = %+v, want none", r)
	}
	apply(t, again, atlas)
	if second := marshalConfig(t, again); !bytes.Equal(first, second) {
		t.Errorf("second apply changed the file:\n%s", second)
	}
}

func TestChangedUserKey(t *testing.T) {
	c := fixture(t)
	apply(t, c, atlas)
	changed := Host{Name: "atlas", Recipient: atlasKey, Users: map[string]string{"jkragt": "age1new"}}
	want := []Replaced{{Anchor: "user_jkragt_atlas", Old: jkragtAt, New: "age1new"}}
	if got := c.Replacements(changed); !slices.Equal(got, want) {
		t.Fatalf("replacements = %+v, want %+v", got, want)
	}
	apply(t, c, changed)
	if r, _ := c.Recipients(UserFile("atlas", "jkragt")); !slices.Equal(r, []string{atlasKey, "age1new"}) {
		t.Errorf("recipients = %q", r)
	}
}

func TestNewHostAddedAndRemovedUser(t *testing.T) {
	c := fixture(t)
	broadway := Host{Name: "broadway", Recipient: "age1broadway", Users: map[string]string{"jkragt": "age1jb", "media": "age1mb"}}
	if r := c.Replacements(broadway); len(r) != 0 {
		t.Errorf("replacements = %+v, want none", r)
	}
	apply(t, c, broadway)
	golden(t, "broadway", marshalConfig(t, c))

	broadway.Users = map[string]string{"jkragt": "age1jb"}
	removed := apply(t, c, broadway)
	if !slices.Equal(removed, []string{"media"}) {
		t.Errorf("removed = %q, want [media]", removed)
	}
	if _, ok := c.Key("user_media_broadway"); ok {
		t.Error("anchor of the removed user is still there")
	}
	if _, err := c.Recipients(UserFile("broadway", "media")); err == nil {
		t.Error("rule of the removed user is still there")
	}
}

func TestSameUserOnTwoHosts(t *testing.T) {
	c := fixture(t)
	apply(t, c, atlas)
	apply(t, c, Host{Name: "broadway", Recipient: "age1broadway", Users: map[string]string{"jkragt": "age1jb"}})
	for file, want := range map[string][]string{
		UserFile("atlas", "jkragt"):    {atlasKey, jkragtAt},
		UserFile("broadway", "jkragt"): {"age1broadway", "age1jb"},
	} {
		if got, err := c.Recipients(file); err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: recipients = %q, %v, want %q", file, got, err, want)
		}
	}
}

func TestRuleOfAHostWithALongerNameIsNotTouched(t *testing.T) {
	c := fixture(t)
	apply(t, c, Host{Name: "atlas-dev", Recipient: "age1dev", Users: map[string]string{"x": "age1x"}})
	if removed := apply(t, c, Host{Name: "atlas", Recipient: atlasKey}); slices.Contains(removed, "dev-x") {
		t.Errorf("removed = %q, took atlas-dev's user for atlas'", removed)
	}
	if _, err := c.Recipients(UserFile("atlas-dev", "x")); err != nil {
		t.Errorf("atlas-dev's rule is gone: %v", err)
	}
}

func TestNewRuleGoesLast(t *testing.T) {
	c := fixture(t)
	apply(t, c, Host{Name: "broadway", Recipient: "age1broadway"})
	rules := c.seq("creation_rules").Content
	if v := value(rules[len(rules)-1], "path_regex"); v == nil || v.Value != HostRule("broadway") {
		t.Errorf("last rule = %v, want broadway's", v)
	}
}

// sops uses the first rule that matches a file, so a new rule must come before
// a catch-all that would match its file.
func TestNewRuleGoesBeforeACatchAll(t *testing.T) {
	c, err := ParseConfig([]byte("keys:\n  - &everyone age1all\ncreation_rules:\n  - key_groups:\n      - age:\n          - *everyone\n"))
	if err != nil {
		t.Fatal(err)
	}
	apply(t, c, Host{Name: "atlas", Recipient: atlasKey, Users: map[string]string{"jkragt": jkragtAt}})
	for file, want := range map[string][]string{
		HostFile("atlas"):           {atlasKey},
		UserFile("atlas", "jkragt"): {atlasKey, jkragtAt},
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
	const gone = "  - path_regex: ^host-users/atlas-gone\\.yaml$\n    key_groups:\n      - age:\n          - *host_atlas\n          - *user_gone_atlas\n"
	const user = "  - path_regex: ^host-users/atlas-jkragt\\.yaml$\n    key_groups:\n      - age:\n          - *host_atlas\n          - *user_jkragt\n"
	c, err := ParseConfig([]byte("keys:\n  - &host_atlas age1atlas\n  - &user_jkragt age1old\n  - &user_gone_atlas age1gone\ncreation_rules:\n" + gone + user + gone + user))
	if err != nil {
		t.Fatal(err)
	}
	if removed := apply(t, c, atlas); !slices.Equal(removed, []string{"gone"}) {
		t.Errorf("removed = %q, want [gone]", removed)
	}
	again, err := ParseConfig(marshalConfig(t, c))
	if err != nil {
		t.Fatal(err)
	}
	var regexes []string
	for _, r := range again.list("creation_rules") {
		regexes = append(regexes, value(r, "path_regex").Value)
		if aliases, _ := ageKeys(r); slices.Contains(aliases, "user_jkragt") {
			t.Errorf("%s still lists *user_jkragt", value(r, "path_regex").Value)
		}
	}
	if want := []string{UserRule("atlas", "jkragt"), UserRule("atlas", "jkragt"), HostRule("atlas")}; !slices.Equal(regexes, want) {
		t.Errorf("rules = %q, want %q", regexes, want)
	}
}

// An anchor of a removed user that something besides a creation rule still
// refers to stays, so the file keeps parsing.
func TestAnchorAliasedOutsideTheRulesStays(t *testing.T) {
	const gone = "  - path_regex: ^host-users/atlas-gone\\.yaml$\n    key_groups:\n      - age:\n          - *host_atlas\n          - *user_gone_atlas\n"
	c, err := ParseConfig([]byte("keys:\n  - &host_atlas age1atlas\n  - &user_gone_atlas age1gone\ncreation_rules:\n" + gone +
		"destination_rules:\n  - s3_bucket: backup\n    key_groups:\n      - age:\n          - *user_gone_atlas\n"))
	if err != nil {
		t.Fatal(err)
	}
	apply(t, c, Host{Name: "atlas", Recipient: atlasKey})
	if _, ok := c.Key("user_gone_atlas"); !ok {
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
		"keys:\n  hosts:\n    - &host_atlas age1atlas\ncreation_rules: []\n",
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
	const data = "keys:\n  - &host_atlas age1atlas\n"
	c, err := ParseConfig([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	c.Recipients(HostFile("atlas"))
	c.Shadowed([]string{HostFile("atlas")})
	c.HasRule(HostFile("atlas"))
	c.Replacements(atlas)
	if got := string(marshalConfig(t, c)); got != data {
		t.Errorf("after reads:\n%s\nwant:\n%s", got, data)
	}
}

func TestEmptyConfig(t *testing.T) {
	c, err := ParseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, c, atlas)
	golden(t, "empty", marshalConfig(t, c))
}

func TestRecipientsOfAnUncoveredFile(t *testing.T) {
	if _, err := fixture(t).Recipients("hosts/nowhere.yaml"); err == nil || !strings.Contains(err.Error(), "no creation rule") {
		t.Errorf("err = %v", err)
	}
}

func TestFileRule(t *testing.T) {
	if got, want := HostRule("atlas-dev"), `^hosts/atlas-dev\.yaml$`; got != want {
		t.Errorf("HostRule = %s, want %s", got, want)
	}
	if got, want := UserRule("atlas", "jkragt"), `^host-users/atlas-jkragt\.yaml$`; got != want {
		t.Errorf("UserRule = %s, want %s", got, want)
	}
}

func TestNothingShadowedInTheFixture(t *testing.T) {
	files := []string{HostFile("atlas"), HostFile("workhorse"), UserFile("atlas", "jkragt"), UserFile("workhorse", "jkragt"), HostFile("broadway")}
	if shadowed, err := fixture(t).Shadowed(files); err != nil || len(shadowed) != 0 {
		t.Errorf("shadowed = %+v, %v, want none", shadowed, err)
	}
}

// sops uses the first rule that matches a file, so a broader rule before a
// file's own rule, or before where Apply adds it, would win.
func TestShadowed(t *testing.T) {
	const broad = "  - path_regex: ^hosts/.*\\.yaml$\n    key_groups:\n      - age:\n          - *admin\n"
	const noRegex = "  - key_groups:\n      - age:\n          - *admin\n"
	const own = "  - path_regex: ^hosts/atlas\\.yaml$\n    key_groups:\n      - age:\n          - *host_atlas\n"
	const other = "  - path_regex: ^hosts/workhorse\\.yaml$\n    key_groups:\n      - age:\n          - *admin\n"
	files := []string{HostFile("atlas"), UserFile("atlas", "jkragt"), HostFile("atlas")}
	tests := []struct {
		name, rules string
		want        []Shadow
	}{
		{"broad rule before the own one", broad + own, []Shadow{{HostFile("atlas"), `path_regex ^hosts/.*\.yaml$`}}},
		{"broad rule and no own one", broad, []Shadow{{HostFile("atlas"), `path_regex ^hosts/.*\.yaml$`}}},
		{"rule without path_regex", noRegex + own, []Shadow{
			{HostFile("atlas"), "creation rule 1, which has no path_regex"},
			{UserFile("atlas", "jkragt"), "creation rule 1, which has no path_regex"},
		}},
		{"own rule first", own + broad + noRegex, []Shadow{{UserFile("atlas", "jkragt"), "creation rule 3, which has no path_regex"}}},
		{"another host's rule only", other, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseConfig([]byte("keys:\n  - &admin age1admin\n  - &host_atlas age1old\ncreation_rules:\n" + tt.rules))
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
	if _, err := c.Shadowed([]string{HostFile("atlas")}); err == nil || !strings.Contains(err.Error(), "path_regex") {
		t.Errorf("err = %v, want the broken path_regex named", err)
	}
}

// sops also encrypts to a recipient written literally into a rule, so it stays
// and counts, also when the rule's aliases change.
func TestLiteralRecipientsInARuleStay(t *testing.T) {
	c, err := ParseConfig([]byte("keys:\n  - &host_atlas age1atlas\ncreation_rules:\n" +
		"  - path_regex: ^hosts/atlas\\.yaml$\n    key_groups:\n      - age:\n          - *host_atlas\n          - age1admin\n" +
		"  - path_regex: ^host-users/atlas-jkragt\\.yaml$\n    key_groups:\n      - age:\n          - age1admin\n          - *host_atlas\n"))
	if err != nil {
		t.Fatal(err)
	}
	apply(t, c, atlas)
	again, err := ParseConfig(marshalConfig(t, c))
	if err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string][]string{
		HostFile("atlas"):           {atlasKey, "age1admin"},
		UserFile("atlas", "jkragt"): {atlasKey, jkragtAt, "age1admin"},
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
	const unescaped = "  - path_regex: ^hosts/atlas.yaml$\n    key_groups:\n      - age:\n          - *admin\n"
	const userGlob = "  - path_regex: ^host-users/atlas-.*\\.yaml$\n    key_groups:\n      - age:\n          - *admin\n"
	const own = "  - path_regex: ^hosts/atlas\\.yaml$\n    key_groups:\n      - age:\n          - *admin\n"
	const noRegex = "  - key_groups:\n      - age:\n          - *admin\n"
	h := Host{Name: "atlas", Recipient: "age1atlas", Users: map[string]string{"jkragt": "age1jkragt"}}
	tests := []struct {
		name, rules string
		want        []Inserted
	}{
		{"differently written rule", unescaped, []Inserted{{HostFile("atlas"), "^hosts/atlas.yaml$"}}},
		{"rule for every user file", userGlob, []Inserted{{UserFile("atlas", "jkragt"), `^host-users/atlas-.*\.yaml$`}}},
		{"own rule", own, nil},
		{"catch-all", noRegex, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseConfig([]byte(keys + tt.rules))
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Insertions(h)
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
	if _, err := c.Insertions(Host{Name: "atlas", Recipient: "age1atlas"}); err == nil || !strings.Contains(err.Error(), "path_regex") {
		t.Errorf("err = %v, want the broken path_regex named", err)
	}
}
