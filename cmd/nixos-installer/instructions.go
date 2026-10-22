package main

import (
	"context"
	"errors"
	"io"

	"installer/internal/runner"
)

const instructions = `Prepare the target machine first (it needs no operating system):

  1. Boot it from the standard NixOS installer (USB or netboot).
  2. On the machine, become root and set its password:  sudo -i, then passwd
  3. Note its IP address:  ip addr

Press Enter when the installer is running... `

// showInstructions tells the operator how to prepare the target and waits for
// Enter. Without an operator to ask (piped stdin) it carries on.
func showInstructions(ctx context.Context) error {
	_, err := runner.Ask(ctx, instructions)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
