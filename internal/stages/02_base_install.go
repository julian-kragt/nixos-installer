package stages

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"installer/internal/git"
	"installer/internal/nix"
	"installer/internal/pipeline"
	"installer/internal/runner"
)

// nixosAnywhereRef is the nixos-anywhere the base install runs.
const nixosAnywhereRef = "github:nix-community/nixos-anywhere/1.13.0"

// sshWaitTimeout is how long the first boot may take, including the operator
// typing the LUKS passphrase at the console.
const sshWaitTimeout = 15 * time.Minute

// osReleasePath and hostKeyPollInterval are variables so tests can change them.
var (
	osReleasePath       = "/etc/os-release"
	hostKeyPollInterval = 5 * time.Second
)

// baseInstall installs the host's bootstrap system on the target, waits for it
// to come up and pins its host key. A target that already runs the system skips
// the install, so a run interrupted after it picks up from there.
func baseInstall(ctx context.Context, env *pipeline.Env) error {
	var info nix.HostInfo
	var kind targetKind
	err := runner.RunSteps(ctx,
		runner.Step{Title: "Read host data", Fn: func(ctx context.Context) (err error) {
			info, err = nix.EvalHostInfo(ctx, env.Local, env.ConfigDir, env.Host)
			return err
		}},
		runner.Step{Title: "Check what the target runs", Fn: func(ctx context.Context) (err error) {
			kind, err = inspectTarget(ctx, env.Remote, env.Host)
			return err
		}},
	)
	if err != nil {
		return err
	}

	switch kind {
	case targetInstalled:
		runner.Info(ctx, fmt.Sprintf("The target already runs %s; boot the NixOS ISO to install it again", env.Host))
	case targetISO:
		if err := installFromISO(ctx, env, info.HardwareConfigPath); err != nil {
			return err
		}
	default:
		// nixos-anywhere would kexec, which empties /tmp and so loses the passphrase.
		return fmt.Errorf("the target runs neither the NixOS ISO nor %s; boot it from the NixOS ISO", env.Host)
	}

	err = runner.Run(ctx, "Commit hardware config", func(ctx context.Context) error {
		message := fmt.Sprintf("chore(%s): update generated hardware config", env.Host)
		_, err := git.Commit(ctx, env.Local, env.ConfigDir, message, info.HardwareConfigPath)
		return err
	})
	if err != nil {
		return err
	}

	pinned, err := env.Pin.Load()
	if err != nil {
		return err
	}
	if pinned == nil {
		return errors.New("no host key pinned for " + env.Host)
	}
	env.State.HostKeyFingerprint = pipeline.Fingerprint(pinned)
	return nil
}

// installFromISO runs nixos-anywhere on the ISO and waits for the first boot.
func installFromISO(ctx context.Context, env *pipeline.Env, hardwareConfigPath string) error {
	return runner.RunSteps(ctx,
		runner.Step{Title: "Check LUKS passphrase", Fn: func(ctx context.Context) error {
			return ensurePassphrase(ctx, env.Remote)
		}},
		runner.Step{Title: "Run nixos-anywhere", Fn: func(ctx context.Context) error {
			return runNixosAnywhere(ctx, env, hardwareConfigPath)
		}},
		runner.Step{Title: "Wait for the installed system", Fn: func(ctx context.Context) error {
			return awaitInstalledSystem(ctx, env)
		}},
	)
}

// targetKind is what a target is running.
type targetKind int

const (
	targetOther     targetKind = iota
	targetISO                  // the NixOS installer: nixos-anywhere does not kexec it
	targetInstalled            // NixOS under the host's name
)

// inspectTarget reads what target runs. It goes over the pinned connection, so
// it is also what checks the pin right before nixos-anywhere, which cannot be
// given one: its own StrictHostKeyChecking=no comes first and ssh takes the
// first value of an option.
func inspectTarget(ctx context.Context, target pipeline.Machine, host string) (targetKind, error) {
	hostname, err := target.Run(ctx, "hostname")
	if err != nil {
		return targetOther, err
	}
	osRelease, err := target.Run(ctx, "cat", osReleasePath)
	if err != nil {
		return targetOther, err
	}
	return classifyTarget(strings.TrimSpace(hostname), osRelease, host), nil
}

