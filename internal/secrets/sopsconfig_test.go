package secrets

import (
	"bytes"
	"flag"
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

// apply is c.Apply(h), which must succeed.
func apply(t *testing.T, c *Config, h Host) []string {
	t.Helper()
	removed, err := c.Apply(h)
	if err != nil {
		t.Fatal(err)
	}
	return removed
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

func TestNewRuleGoesBeforeACatchAll(t *testing.T) {
	c, err := ParseConfig([]byte("keys:\n  - &everyone age1all\ncreation_rules:\n  - key_groups:\n      - age:\n          - *everyone\n"))
	if err != nil {
		t.Fatal(err)
	}
	apply(t, c, Host{Name: "atlas", Recipient: atlasKey})
	if got, err := c.Recipients(HostFile("atlas")); err != nil || !slices.Equal(got, []string{atlasKey}) {
		t.Errorf("recipients = %q, %v; the catch-all shadows the new rule", got, err)
	}
	out := string(marshalConfig(t, c))
	if strings.Index(out, "path_regex") > strings.Index(out, "*everyone") {
		t.Errorf("new rule after the catch-all:\n%s", out)
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

// sops uses the first rule that matches a file, so the host's rule must come
// before any broader rule, wherever it was.
func TestHostRuleGoesBeforeABroaderRule(t *testing.T) {
	const broad = "  - path_regex: ^hosts/.*\\.yaml$\n    key_groups:\n      - age:\n          - *admin\n"
	const noRegex = "  - key_groups:\n      - age:\n          - *admin\n"
	const own = "  - path_regex: ^hosts/atlas\\.yaml$\n    key_groups:\n      - age:\n          - *host_atlas\n"
	for name, rules := range map[string]string{
		"anchored broad rule":        broad,
		"rule without path_regex":    noRegex,
		"own rule after a broad one": broad + own,
	} {
		t.Run(name, func(t *testing.T) {
			c, err := ParseConfig([]byte("keys:\n  - &admin age1admin\n  - &host_atlas age1old\ncreation_rules:\n" + rules))
			if err != nil {
				t.Fatal(err)
			}
			apply(t, c, atlas)
			for file, want := range map[string][]string{
				HostFile("atlas"):           {atlasKey},
				UserFile("atlas", "jkragt"): {atlasKey, jkragtAt},
			} {
				if got, err := c.Recipients(file); err != nil || !slices.Equal(got, want) {
					t.Errorf("%s: recipients = %q, %v, want %q", file, got, err, want)
				}
			}
			again, err := ParseConfig(marshalConfig(t, c))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := again.Recipients(HostFile("atlas")); err != nil || !slices.Equal(got, []string{atlasKey}) {
				t.Errorf("after a round trip: recipients = %q, %v", got, err)
			}
		})
	}
}

func TestApplyReportsABrokenRegex(t *testing.T) {
	c, err := ParseConfig([]byte("creation_rules:\n  - path_regex: ^hosts/(\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Apply(atlas); err == nil || !strings.Contains(err.Error(), "path_regex") {
		t.Errorf("err = %v, want the broken path_regex named", err)
	}
}
