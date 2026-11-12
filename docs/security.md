# Security model

nixos-installer is built so that the machine you run it on, the *provider*, never sees a secret. This page says
where each secret and key lives, what the provider holds, and what is left on the target.

## Where secrets are typed

| Secret | Typed at | Lives |
|---|---|---|
| LUKS passphrase | the target's console, on tty9, in the console helper (0.1) | `/tmp/disko-password` in the ISO's RAM, until 0.2 reboots into the installed system |
| Values of the secret files | the target's console, in vim and password prompts (2.3) | the helper's memory, and while vim is open a mode-600 file under `/run/nixos-installer/tmp` (tmpfs), removed when vim exits |
| ISO root password | the provider, or `--ssh-password` | the ISO's RAM; used once to install your SSH key, and gone with the ISO |

The ISO root password is the one secret the provider handles. It is a throwaway that only lets someone into the ISO
until the install reboots it, so passing it as a flag, where `ps` and shell history can show it, is accepted.

The console helper (`nixos-installer-console`) is a static binary the installer copies to `/run/nixos-installer` on the
target and starts with `systemd-run` on tty9, which it brings to the screen. Vim runs there with `--clean -n -i NONE`
and without swap, backup or undo files. Passwords are hashed with `mkpasswd` (yescrypt) and files encrypted with
`sops`, both on the target. Ctrl+C on the provider stops the helper.

## Keys

One key per role. Each is a standalone age key, made with `age-keygen` on the machine that uses it, and never derived
from an SSH key.

| Key | Where | Decrypts |
|---|---|---|
| Host | `/var/lib/sops-nix/key.txt` on the host, root only (1.2) | `hosts/<host>.yaml` and the host's `host-users/<host>-*.yaml` |
| User on a host | `~/.config/sops/age/keys.txt` of that user on that host (1.3) | `host-users/<host>-<user>.yaml` only |

There is **no operator key**. No recipient is on every file, and no machine besides a host can decrypt that host's
secrets. A compromised host exposes only its own secrets; a compromised provider exposes none. The cost is that a
host's secrets are edited and re-encrypted on that host, and a reinstalled host's files have to be typed again: nobody
holds the old key to re-encrypt them.

The installer never replaces an existing key file. If a recipient in `.sops.yaml` differs from the host's key, it
shows both and asks you to type `replace`.

## What the provider holds

- The public age recipients of the host and its users, in the state file and `.sops.yaml`.
- Ciphertext: 2.3 reads back only the encrypted file, checks that every value is `ENC[…]`, and has the target check
  that it decrypts. `sops updatekeys` is never run, because the provider can't decrypt.
- Your SSH keys, in your ssh-agent, which the installer uses but never copies.

Commands that could print a secret run without streaming their output, so nothing secret reaches the terminal, the log
file or an error message. Secrets are passed on stdin, never as command arguments. Remote commands run with
`umask 077`.

## SSH

- **Host key pinning.** Each host has one pinned key, in `<state dir>/<host>.known_hosts`, keyed by host name rather
  than address. The ISO's key is pinned at first contact and its fingerprint shown. A changed key is accepted only
  before the install and only when you type `trust`; afterwards it is refused. 0.2 accepts only an installed-system
  key that differs from the ISO's, and pins it. `--reinstall` pins to a file of its own, which replaces the old pin
  only once you confirm the wipe.
- **nixos-anywhere** can't be given the pin (its own `StrictHostKeyChecking=no` wins), so 0.2 checks the pin over its
  own connection right before starting it.
- **Agent forwarding** is off for every command (`ForwardAgent=no`, whatever your ssh config says) except the ones in
  3.2 that fetch the config and secrets repositories. sshd removes a forwarded agent when its session ends, Ctrl+C and
  dropped connections included. 3.3 checks that no forwarded agent still answers on the target.
- `nix flake metadata` and the rebuild copy the repositories into the target's Nix store, which every user on the
  target can read. The secrets repository holds only ciphertext, but it does show which hosts, users and secrets exist.
  Keep it private.

## What is left on the target

Nothing secret, on success or failure. The LUKS passphrase file lives only in the ISO's RAM, and a retry of 0.2 needs
it, so no cleanup runs when a stage fails. 3.3 still removes `/tmp/disko-password` if it is somehow on the installed
system, and the console helper's `/run/nixos-installer`.

## Reporting a vulnerability

See [SECURITY.md](../SECURITY.md).