// classifyTarget decides from the hostname and /etc/os-release. The installer
// test is the one nixos-anywhere uses to skip its kexec. Testing for
// /run/current-system would not do: the ISO has that too.
func classifyTarget(hostname, osRelease, host string) targetKind {
	fields := map[string]string{}
	for _, line := range strings.Split(osRelease, "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			fields[key] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	switch {
	case fields["ID"] != "nixos":
		return targetOther
	case fields["VARIANT_ID"] == "installer":
		return targetISO
	case hostname == host:
		return targetInstalled
	}
	return targetOther
}

// ensurePassphrase asks for the LUKS passphrase again when it is not on target
// any more, as after a reboot of the ISO.
func ensurePassphrase(ctx context.Context, target pipeline.Machine) error {
	_, err := target.Run(ctx, "test", "-s", diskPasswordPath)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return askAndSendPassphrase(ctx, target)
	}
	return err
}

// runNixosAnywhere installs the host's bootstrap system from the config flake.
// It runs in the config's root, where a relative --generate-hardware-config path
// lands. All its output goes to stderr, which streams to the runner.
func runNixosAnywhere(ctx context.Context, env *pipeline.Env, hardwareConfigPath string) error {
	prefix, err := git.Prefix(ctx, env.Local, env.ConfigDir)
	if err != nil {
		return err
	}
	args := append([]string{"-c", `cd "$1" && shift && exec "$@" 1>&2`, "sh", env.ConfigDir, "nix"},
		nixosAnywhereArgs(configFlakeURL(env.ConfigDir, prefix), env.Host, env.Target, hardwareConfigPath)...)
	if _, err := env.Local.Run(ctx, "sh", args...); err != nil {
		return fmt.Errorf("nixos-anywhere: %w", err)
	}
	return nil
}

// nixosAnywhereArgs are the arguments of `nix` for the install. hardwareConfigPath
// is relative to the config's root.
func nixosAnywhereArgs(config, host, target, hardwareConfigPath string) []string {
	return []string{"run", nixosAnywhereRef, "--",
		"--flake", config + "#" + host + "-bootstrap",
		"--target-host", "root@" + target,
		"--build-on", "remote",
		"--generate-hardware-config", "nixos-generate-config", hardwareConfigPath,
	}
}

// configFlakeURL is the git+file reference of the config flake at dir, prefix
// being its path below the top of the repository (as from git.Prefix).
func configFlakeURL(dir, prefix string) string {
	if prefix == "" {
		return "git+file://" + dir
	}
	sub := strings.TrimSuffix(prefix, "/")
	return "git+file://" + strings.TrimSuffix(dir, "/"+sub) + "?dir=" + sub
}

// awaitInstalledSystem waits for the first boot, pins the new host key and
// checks that the system behind it is the host.
func awaitInstalledSystem(ctx context.Context, env *pipeline.Env) error {
	old, err := env.Pin.Load()
	if err != nil {
		return err
	}
	runner.Warn(ctx, fmt.Sprintf("Type the LUKS passphrase at the console of %s when it asks", env.Host))
	key, err := waitForNewHostKey(ctx, old, sshWaitTimeout, func(ctx context.Context) (ssh.PublicKey, error) {
		return pipeline.ScanHostKey(ctx, env.Target)
	})
	if err != nil {
		return err
	}
	if err := env.Pin.Save(key); err != nil {
		return err
	}
	runner.Info(ctx, "Pinned the installed system's host key: "+pipeline.Fingerprint(key))

	kind, err := inspectTarget(ctx, env.Remote, env.Host)
	if err != nil {
		return fmt.Errorf("log in to the installed system: %w", err)
	}
	if kind != targetInstalled {
		return fmt.Errorf("the system that came up at %s is not %s", env.Target, env.Host)
	}
	return nil
}

// waitForNewHostKey polls scan until it returns a key other than old, which is
// the ISO's: until then the machine has not rebooted, or is at the passphrase
// prompt with no ssh at all.
func waitForNewHostKey(ctx context.Context, old ssh.PublicKey, timeout time.Duration, scan func(context.Context) (ssh.PublicKey, error)) (ssh.PublicKey, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout,
		fmt.Errorf("the installed system did not come up within %s; run the installer again once it has", timeout))
	defer cancel()
	for {
		if key, err := scan(ctx); err == nil && !pipeline.SameKey(key, old) {
			return key, nil
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-time.After(hostKeyPollInterval):
		}
	}
}
