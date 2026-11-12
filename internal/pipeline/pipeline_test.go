package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"installer/internal/runner"
)

// fake is a stage that records that it ran.
func fake(id string, ran *[]string) Stage {
	return Stage{ID: id, Name: "stage " + id, Run: func(context.Context, *Env) error {
		*ran = append(*ran, id)
		return nil
	}}
}

// run runs the stages for host "h" with the state kept in dir and returns
// what the runner printed.
func run(t *testing.T, dir string, stages ...Stage) (*State, string, error) {
	t.Helper()
	st, err := LoadState(dir, "h")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ctx := runner.NewContext(context.Background(), runner.New(runner.Options{Out: &out}))
	err = Run(ctx, &Env{State: st}, stages)
	return st, out.String(), err
}

func TestRunsStagesInListOrderAndRecordsThem(t *testing.T) {
	var ran []string
	dir := t.TempDir()
	st, _, err := run(t, dir, fake("0.2", &ran), fake("0.10", &ran), fake("0.3", &ran))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ran, ","); got != "0.2,0.10,0.3" {
		t.Fatalf("ran %s, want the order of the list", got)
	}
	if got := strings.Join(st.Completed, ","); got != "0.2,0.10,0.3" {
		t.Fatalf("completed %s", got)
	}
	saved, err := LoadState(dir, "h")
	if err != nil || strings.Join(saved.Completed, ",") != "0.2,0.10,0.3" {
		t.Fatalf("saved state: %+v, %v", saved, err)
	}
}

func TestResumesBehindCompletedStages(t *testing.T) {
	var ran []string
	dir := t.TempDir()
	if _, _, err := run(t, dir, fake("0.1", &ran)); err != nil {
		t.Fatal(err)
	}

	ran = nil
	_, out, err := run(t, dir, fake("0.1", &ran), fake("0.2", &ran), fake("0.3", &ran))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ran, ","); got != "0.2,0.3" {
		t.Fatalf("ran %s, want 0.2,0.3", got)
	}
	if !strings.Contains(out, "0.1 stage 0.1 (done earlier)") {
		t.Errorf("skipped stage not reported:\n%s", out)
	}
}

func TestAlwaysStageRerunsAndIsNotRecorded(t *testing.T) {
	var ran []string
	env := fake("0.0", &ran)
	env.Always = true
	dir := t.TempDir()
	for range 2 {
		st, _, err := run(t, dir, env, fake("0.1", &ran))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(st.Completed, ","); got != "0.1" {
			t.Fatalf("completed %s, want 0.1", got)
		}
	}
	if got := strings.Join(ran, ","); got != "0.0,0.1,0.0" {
		t.Fatalf("ran %s, want 0.0,0.1,0.0", got)
	}
}

func TestFailedStageStopsThePipelineAndIsNotRecorded(t *testing.T) {
	var ran []string
	bad := Stage{ID: "0.2", Name: "bad", Run: func(context.Context, *Env) error { return errors.New("boom") }}
	st, out, err := run(t, t.TempDir(), fake("0.1", &ran), bad, fake("0.3", &ran))
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
	if got := strings.Join(ran, ","); got != "0.1" {
		t.Fatalf("ran %s, want 0.1", got)
	}
	if got := strings.Join(st.Completed, ","); got != "0.1" {
		t.Fatalf("completed %s, want 0.1", got)
	}
	if !strings.Contains(out, "✗ 0.2 bad") || !strings.Contains(out, "\n  boom\n") {
		t.Errorf("failure not shown:\n%s", out)
	}
}

func TestSaveFailureFailsTheStage(t *testing.T) {
	var ran []string
	dir := filepath.Join(t.TempDir(), "state")
	st, err := LoadState(dir, "h")
	if err != nil {
		t.Fatal(err)
	}
	// A regular file where the state directory should be makes Save fail.
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ctx := runner.NewContext(context.Background(), runner.New(runner.Options{Out: &out}))
	err = Run(ctx, &Env{State: st}, []Stage{fake("1.0", &ran), fake("1.1", &ran)})
	if err == nil || !strings.Contains(err.Error(), "persist state") {
		t.Fatalf("err = %v, want a persist error", err)
	}
	if got := strings.Join(ran, ","); got != "1.0" {
		t.Fatalf("ran %s, want the pipeline to stop behind 1.0", got)
	}
	if !strings.Contains(out.String(), "✗ 1.0 stage 1.0") || !strings.Contains(out.String(), "\n  persist state") {
		t.Errorf("save failure not shown to the operator:\n%s", out.String())
	}
}

func TestNewEnvPinsNextToTheState(t *testing.T) {
	dir := t.TempDir()
	st, err := LoadState(dir, "atlas")
	if err != nil {
		t.Fatal(err)
	}
	env := NewEnv(Inputs{Target: "192.0.2.1"}, st)
	if env.Pin.File != filepath.Join(dir, "atlas.known_hosts") || env.Pin.Host != "atlas" {
		t.Errorf("pin = %+v", env.Pin)
	}
}

func TestOutdatedStageRunsAgainWithEveryStageAfterIt(t *testing.T) {
	var ran []string
	dir := t.TempDir()
	stale := ""
	check := fake("0.2", &ran)
	check.Outdated = func(context.Context, *Env) (string, error) { return stale, nil }
	stages := []Stage{fake("0.1", &ran), check, fake("0.3", &ran)}
	if _, _, err := run(t, dir, stages...); err != nil {
		t.Fatal(err)
	}

	ran = nil
	if _, _, err := run(t, dir, stages...); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Fatalf("ran %s, want nothing while 0.2 is up to date", strings.Join(ran, ","))
	}

	ran, stale = nil, "users changed"
	st, out, err := run(t, dir, stages...)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ran, ","); got != "0.2,0.3" {
		t.Fatalf("ran %s, want 0.2,0.3", got)
	}
	if got := strings.Join(st.Completed, ","); got != "0.1,0.2,0.3" {
		t.Fatalf("completed %s, want each stage once", got)
	}
	if !strings.Contains(out, "0.2 stage 0.2 must run again: users changed") {
		t.Errorf("reason not shown:\n%s", out)
	}
}

func TestOutdatedStageStaysOpenWhenALaterStageFails(t *testing.T) {
	var ran []string
	dir := t.TempDir()
	check := fake("0.1", &ran)
	stages := []Stage{check, fake("0.2", &ran)}
	if _, _, err := run(t, dir, stages...); err != nil {
		t.Fatal(err)
	}

	check.Outdated = func(context.Context, *Env) (string, error) { return "changed", nil }
	bad := Stage{ID: "0.2", Name: "bad", Run: func(context.Context, *Env) error { return errors.New("boom") }}
	if _, _, err := run(t, dir, check, bad); err == nil {
		t.Fatal("want the failure of 0.2")
	}
	saved, err := LoadState(dir, "h")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(saved.Completed, ","); got != "0.1" {
		t.Fatalf("saved completed %s, want 0.1: 0.2 must run again next time", got)
	}
}

// A stage that no longer exists, as 4.1 after it became 2.5, is dropped from
// Completed instead of staying there for good.
func TestDropsCompletedIDsThatAreNoStage(t *testing.T) {
	var ran []string
	dir := t.TempDir()
	st, err := LoadState(dir, "h")
	if err != nil {
		t.Fatal(err)
	}
	st.Completed = []string{"0.1", "4.1"}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	st, _, err = run(t, dir, fake("0.1", &ran), fake("0.2", &ran))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(st.Completed, ","); got != "0.1,0.2" {
		t.Errorf("completed %s, want 0.1,0.2", got)
	}
}
