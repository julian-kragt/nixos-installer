package main

import (
	"flag"
	"fmt"
	"io"
	"net/netip"

	"installer/internal/pipeline"
)

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
	fs.StringVar(&o.Host, "host", "", "Host to install (prompts when empty)")
	fs.StringVar(&o.Target, "target", "", "IP address of the target device (prompts when empty)")
	fs.StringVar(&o.SSHPassword, "ssh-password", "", "Root SSH password of the NixOS ISO for the initial key bootstrap (prompts when empty)")
	fs.BoolVar(&o.Reinstall, "reinstall", false, "Delete the host's saved state and pinned host key first, to install it again from the NixOS ISO")
	fs.BoolVar(&o.GenerateHardware, "generate-hardware", false, "Generate a missing hardware config from the target without asking")
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
