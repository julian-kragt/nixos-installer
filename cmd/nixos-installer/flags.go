package main

import (
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"

	"installer/internal/pipeline"
)

// sshPasswordEnv holds the ISO's root password for a run that cannot be asked.
// It is not a flag: a command line is visible to every user in ps and is kept
// in the shell's history.
const sshPasswordEnv = "NIXOS_INSTALLER_SSH_PASSWORD"

// options are the command-line inputs. Empty inputs are completed by
// resolveInputs.
type options struct {
	pipeline.Inputs
	verbose bool
}

// parseFlags parses args (without the program name). Usage and flag errors are
// written to stderr; the returned error is already user-presentable.
func parseFlags(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("nixos-installer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.verbose, "verbose", false, "Enable verbose logging")
	fs.StringVar(&o.ConfigDir, "config", "", "Path to the config flake (prompts when empty)")
	fs.StringVar(&o.SecretsDir, "secrets", "", "Path to the nix-secrets repository (prompts when empty)")
	fs.StringVar(&o.Host, "host", "", "Host to install (prompts when empty)")
	fs.StringVar(&o.Target, "target", "", "IP address of the target device (prompts when empty)")
	fs.BoolVar(&o.Reinstall, "reinstall", false, "Delete the host's saved state and pinned host key first, to install it again from the NixOS ISO")
	fs.BoolVar(&o.GenerateHardware, "generate-hardware", false, "Generate a missing hardware config from the target without asking")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage of nixos-installer:")
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), "\nEnvironment:\n  %s\n    \tRoot SSH password of the NixOS ISO for the initial key bootstrap (prompts when unset)\n", sshPasswordEnv)
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if o.Target != "" {
		if err := validateTarget(o.Target); err != nil {
			return options{}, err
		}
	}
	return o, nil
}

// takeSSHPassword returns the password in sshPasswordEnv and removes it from
// the environment, so no command the installer runs inherits it.
func takeSSHPassword() string {
	pw := os.Getenv(sshPasswordEnv)
	os.Unsetenv(sshPasswordEnv)
	return pw
}

func validateTarget(s string) error {
	ip, err := netip.ParseAddr(s) // IPv4, IPv6 and IPv6 with zone (fe80::1%eth0)
	switch {
	// Loopback and unspecified addresses would point the install at this machine.
	case err != nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast():
		return fmt.Errorf("--target: %q is not a valid target IP address", s)
	case ip.Is6() && ip.IsLinkLocalUnicast() && ip.Zone() == "":
		return fmt.Errorf("--target: link-local address %q needs a zone, e.g. %s%%eth0", s, s)
	}
	return nil
}
