package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"installer/internal/pipeline"
	"installer/internal/runner"
)

// operator returns a context whose runner reads the operator's answers from
// input, as piped stdin would, and the buffer it prints to.
func operator(input string) (context.Context, *bytes.Buffer) {
	var out bytes.Buffer
	ui := runner.New(runner.Options{Out: &out, In: strings.NewReader(input)})
	return runner.NewContext(context.Background(), ui), &out
}

// newConfig creates a config flake directory with the given installable hosts.
func newConfig(t *testing.T, hosts ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, h := range hosts {
		p := filepath.Join(dir, "hosts", "nixos", h)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "bootstrap.nix"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAskTarget(t *testing.T) {
	ctx, out := operator("nope\n\n10.0.0.5\n")
	got, err := askTarget(ctx, "")
	if err != nil || got != "10.0.0.5" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !strings.Contains(out.String(), `("nope" is not a valid IP)`) {
		t.Errorf("bad answer not explained: %q", out.String())
	}

	ctx, out = operator("")
	if got, err := askTarget(ctx, "10.0.0.6"); err != nil || got != "10.0.0.6" || out.Len() != 0 {
		t.Errorf("a given target must not be asked for: %q, %v, printed %q", got, err, out.String())
	}
	if _, err := askTarget(ctx, ""); err == nil || !strings.Contains(err.Error(), "--target") {
		t.Errorf("EOF: err = %v, want a hint at --target", err)
	}
}

func TestListAndPickHost(t *testing.T) {
	dir := newConfig(t, "broadway", "atlas")
	if err := os.MkdirAll(filepath.Join(dir, "hosts", "nixos", "nobootstrap"), 0o755); err != nil {
		t.Fatal(err)
	}

	hosts, err := listHosts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hosts, []string{"atlas", "broadway"}) {
		t.Fatalf("got %v", hosts)
	}
	if _, err := listHosts(t.TempDir()); err == nil {
		t.Error("want an error for a directory that is not a config flake")
	}

	ctx, _ := operator("2\n")
	if got, err := pickHost(ctx, hosts, ""); err != nil || got != "broadway" {
		t.Fatalf("pick: %q %v", got, err)
	}
	none, out := operator("")
	if got, err := pickHost(none, hosts, "atlas"); err != nil || got != "atlas" {
		t.Fatalf("want: %q %v", got, err)
	}
	if got, err := pickHost(none, hosts[:1], ""); err != nil || got != "atlas" || out.Len() != 0 {
		t.Fatalf("a lone host is taken without asking: %q %v, printed %q", got, err, out.String())
	}
	if _, err := pickHost(none, hosts, "x"); err == nil {
		t.Fatal("expected unknown host error")
	}
}

func TestResolveInputsCompletesInputsAndLoadsState(t *testing.T) {
	stateDir := t.TempDir()
	saved, err := pipeline.LoadState(stateDir, "broadway")
	if err != nil {
		t.Fatal(err)
	}
	saved.Disk = "/dev/vda"
	if err := saved.Save(); err != nil {
		t.Fatal(err)
	}

	ctx, _ := operator("\n192.168.1.100\n2\n") // Enter, target, host
	in := pipeline.Inputs{ConfigDir: newConfig(t, "atlas", "broadway")}
	st, err := resolveInputs(ctx, &in, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if in.Target != "192.168.1.100" || in.Host != "broadway" {
		t.Errorf("inputs: %+v", in)
	}
	if st.Host != "broadway" || st.Disk != "/dev/vda" {
		t.Errorf("state: %+v", st)
	}
}

func TestResolveInputsFromFlagsAsksNothing(t *testing.T) {
	ctx, _ := operator("")
	in := pipeline.Inputs{ConfigDir: newConfig(t, "atlas"), Host: "atlas", Target: "192.168.1.100"}
	st, err := resolveInputs(ctx, &in, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if st.Host != "atlas" || len(st.Completed) != 0 {
		t.Errorf("state: %+v", st)
	}
}
