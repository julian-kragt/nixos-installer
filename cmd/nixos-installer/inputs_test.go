package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// newConfig creates a config flake directory and puts a `nix` on PATH that
// reports the given hosts as installable.
func newConfig(t *testing.T, hosts ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := json.Marshal(append([]string{}, hosts...))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' '" + string(list) + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "nix"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return dir
}

// newSecrets creates a nix-secrets directory.
func newSecrets(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".sops.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
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

func TestAskConfigDir(t *testing.T) {
	dir := newConfig(t)
	ctx, _ := operator("")
	if got, err := askConfigDir(ctx, dir); err != nil || got != dir {
		t.Errorf("given config: got %q, %v", got, err)
	}
	if _, err := askConfigDir(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "no flake.nix") {
		t.Errorf("given config without flake.nix: err = %v", err)
	}
	if _, err := askConfigDir(ctx, ""); err == nil || !strings.Contains(err.Error(), "--config") {
		t.Errorf("EOF: err = %v, want a hint at --config", err)
	}
	ctx, out := operator(t.TempDir() + "\n" + dir + "\n")
	if got, err := askConfigDir(ctx, ""); err != nil || got != dir {
		t.Errorf("asked: got %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "no flake.nix") {
		t.Errorf("bad answer not explained: %q", out.String())
	}
}

func TestAskSecretsDir(t *testing.T) {
	dir := newSecrets(t)
	ctx, _ := operator("")
	if got, err := askSecretsDir(ctx, dir); err != nil || got != dir {
		t.Errorf("given secrets: got %q, %v", got, err)
	}
	if _, err := askSecretsDir(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "no .sops.yaml") {
		t.Errorf("given secrets without .sops.yaml: err = %v", err)
	}
	if _, err := askSecretsDir(ctx, ""); err == nil || !strings.Contains(err.Error(), "--secrets") {
		t.Errorf("EOF: err = %v, want a hint at --secrets", err)
	}
	ctx, out := operator(t.TempDir() + "\n" + dir + "\n")
	if got, err := askSecretsDir(ctx, ""); err != nil || got != dir {
		t.Errorf("asked: got %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "no .sops.yaml") {
		t.Errorf("bad answer not explained: %q", out.String())
	}
}

func TestListHostsWithoutNix(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	ctx, _ := operator("")
	if _, err := listHosts(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "nix not found") {
		t.Errorf("err = %v", err)
	}
}

func TestListAndPickHost(t *testing.T) {
	dir := newConfig(t, "atlas", "broadway")
	ctx, _ := operator("2\n")
	hosts, err := listHosts(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hosts, []string{"atlas", "broadway"}) {
		t.Fatalf("got %v", hosts)
	}

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

func TestListHostsWithoutHosts(t *testing.T) {
	dir := newConfig(t)
	ctx, _ := operator("")
	if _, err := listHosts(ctx, dir); err == nil || !strings.Contains(err.Error(), "no installable hosts") {
		t.Errorf("err = %v", err)
	}
}

func TestResolveInputsCompletesInputsAndLoadsState(t *testing.T) {
	stateDir := t.TempDir()
	saved, err := pipeline.LoadState(stateDir, "broadway")
	if err != nil {
		t.Fatal(err)
	}
	saved.Completed = []string{"0.1"}
	if err := saved.Save(); err != nil {
		t.Fatal(err)
	}

	secretsDir := newSecrets(t)
	ctx, out := operator(secretsDir + "\n2\n192.168.1.100\n") // secrets, host, target
	in := pipeline.Inputs{ConfigDir: newConfig(t, "atlas", "broadway")}
	st, err := resolveInputs(ctx, &in, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Prepare the target") {
		t.Errorf("instructions shown to a host with a completed stage:\n%s", out.String())
	}
	if in.Target != "192.168.1.100" || in.Host != "broadway" || in.SecretsDir != secretsDir {
		t.Errorf("inputs: %+v", in)
	}
	if st.Host != "broadway" || len(st.Completed) != 1 {
		t.Errorf("state: %+v", st)
	}
}

func TestResolveInputsShowsInstructionsToANewHost(t *testing.T) {
	ctx, out := operator("\n192.168.1.100\n") // Enter, target
	in := pipeline.Inputs{ConfigDir: newConfig(t, "atlas"), SecretsDir: newSecrets(t)}
	if _, err := resolveInputs(ctx, &in, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if in.Target != "192.168.1.100" {
		t.Errorf("target = %q", in.Target)
	}
	if !strings.Contains(out.String(), "Prepare the target") {
		t.Errorf("no instructions for a new host:\n%s", out.String())
	}
}

func TestResolveInputsFromFlagsAsksNothing(t *testing.T) {
	ctx, _ := operator("")
	in := pipeline.Inputs{ConfigDir: newConfig(t, "atlas"), SecretsDir: newSecrets(t), Host: "atlas", Target: "192.168.1.100"}
	st, err := resolveInputs(ctx, &in, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if st.Host != "atlas" || len(st.Completed) != 0 {
		t.Errorf("state: %+v", st)
	}
}

func TestResolveInputsReinstallStartsOver(t *testing.T) {
	stateDir := t.TempDir()
	saved, err := pipeline.LoadState(stateDir, "atlas")
	if err != nil {
		t.Fatal(err)
	}
	saved.Completed = []string{"0.1", "0.2"}
	if err := saved.Save(); err != nil {
		t.Fatal(err)
	}
	pin := filepath.Join(stateDir, "atlas.known_hosts")
	if err := os.WriteFile(pin, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, _ := operator("")
	in := pipeline.Inputs{ConfigDir: newConfig(t, "atlas"), SecretsDir: newSecrets(t), Host: "atlas", Target: "192.168.1.100", Reinstall: true}
	st, err := resolveInputs(ctx, &in, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Completed) != 0 {
		t.Errorf("state: %+v", st)
	}
	if _, err := os.Stat(pin); !os.IsNotExist(err) {
		t.Errorf("pin not removed: %v", err)
	}
}
