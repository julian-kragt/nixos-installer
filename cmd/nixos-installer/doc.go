// Command nixos-installer installs a NixOS host from its config flake, from the
// NixOS ISO to the full system with its sops secrets.
//
// It runs on the operator's machine and drives the target over SSH. The
// config must use nixos-bootstrap
// (https://github.com/julian-kragt/nixos-bootstrap), which gives every host a
// minimal first system and the installerData output read here. The install
// runs in stages: the bootstrap system is installed with nixos-anywhere, the
// target gets its own age keys, the host's secrets are written at the
// target's console and pushed to the secrets repository, the config locks
// them, and the target switches to its full system.
//
// Usage:
//
//	nixos-installer [flags]
//
// The flags are:
//
//	--config path
//		Local clone of the config flake. Asked for when empty.
//	--secrets path
//		Local clone of the secrets repository, the config's nix-secrets
//		input. Asked for when empty.
//	--host name
//		Host to install, one of the config's installerData entries. Asked
//		for when empty, and picked when there is only one.
//	--target address
//		IP address of the target. Asked for when empty.
//	--ssh-password password
//		Root password of the NixOS ISO, for the first key install. Asked for
//		when needed and empty.
//	--reinstall
//		Install the host again from the NixOS ISO, starting from a fresh state.
//	--generate-hardware
//		Generate a missing hardware config from the target without asking.
//	--verbose
//		Show debug messages and debug steps.
//
// Progress is saved per host in ${XDG_STATE_HOME:-~/.local/state}/nixos-installer,
// so a run that stops resumes where it stopped, and a second run of a finished
// install only checks the environment. Each run writes a log file to the logs
// directory there.
//
// See README.md and docs/ in the repository for setup and usage.
package main
