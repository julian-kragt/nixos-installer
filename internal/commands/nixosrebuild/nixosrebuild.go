// Package nixosrebuild switches a target to a system with nixos-rebuild, and
// follows the switch when the connection drops while it runs.
package nixosrebuild

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/julian-kragt/nixos-installer/internal/machine"
)

// switchUnit is the unit nixos-rebuild runs switch-to-configuration in. With
// systemd 261 or newer it watches the unit with `systemd-run --wait
// --output=cat`: the unit writes to the journal, so the switch goes on when the
// connection drops and only the watcher ends.
const switchUnit = "nixos-rebuild-switch-to-configuration.service"

// Switch builds the system flake points to on target and switches to it, with
// the build output streamed. Its fetches go over target's ssh, so the caller
// passes a Machine that forwards the agent when they need it.
func Switch(ctx context.Context, target machine.Machine, flake string) error {
	_, err := target.Run(ctx, "nixos-rebuild", "switch", "--flake", flake)
	return err
}

// pollInterval is a variable so tests can change it.
var pollInterval = 5 * time.Second

// AwaitSwitch waits until target answers and no switch runs there any more,
// after the connection dropped during Switch. Whether the switch got through
// is for the caller to check. A changed host key ends the wait at once.
func AwaitSwitch(ctx context.Context, target machine.Machine, timeout time.Duration) error {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout,
		fmt.Errorf("the target did not answer within %s; once it does, run the installer again", timeout))
	defer cancel()
	for {
		_, err := target.Run(ctx, "systemctl", "is-active", "--quiet", switchUnit)
		switch {
		case ctx.Err() != nil:
			return context.Cause(ctx) // the command was cut off, not answered
		case err != nil && strings.Contains(err.Error(), "Host key verification failed"):
			return fmt.Errorf("the host key changed during the rebuild: %w", err)
		case err != nil && !machine.LostConnection(err):
			return nil // the target answered: the unit is gone
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(pollInterval):
		}
	}
}

// currentSystem is the link to the system target runs, a variable for tests.
var currentSystem = "/run/current-system"

// CurrentSystem returns the store path of the system target runs.
func CurrentSystem(ctx context.Context, target machine.Machine) (string, error) {
	out, err := target.Run(ctx, "readlink", "-f", currentSystem)
	return strings.TrimSpace(out), err
}
