// Package console lets the operator type secrets at the target's own console,
// so they never pass through the provider. The provider copies a small helper,
// nixos-installer-console, to the target and starts it on a virtual terminal
// of its own; the operator answers there, and the provider only ever reads
// back what the helper leaves behind: ciphertext, or nothing at all.
package console

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"

	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/runner"
)

// HelperName is the helper's binary, built from cmd/nixos-installer-console.
const HelperName = "nixos-installer-console"

// EnvDir names the environment variable that holds the directory with a
// helper per target system, <dir>/<system>/nixos-installer-console. The
// package's wrapper sets it.
const EnvDir = "NIXOS_INSTALLER_CONSOLE"

// Dir is where everything the console needs lives on the target. /run is a
// tmpfs on the ISO and on the installed system alike, so nothing here reaches
// a disk, and all of it is gone after a reboot.
const Dir = "/run/nixos-installer"

// VT is the virtual terminal the helper runs on. logind starts a getty on
// demand only up to NAutoVTs (6), so nothing else claims it.
const VT = 9

// unit is the systemd unit the helper runs in.
const unit = "nixos-installer-console"

var (
	helperPath = path.Join(Dir, "bin", HelperName)
	tmpDir     = path.Join(Dir, "tmp")
)

// goArch maps the Nix systems the helper is built for to their GOARCH.
var goArch = map[string]string{
	"x86_64-linux":  "amd64",
	"aarch64-linux": "arm64",
}

// Session is the helper, installed on Target, for the host Host.
type Session struct {
	Target machine.Machine
	Host   string
}

// Prepare installs the helper for system on target and checks that it runs
// there. system is the host's Nix system, as installerData gives it.
func Prepare(ctx context.Context, local, target machine.Machine, host, system string) (Session, error) {
	bin, err := Helper(ctx, local, system)
	if err != nil {
		return Session{}, err
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		return Session{}, err
	}
	if err := WriteFile(ctx, target, helperPath, data, true); err != nil {
		return Session{}, fmt.Errorf("install %s on the target: %w", HelperName, err)
	}
	if out, err := target.Run(ctx, helperPath, "check"); err != nil || string(bytes.TrimSpace([]byte(out))) != "ok" {
		return Session{}, fmt.Errorf("%s (built for %s) does not run on the target: %w", HelperName, system, err)
	}
	if !target.HasCommand(ctx, "systemd-run") {
		return Session{}, errors.New("systemd-run is missing on the target")
	}
	return Session{Target: target, Host: host}, nil
}

// Helper returns the path, on local, of the helper built for system: from
// EnvDir when the package's wrapper set it, otherwise built with go from the
// installer's source tree, for a run with go run in the dev shell.
func Helper(ctx context.Context, local machine.Machine, system string) (string, error) {
	arch, ok := goArch[system]
	if !ok {
		return "", fmt.Errorf("no %s for %s; it is built for x86_64-linux and aarch64-linux", HelperName, system)
	}
	if dir := os.Getenv(EnvDir); dir != "" {
		bin := filepath.Join(dir, system, HelperName)
		if _, err := os.Stat(bin); err != nil {
			return "", fmt.Errorf("%s for %s: %w", HelperName, system, err)
		}
		return bin, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(cache, "nixos-installer", system, HelperName)
	_, err = local.Run(ctx, "env", "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0",
		"go", "build", "-trimpath", "-o", bin, "github.com/julian-kragt/nixos-installer/cmd/"+HelperName)
	if err != nil {
		return "", fmt.Errorf("build %s for %s (%s is not set, so the installer must run from its source tree): %w",
			HelperName, system, EnvDir, err)
	}
	return bin, nil
}

// writeFile writes stdin to $1 through a temporary file, so a reader never sees
// half of it, and makes it executable when $2 is set. The machine's umask (077)
// keeps it and its directory to root.
const writeFile = `mkdir -p "$(dirname "$1")" && cat > "$1.tmp" && { test -z "$2" || chmod 700 "$1.tmp"; } && mv -f "$1.tmp" "$1"`

// WriteFile writes data to file on target, executable when exec is set. data
// goes over stdin; it must not be a secret, as the provider holds it.
func WriteFile(ctx context.Context, target machine.Machine, file string, data []byte, exec bool) error {
	flag := ""
	if exec {
		flag = "x"
	}
	_, err := target.RunStdin(ctx, bytes.NewReader(data), "sh", "-c", writeFile, "sh", file, flag)
	return err
}

// ReadFile returns file on target, or nil when it does not exist.
func ReadFile(ctx context.Context, target machine.Machine, file string) ([]byte, error) {
	out, err := target.Run(ctx, "sh", "-c", `if [ -e "$1" ]; then cat "$1"; fi`, "sh", file)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return []byte(out), nil
}

// Remove deletes Dir, with the helper and anything it left, on target.
func Remove(ctx context.Context, target machine.Machine) error {
	_, err := target.Run(ctx, "rm", "-rf", Dir)
	return err
}

// start stops a helper a former run left behind, then runs the command $3… on
// the virtual terminal $1 as the unit $unit, with TMPDIR $2, and waits for it.
// systemd-run --wait exits with the helper's exit code. A transient unit gets
// systemd's environment, not this shell's, and on NixOS systemd's PATH holds
// none of the system's packages, so the helper gets this shell's PATH: the
// one 1.1 Readiness found vim, sops and mkpasswd in.
const start = `vt=$1 tmp=$2
shift 2
systemctl stop ` + unit + ` 2>/dev/null
systemctl reset-failed ` + unit + ` 2>/dev/null
mkdir -p "$tmp" &&
exec systemd-run --quiet --wait --collect --unit=` + unit + ` \
	-p TTYPath=/dev/tty"$vt" -p StandardInput=tty-force -p StandardOutput=tty -p StandardError=tty \
	-p TTYReset=yes -p TTYVHangup=yes -p TTYVTDisallocate=yes \
	-p Environment=TERM=linux -p Environment=TMPDIR="$tmp" -p Environment=PATH="$PATH" \
	-- "$@"`

// ErrAborted means the helper on the console did not finish: the operator gave
// up, or it failed. Its screen says why.
var ErrAborted = errors.New("not finished at the console")

// Run starts the helper with args on the target's console and waits until it
// exits; what tells the operator to go there, so it names what is asked
// there. The helper switches the console to its terminal and back. When ctx is
// cancelled, the helper is stopped, so no prompt stays open on the console.
func (s Session) Run(ctx context.Context, what string, args ...string) error {
	runner.Warn(ctx, fmt.Sprintf("Type %s at the console of %s (it switched to tty%d; Alt+F%d)", what, s.Host, VT, VT))
	defer func() {
		// Also after Ctrl+C: a cancelled ctx would fail at once.
		_, _ = s.Target.Quiet().Run(context.WithoutCancel(ctx), "systemctl", "stop", unit)
	}()
	argv := append([]string{"sh", "-c", start, "sh", fmt.Sprint(VT), tmpDir, helperPath}, args...)
	_, err := s.Target.Run(ctx, argv[0], argv[1:]...)
	var exit *exec.ExitError
	if err != nil && ctx.Err() == nil && !machine.LostConnection(err) && errors.As(err, &exit) {
		return fmt.Errorf("%s: %w (see the console of %s)", what, ErrAborted, s.Host)
	}
	return err
}
