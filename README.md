# nixos-installer

Installs a NixOS host from its config flake, from a machine booted from the NixOS ISO to the full system with its
[sops-nix](https://github.com/Mic92/sops-nix) secrets, in one command you can run again until it is done.

```sh
nix run github:julian-kragt/nixos-installer -- \
  --config ~/nixos-config --secrets ~/nix-secrets --host myhost --target 192.0.2.10
```

It runs on your own machine (the *provider*, Linux or macOS) and drives the *target* over SSH. Secrets are never typed
on the provider: the LUKS passphrase and the values of the host's secret files are typed at the target's own console,
and the provider only ever holds public keys and ciphertext.

## Why it exists

A full NixOS config that reads its passwords from sops can't be the first thing on a new host: its secrets have to be
encrypted to a key that only exists once the host is installed, and with `users.mutableUsers = false` a host whose
secrets can't be decrypted has no login. So an install takes several steps, on two machines and three repositories:
install a minimal system, make the host's keys, write and encrypt its secrets, push them, lock them into the config and
switch to the full system. Each step can fail halfway (a dropped connection, a reboot that waits for a passphrase, a
push that is rejected).

nixos-installer does all of it. It records which stages are done, so a run that stops resumes where it stopped, and a
run on a finished host changes nothing.

## How it works

The install builds on [nixos-bootstrap](https://github.com/julian-kragt/nixos-bootstrap), which gives every host in
your config a minimal first system (`<host>-bootstrap`) and an `installerData.<host>` output the installer reads.

| Phase | Stages | What happens |
|---|---|---|
| 0 Base install | 0.0 – 0.2 | Checks the provider, the config and the target, takes your confirmation, has you type the LUKS passphrase at the target's console, and installs the bootstrap system with [nixos-anywhere](https://github.com/nix-community/nixos-anywhere). Commits and pushes the generated hardware config. |
| 1 Identity | 1.1 – 1.3 | Checks the installed bootstrap system, then creates an age key for the host and one for each of its users, on the target. Only their public recipients leave it. |
| 2 Secrets | 2.1 – 2.5 | Writes the recipients into the secrets repository's `.sops.yaml`, has you fill in the host's secret files at the target's console, where they are encrypted, then commits and pushes the ciphertext and checks the pushed commit. |
| 3 Deploy | 3.1 – 3.3 | Locks the config's `nix-secrets` input at that commit and pushes it, switches the target to the full config, checks it, and cleans up. |

Every stage is described in [docs/stages.md](docs/stages.md).

## Requirements

**Your config** is a flake that:

- uses [nixos-bootstrap](https://github.com/julian-kragt/nixos-bootstrap) and lists the host in `bootstrapEntries`;
- has a non-flake input named `nix-secrets`, a `git+ssh://` URL of your secrets repository;
- reads the host's secrets with sops-nix from the paths and keys the installer writes.

**Your secrets repository** is laid out like
[nix-secrets-reference](https://github.com/julian-kragt/nix-secrets-reference): a `.sops.yaml`, and a
`secrets-entry.nix` that says where host and user files and their templates are, and whether users have a file per
host, one shared by their hosts, or both. Its flake outputs that layout as `installerSecretsData`, checked by
nixos-bootstrap.

[docs/setup.md](docs/setup.md) shows both.

**The provider** has `nix` with flakes, `ssh`, `git` and an ssh-agent holding a key that is in the host's
`rootAuthorizedKeys`. It needs local clones of the config and the secrets repository, each pushed to an ssh remote.

**The target** is an `x86_64-linux` or `aarch64-linux` machine booted from the NixOS ISO, with root's password set and
someone at its console (in person, or through IPMI or another remote console). The install wipes the disks of the
host's disko layout.

## Usage

```sh
nix run github:julian-kragt/nixos-installer -- [flags]
```

Every flag that takes a value is asked for when it is left out, so a plain `nixos-installer` walks you through it.

| Flag | Meaning |
|---|---|
| `--config <path>` | Local clone of the config flake. |
| `--secrets <path>` | Local clone of the secrets repository, the config's `nix-secrets` input. |
| `--host <name>` | Host to install, one of the config's `installerData` entries. Picked without asking when there is only one. |
| `--target <ip>` | IP address of the target: IPv4, IPv6, or link-local IPv6 with a zone (`fe80::1%eth0`). |
| `--ssh-password <password>` | Root password of the NixOS ISO, used once to install your SSH key on it. Asked for only when needed. It only lives in the ISO's memory, so passing it as a flag is fine. |
| `--reinstall` | Install the host again from the NixOS ISO, starting from a fresh state. The saved state and pinned host key are replaced only once you confirm the wipe. |
| `--generate-hardware` | Generate a missing hardware config from the target without asking. |
| `--rekey-via <host>=<address>` | Where to reach an installed host that reads a shared secret file, to encrypt it to this host too. Repeatable. Default: the host's name. |
| `--verbose` | Show debug messages and debug steps. |

The installer stops to ask you before anything it can't take back: the wipe (type `install`), a new `.sops.yaml` rule
that goes before an existing one (`add`), replacing a host's recipients after a reinstall (`replace`), a changed SSH
host key on the ISO, or an unpinned one on a host that re-encrypts a shared file (`trust`), and a missing hardware
config (`yes`). Without a terminal it fails instead of asking.

A full walk-through, and what to do when something fails, is in [docs/usage.md](docs/usage.md).

### State and logs

The installer keeps its state in `${XDG_STATE_HOME:-~/.local/state}/nixos-installer`:

| File | What it holds |
|---|---|
| `<host>.json` | The completed stages and the facts later runs need: the host's and users' age recipients, the secrets commit and the config revision. No secrets. |
| `<host>.known_hosts` | The host's pinned SSH host key. |
| `logs/install-<time>.log` | The log of each run. |

## Documentation

| Document | For |
|---|---|
| [docs/setup.md](docs/setup.md) | Preparing your config and secrets repository |
| [docs/usage.md](docs/usage.md) | Installing, re-running, adding and removing users, reinstalling, failures |
| [docs/stages.md](docs/stages.md) | What each stage checks and changes |
| [docs/security.md](docs/security.md) | Where secrets are typed, which keys exist, what the provider holds |
| [docs/architecture.md](docs/architecture.md) | How the code is organised, for contributors |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Building, testing and sending changes |

## Limits

- The config and secrets repositories are fetched over SSH, with agent forwarding from the provider. Stage 1.1 checks
  that the target reaches GitHub and knows its host key, so today both repositories have to be on GitHub.
- The provider checks its internet connection by connecting to `8.8.8.8:53`.
- SSH to the target is root on port 22.
- The config's input for the secrets repository must be called `nix-secrets`.
- Stage 3.2 survives a dropped connection during the switch only with systemd 261 or newer on the target, which runs
  the switch in its own unit.
- nixos-anywhere is pinned at 1.13.0.
- Planned, not built yet: subcommands to run only the secrets phase or show a host's status. Until then, adding or
  removing a user is a plain run (see [docs/usage.md](docs/usage.md#add-or-remove-a-user)).

## License

[MIT](LICENSE)
