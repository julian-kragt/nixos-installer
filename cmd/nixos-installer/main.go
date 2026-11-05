package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"installer/internal/pipeline"
	"installer/internal/runner"
	"installer/internal/stages"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	context.AfterFunc(ctx, stop) // restore default handling so a second Ctrl+C force-quits

	flags, err := parseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	stateDir := defaultStateDir()
	runnerOpts := runner.Options{Out: os.Stderr, In: os.Stdin, Verbose: flags.verbose}
	logPath, logFile, err := openLogFile(stateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: no log file: %v\n", err)
	} else {
		defer logFile.Close()
		runnerOpts.File = logFile
	}

	ui := runner.New(runnerOpts)
	err = install(runner.NewContext(ctx, ui), flags.Inputs, stateDir)
	ui.Close()
	if err != nil {
		// The runner already showed err as a failed task.
		if logFile != nil {
			fmt.Fprintf(os.Stderr, "Full log: %s\n", logPath)
		}
		return 1
	}
	return 0
}

// install completes the inputs and runs the pipeline for the chosen host.
func install(ctx context.Context, in pipeline.Inputs, stateDir string) error {
	st, err := resolveInputs(ctx, &in, stateDir)
	if err != nil {
		return err
	}
	return pipeline.Run(ctx, pipeline.NewEnv(in, st), stages.All)
}

// defaultStateDir returns ${XDG_STATE_HOME:-~/.local/state}/nixos-installer.
func defaultStateDir() string {
	const app = "nixos-installer"
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) { // unset, or relative, which the XDG spec says to ignore
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", ".state", app)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, app)
}

// openLogFile creates <stateDir>/logs/install-<time>.log.
func openLogFile(stateDir string) (string, *os.File, error) {
	dir := filepath.Join(stateDir, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("install-%s.log", time.Now().Format("20060102-150405")))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return "", nil, err
	}
	return path, f, nil
}
