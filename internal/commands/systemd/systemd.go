// Package systemd asks systemd on a machine about its units.
package systemd

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"installer/internal/machine"
)

// FailedUnits returns the names of the units on m that are failed.
func FailedUnits(ctx context.Context, m machine.Machine) ([]string, error) {
	out, err := m.Run(ctx, "systemctl", "--failed", "--no-legend", "--plain")
	if err != nil {
		return nil, err
	}
	var units []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			units = append(units, f[0])
		}
	}
	return units, nil
}

// IsFailed reports whether unit is failed on m. A unit that does not exist is not.
func IsFailed(ctx context.Context, m machine.Machine, unit string) (bool, error) {
	_, err := m.Run(ctx, "systemctl", "is-failed", "--quiet", unit)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && !machine.LostConnection(err):
		return false, nil
	}
	return false, err
}
