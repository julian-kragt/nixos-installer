// Package pipeline is the engine the stages run in: the state that survives a
// restart and the loop that runs the stages in order and resumes behind the last
// completed one.
package pipeline

import (
	"context"
	"fmt"
	"path/filepath"

	"installer/internal/machine"
	"installer/internal/runner"
)

// Inputs are the operator's choices for this run. They are complete before
// the first stage runs.
type Inputs struct {
	Host        string // host to install: an entry of the config flake's `bootstrapEntries` output
	Target      string // IP address of the target machine
	ConfigDir   string // absolute path to the config flake
	SSHPassword string // root SSH password of the NixOS ISO for initial key bootstrap; prompts when empty
	Reinstall   bool   // delete what is saved for Host before loading its state
	// GenerateHardware generates a missing hardware config without asking.
	GenerateHardware bool
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
// key pin lives next to the state file.
func NewEnv(in Inputs, st *State) *Env {
	pin := machine.NewHostPin(filepath.Dir(st.Path()), st.Host)
	return &Env{Inputs: in, State: st, Local: machine.Local(), Remote: machine.SSH(in.Target, pin), Pin: pin}
}

// Stage is one step of the pipeline.
type Stage struct {
	ID   string // as in the Pipeline doc: "0.0", "2.3"
	Name string
	// Always marks a stage that runs on every invocation, even when it ran
	// before (e.g. environment checks). It is not recorded in State.Completed.
	Always bool
	Run    func(ctx context.Context, env *Env) error
}

// Run runs the stages in the given order. A stage in State.Completed is
// skipped; every other stage is recorded there when it succeeds and the state
// is saved, so the next invocation resumes behind it.
func Run(ctx context.Context, env *Env, stages []Stage) error {
	runner.Info(ctx, "Starting pipeline")
	for _, s := range stages {
		title := s.ID + " " + s.Name
		if !s.Always && env.State.IsCompleted(s.ID) {
			runner.Info(ctx, title+" (done earlier)")
			continue
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
