package stages

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"installer/internal/git"
	"installer/internal/nix"
	"installer/internal/pipeline"
	"installer/internal/runner"
)

// diskPasswordPath is where the LUKS passphrase lives on the target; the
// config's disko.nix reads it. A variable so tests can point it elsewhere.
var diskPasswordPath = "/tmp/disko-password"

// providerPreparation makes sure everything is in place before the target is
// wiped, collects the host facts later stages need, gets the operator's
// confirmation and delivers the LUKS passphrase. It only reads from the target.
func providerPreparation(ctx context.Context, env *pipeline.Env) error {
	var disk string
	var info nix.HostInfo
	err := runner.RunSteps(ctx,
		runner.Step{Title: "Resolve install disk", Fn: func(ctx context.Context) (err error) {
			disk, err = resolveDisk(ctx, env)
			return err
		}},
		runner.Step{Title: "Read host data", Fn: func(ctx context.Context) (err error) {
			info, err = nix.EvalHostInfo(ctx, env.Local, env.ConfigDir, env.Host)
			return err
		}},
		runner.Step{Title: "Check config repository", Fn: func(ctx context.Context) error {
			return checkConfigRepo(ctx, env, info.HardwareConfigPath)
		}},
		runner.Step{Title: "Check SSH access to the installed system", Fn: func(ctx context.Context) error {
			return checkBootstrapKeys(ctx, env, info.RootAuthorizedKeys)
		}},
		runner.Step{Title: "Check bootstrap system", Fn: func(ctx context.Context) error {
			return nix.CheckBootstrapSystem(ctx, env.Local, env.ConfigDir, env.Host)
		}},
		runner.Step{Title: "Check config installs to the disk", Fn: func(ctx context.Context) error {
			devices, err := nix.EvalDisks(ctx, env.Local, env.ConfigDir, env.Host)
			if err != nil {
				return err
			}
			return checkConfigDisk(ctx, env.Remote, env.Host, devices, disk)
		}},
		runner.Step{Title: "Check target", Fn: func(ctx context.Context) error {
			return checkTarget(ctx, env.Remote)
		}},
	)
	if err != nil {
		return err
	}

	if err := confirmInstall(ctx, env.Remote, env.Host, disk); err != nil {
		return err
	}
	if err := runner.Run(ctx, "Deliver LUKS passphrase", func(ctx context.Context) error {
		return askAndSendPassphrase(ctx, env.Remote)
	}); err != nil {
		return err
	}

	env.State.Disk = disk
	runner.Info(ctx, "Disk: "+disk)
	return nil
}

// checkBootstrapKeys fails when none of the provider's agent keys is among
// authorized, root's keys on the installed system: password login is off there,
// so the installer could not reach it after the base install.
func checkBootstrapKeys(ctx context.Context, env *pipeline.Env, authorized []string) error {
	agent, err := env.Local.Quiet().Run(ctx, "ssh-add", "-L")
	if err != nil {
		return fmt.Errorf("no keys in the ssh-agent (load one with ssh-add): %w", err)
	}
	if !agentKeyAuthorized(agent, authorized) {
		return fmt.Errorf("none of the ssh-agent keys is in root's authorized keys of the %s bootstrap system; "+
			"the installed system would be unreachable", env.Host)
	}
	return nil
}

// agentKeyAuthorized reports whether a key of `ssh-add -L` output is among the
// authorized_keys lines. Keys are compared by their base64 blob, so comments
// and options do not matter.
func agentKeyAuthorized(agent string, authorized []string) bool {
	blobs := map[string]bool{}
	for _, line := range strings.Split(agent, "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			blobs[f[1]] = true
		}
	}
	for _, line := range authorized {
		for _, field := range strings.Fields(line) {
			if blobs[field] {
				return true
			}
		}
	}
	return false
}

// managedPaths are the files, relative to the config flake, that the installer
// itself changes.
func managedPaths(hardwareConfigPath string) []string {
	return []string{
		hardwareConfigPath,
		"flake.lock",
	}
}

// checkConfigRepo requires the config to be a git repository with an upstream
// and without uncommitted changes the installer does not manage: only pushed
// config is installed in Phase 3.
func checkConfigRepo(ctx context.Context, env *pipeline.Env, hardwareConfigPath string) error {
	prefix, err := git.Prefix(ctx, env.Local, env.ConfigDir)
	if err != nil {
		return fmt.Errorf("config %s is not a git repository: %w", env.ConfigDir, err)
	}
	ok, err := git.HasUpstream(ctx, env.Local, env.ConfigDir)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("the branch of %s has no upstream; push it first (git push -u)", env.ConfigDir)
	}
	dirty, err := git.Dirty(ctx, env.Local, env.ConfigDir)
	if err != nil {
		return err
	}
	if other := unmanagedChanges(dirty, prefix, hardwareConfigPath); len(other) > 0 {
		return fmt.Errorf("uncommitted changes in %s: %s\ncommit and push them first", env.ConfigDir, strings.Join(other, ", "))
	}
	return nil
}

