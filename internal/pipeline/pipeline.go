// Package pipeline is the engine the stages run in: the state that survives a
// restart and the loop that runs the stages in order and resumes behind the last
// completed one.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/runner"
)

// Inputs are the operator's choices for this run. They are complete before
// the first stage runs.
type Inputs struct {
	Host        string // host to install: an entry of the config flake's `bootstrapEntries` output
	Target      string // IP address of the target machine
	ConfigDir   string // absolute path to the config flake
	SecretsDir  string // absolute path to the nix-secrets clone the secrets phase writes and pushes
	SSHPassword string // root SSH password of the NixOS ISO for initial key bootstrap; prompts when empty
	Reinstall   bool   // install Host again from the ISO: start from a fresh state, replace what is saved once the wipe is confirmed
	// GenerateHardware generates a missing hardware config without asking.
	GenerateHardware bool
	// RekeyVia is the address of an installed host by name, for the hosts
	// 2.3 encrypts shared secret files on. A host not in it is reached at its name.
	RekeyVia map[string]string
}

// Env is everything a stage needs besides its context. Parallel steps must
// not write State; they return results and the stage assigns them after the
// join.
type Env struct {
	Inputs
	State  *State
	Local  machine.Machine // the provider: the machine the installer runs on
	Remote machine.Machine // the target
	Pin    machine.HostPin // the host key Remote accepts
}

// NewEnv returns the Env for a run: in must be complete and st loaded. The host
// key pin lives next to the state file; a reinstall pins to its own file until
// ConfirmReinstall.
func NewEnv(in Inputs, st *State) *Env {
	pin := machine.NewHostPin(filepath.Dir(st.Path()), st.Host)
	if in.Reinstall {
		pin = machine.NewReinstallPin(filepath.Dir(st.Path()), st.Host)
	}
	return &Env{Inputs: in, State: st, Local: machine.Local(), Remote: machine.SSH(in.Target, pin), Pin: pin}
}

// ConfirmReinstall replaces what is saved for the host by the reinstall, once
// the operator confirmed the wipe: the state file and pin are deleted, and the
// pin of the reinstall takes the pin's place. It does nothing on another run.
func (e *Env) ConfirmReinstall() error {
	if !e.Reinstall {
		return nil
	}
	dir := filepath.Dir(e.State.Path())
	pin := machine.NewHostPin(dir, e.Host)
	// The rename replaces the old pin in one step, so a failure leaves
	// everything saved as it was.
	err := os.Rename(e.Pin.File, pin.File)
	switch {
	case errors.Is(err, os.ErrNotExist): // nothing pinned yet: forget the old pin too
		err = RemoveState(dir, e.Host)
	case err == nil:
		err = os.Remove(e.State.Path())
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	default:
		err = fmt.Errorf("state: move the pinned host key: %w", err)
	}
	if err != nil {
		return err
	}
	e.Pin, e.Remote, e.Reinstall = pin, machine.SSH(e.Target, pin), false
	return nil
}

// Stage is one step of the pipeline.
type Stage struct {
	ID   string // as in the Pipeline doc: "0.0", "2.3"
	Name string
	// Always marks a stage that runs on every invocation, even when it ran
	// before (e.g. environment checks). It is not recorded in State.Completed.
	Always bool
	Run    func(ctx context.Context, env *Env) error
	// Outdated, when set, says why a completed stage must run again because
	// what it recorded no longer holds, or "" when it need not.
	Outdated func(ctx context.Context, env *Env) (string, error)
}

// Run runs the stages in the given order. A stage in State.Completed is
// skipped unless it is outdated; every other stage is recorded there when it
// succeeds and the state is saved, so the next invocation resumes behind it.
// An outdated stage and every stage after it are no longer completed: they
// build on what it recorded. An ID in State.Completed that is no stage, as one
// of a stage that was removed, is dropped.
func Run(ctx context.Context, env *Env, stages []Stage) error {
	runner.Info(ctx, "Starting pipeline")
	ids := stageIDs(stages)
	env.State.Completed = slices.DeleteFunc(env.State.Completed, func(id string) bool { return !slices.Contains(ids, id) })
	for i, s := range stages {
		title := s.ID + " " + s.Name
		if !s.Always && env.State.IsCompleted(s.ID) {
			reason, err := outdated(ctx, env, s)
			if err != nil {
				return fmt.Errorf("%s: %w", title, err)
			}
			if reason == "" {
				runner.Info(ctx, title+" (done earlier)")
				continue
			}
			runner.Info(ctx, title+" must run again: "+reason)
			if err := env.State.Reopen(stageIDs(stages[i:])...); err != nil {
				return fmt.Errorf("persist state: %w", err)
			}
		}
		err := runner.Run(ctx, title, func(ctx context.Context) error {
			return runStage(ctx, env, s)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// outdated is s.Outdated, or "" for a stage without it.
func outdated(ctx context.Context, env *Env, s Stage) (string, error) {
	if s.Outdated == nil {
		return "", nil
	}
	return s.Outdated(ctx, env)
}

func stageIDs(stages []Stage) []string {
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID
	}
	return ids
}

// runStage runs s and records it as completed.
func runStage(ctx context.Context, env *Env, s Stage) error {
	if err := s.Run(ctx, env); err != nil {
		return err
	}
	if s.Always {
		return nil // re-run every time, never recorded
	}
	env.State.Completed = append(env.State.Completed, s.ID)
	if err := env.State.Save(); err != nil {
		return fmt.Errorf("persist state: %w", err)
	}
	return nil
}
