// Command nixos-installer-console runs on the target, on a virtual terminal of
// its own, and asks the operator there for what the provider must not see:
// the LUKS passphrase and the values of the host's secret files. The installer
// copies it to the target and starts it (internal/console).
//
//	nixos-installer-console check
//	nixos-installer-console luks <host>
//	nixos-installer-console secrets <host> <dir> <path>=<template>[:<user>]...
//
// luks writes the passphrase to /tmp/disko-password. secrets writes each file
// in the repository at dir, encrypted with sops to the recipients of dir's
// .sops.yaml. It exits non-zero when anything is left undone.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/julian-kragt/nixos-installer/internal/authoring"
	"github.com/julian-kragt/nixos-installer/internal/commands/luks"
	"github.com/julian-kragt/nixos-installer/internal/console"
	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/runner"
)

// readTime is how long a failure stays on screen before the console switches back.
const readTime = 30 * time.Second

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 1 && args[0] == "check" {
		fmt.Println("ok")
		return 0
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: nixos-installer-console check | luks <host> | secrets <host> <dir> <path>=<template>[:<user>]...")
		return 2
	}

	// systemctl stop, when the installer is stopped, sends SIGTERM; a hangup of
	// the terminal SIGHUP.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	restore, err := console.Foreground(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	ui := runner.New(runner.Options{Out: os.Stdout, In: os.Stdin})
	ctx = runner.NewContext(ctx, ui)
	err = dispatch(ctx, args[0], args[1], args[2:])
	if err != nil && ctx.Err() == nil {
		// Let the operator read why before the console switches back.
		wait, cancel := context.WithTimeout(ctx, readTime)
		_, _ = runner.Ask(wait, "Press Enter to return to the installer... ")
		cancel()
	}
	ui.Close()
	restore()
	if err != nil {
		return 1
	}
	return 0
}

// dispatch runs the command what for host.
func dispatch(ctx context.Context, what, host string, args []string) error {
	runner.Info(ctx, "nixos-installer is installing "+host+". What you type here stays on this machine.")
	switch what {
	case "luks":
		return runner.Run(ctx, "LUKS passphrase for "+host, luks.Type)
	case "secrets":
		if len(args) < 2 {
			return errors.New("secrets needs a directory and at least one file")
		}
		dir := args[0]
		for _, arg := range args[1:] {
			f, err := authoring.ParseArg(arg)
			if err != nil {
				return err
			}
			err = runner.Run(ctx, "Secrets in "+f.Path, func(ctx context.Context) error {
				return authoring.Author(ctx, machine.Local(), dir, host, f)
			})
			if err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("unknown command %q", what)
}