// unmanagedChanges returns the dirty paths (relative to the repository top)
// that are not files the installer manages. prefix is where the config flake
// sits in the repository.
func unmanagedChanges(dirty []string, prefix, hardwareConfigPath string) []string {
	var managed []string
	for _, p := range managedPaths(hardwareConfigPath) {
		managed = append(managed, prefix+p)
	}
	var other []string
	for _, p := range dirty {
		if !slices.Contains(managed, p) {
			other = append(other, p)
		}
	}
	return other
}

// checkConfigDisk fails unless the host's disko layout, devices by disk name,
// installs to disk and all its devices exist on target. The operator sets the
// devices in the config: the installer only compares them, after readlink -f on
// the target, and says what to change.
func checkConfigDisk(ctx context.Context, target pipeline.Machine, host string, devices map[string]string, disk string) error {
	names := slices.Sorted(maps.Keys(devices))
	found := false
	var listed []string
	for _, name := range names {
		listed = append(listed, fmt.Sprintf("%s = %q", name, devices[name]))
		// readlink fails when the device's directory is missing: not this disk.
		if same, err := sameDevice(ctx, target, devices[name], disk); err == nil && same {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("the disko layout of %s has %s, not %q\nset the device to %q in the config, then commit and push",
			host, strings.Join(listed, ", "), disk, disk)
	}
	for _, name := range names {
		if _, err := target.Run(ctx, "test", "-b", devices[name]); err != nil {
			return fmt.Errorf("%s (disk %s of %s) is not a block device on the target", devices[name], name, host)
		}
	}
	return nil
}

// checkTarget checks what the base install needs on the target ISO.
func checkTarget(ctx context.Context, target pipeline.Machine) error {
	for _, tool := range []string{"nix", "lsblk", "curl"} {
		if _, err := target.Run(ctx, "sh", "-c", `command -v "$1" >/dev/null`, "sh", tool); err != nil {
			return fmt.Errorf("%s is missing on the target", tool)
		}
	}
	_, err := target.Run(ctx, "curl", "-fsS", "--max-time", "10", "-o", "/dev/null", "https://cache.nixos.org/nix-cache-info")
	if err != nil {
		return fmt.Errorf("target cannot reach cache.nixos.org: %w", err)
	}
	return nil
}

// confirmInstall shows what is about to be wiped and requires the operator to
// type "install".
func confirmInstall(ctx context.Context, target pipeline.Machine, host, disk string) error {
	name, err := target.Run(ctx, "hostname")
	if err != nil {
		return err
	}
	disks, err := target.Run(ctx, "lsblk", "-d", "-o", "NAME,SIZE,MODEL,TYPE")
	if err != nil {
		return err
	}
	runner.Info(ctx, fmt.Sprintf("Target %s will install %s", strings.TrimSpace(name), host))
	for _, line := range strings.Split(strings.TrimSpace(disks), "\n") {
		runner.Info(ctx, line)
	}
	answer, err := runner.Ask(ctx, fmt.Sprintf("Everything on %s will be erased. Type 'install' to continue: ", disk))
	if errors.Is(err, io.EOF) {
		return errors.New("confirmation required: run the installer in a terminal")
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(answer) != "install" {
		return errors.New("aborted: nothing was changed on the target")
	}
	return nil
}

// askAndSendPassphrase asks for the LUKS passphrase (twice) and puts it in
// diskPasswordPath on target. Stage 0.2 calls it again when it resumes after
// the ISO was rebooted and /tmp is empty. The passphrase is only ever in
// memory, on stdin of a quiet command and in that file: not in the environment
// (every child process sees it), not in args, not in the log.
func askAndSendPassphrase(ctx context.Context, target pipeline.Machine) error {
	pw, err := askPassphrase(ctx)
	if err != nil {
		return err
	}
	return sendPassphrase(ctx, target, pw)
}

// askPassphrase asks until two non-empty answers agree.
func askPassphrase(ctx context.Context) (string, error) {
	for {
		first, err := runner.AskSecret(ctx, "LUKS passphrase: ")
		if err != nil {
			return "", fmt.Errorf("LUKS passphrase: %w", err)
		}
		second, err := runner.AskSecret(ctx, "Repeat LUKS passphrase: ")
		if err != nil {
			return "", fmt.Errorf("LUKS passphrase: %w", err)
		}
		switch {
		case first == "":
			runner.Warn(ctx, "The passphrase cannot be empty")
		case first != second:
			runner.Warn(ctx, "The passphrases do not match")
		default:
			return first, nil
		}
	}
}

// sendPassphrase writes pw, without a newline, to diskPasswordPath (mode 600)
// on target.
func sendPassphrase(ctx context.Context, target pipeline.Machine, pw string) error {
	_, err := target.Quiet().RunStdin(ctx, strings.NewReader(pw),
		"sh", "-c", `rm -f "$1" && umask 077 && cat > "$1"`, "sh", diskPasswordPath)
	if err != nil {
		return fmt.Errorf("send LUKS passphrase: %w", err)
	}
	return nil
}

// resolveDisk returns the install disk: --disk, else the disk recorded by an
// earlier run, a lone disk on the target, or the operator's pick. A disk that
// differs from the recorded one is an error: earlier stages worked on that one.
// Paths are compared after readlink -f, so a /dev/disk/by-id/ path and the
// /dev/<name> lsblk reports are the same disk.
func resolveDisk(ctx context.Context, env *pipeline.Env) (string, error) {
	recorded := env.State.Disk
	var realRecorded string
	if recorded != "" {
		var err error
		if realRecorded, err = realPath(ctx, env.Remote, recorded); err != nil {
			return "", err
		}
	}

	disk := env.Inputs.Disk
	if disk == "" {
		disks, err := listDisks(ctx, env.Remote)
		if err != nil {
			return "", fmt.Errorf("list disks on target: %w", err)
		}
		if disk, err = chooseDisk(ctx, disks, realRecorded); err != nil {
			return "", err
		}
	}
	if recorded == "" {
		return disk, nil
	}
	same, err := sameDevice(ctx, env.Remote, recorded, disk)
	if err != nil {
		return "", err
	}
	if !same {
		return "", fmt.Errorf("install disk is %s but %s was used before; remove %s to start over",
			disk, recorded, env.State.Path())
	}
	return recorded, nil
}

// realPath is path with symlinks resolved, as seen on m.
func realPath(ctx context.Context, m pipeline.Machine, path string) (string, error) {
	out, err := m.Run(ctx, "readlink", "-f", path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// sameDevice reports whether a and b are the same path once symlinks are
// resolved on m.
func sameDevice(ctx context.Context, m pipeline.Machine, a, b string) (bool, error) {
	if a == b {
		return true, nil
	}
	ra, err := realPath(ctx, m, a)
	if err != nil {
		return false, err
	}
	rb, err := realPath(ctx, m, b)
	if err != nil {
		return false, err
	}
	return ra == rb, nil
}

// Disk is a whole block device that can be installed to.
type Disk struct {
	Path string
	Size int64 // bytes
}

func (d Disk) String() string { return fmt.Sprintf("%s (%d GB)", d.Path, d.Size/1e9) }

// listDisks returns the non-removable whole disks of m.
func listDisks(ctx context.Context, m pipeline.Machine) ([]Disk, error) {
	out, err := m.Run(ctx, "lsblk", "-dnb", "-o", "NAME,SIZE,TYPE,RM")
	if err != nil {
		return nil, err
	}
	return parseLsblk(out), nil
}

// parseLsblk keeps non-removable devices of type "disk" from
// `lsblk -dnb -o NAME,SIZE,TYPE,RM` output.
func parseLsblk(out string) []Disk {
	var disks []Disk
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 4 || f[2] != "disk" || f[3] != "0" {
			continue
		}
		size, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil || size == 0 {
			continue
		}
		disks = append(disks, Disk{Path: "/dev/" + f[0], Size: size})
	}
	return disks
}

// chooseDisk picks the install disk among the candidates found on the target.
// The disk recorded by an earlier run wins, a lone candidate is taken as is and
// with several the operator chooses.
func chooseDisk(ctx context.Context, candidates []Disk, recorded string) (string, error) {
	for _, d := range candidates {
		if d.Path == recorded {
			return recorded, nil
		}
	}
	switch len(candidates) {
	case 0:
		return "", errors.New("no installable disk found on target; use --disk")
	case 1:
		return candidates[0].Path, nil
	}
	items := make([]string, len(candidates))
	for i, d := range candidates {
		items[i] = d.String()
	}
	i, err := runner.Choose(ctx, "Select the install disk:", items)
	if err != nil {
		return "", fmt.Errorf("%w (or pass --disk)", err)
	}
	return candidates[i].Path, nil
}
