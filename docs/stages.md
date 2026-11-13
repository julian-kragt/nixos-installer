# Stages

The install is a fixed list of stages (`internal/stages/stages.go`), run in order. A stage that succeeds is recorded in
the host's state file and skipped by later runs, unless its **re-run check** finds that what it recorded no longer
holds: then it runs again, and so does every stage after it.

*Provider* is the machine the installer runs on, *target* the machine being installed.

## Phase 0: base install

### 0.0 Environment preparation

Runs on the provider, against the target. **Runs on every run** and is never recorded.

- `ssh` and `git` are on the provider's PATH; the provider is online (`8.8.8.8:53`); the target answers on port 22.
- Checks the target's SSH host key against the pin. On first contact it pins the key and shows its fingerprint.
  Until 0.2 is done, a changed key (every ISO boot makes a new one) is accepted only when you type `trust`. After that it
  is refused.
- Sets up key login. While 0.2 is not done and the target offers password login, it installs the provider's public keys
  for root with the ISO's root password (`--ssh-password`, or asked for).

### 0.1 Provider preparation

Runs on the provider, with checks on the target.

- The config has no uncommitted changes and is level with its upstream, so 0.2's push can't be rejected after the wipe.
- Generates a missing hardware config from the target (`nixos-generate-config --no-filesystems`) after you type `yes`,
  or without asking with `--generate-hardware`.
- Checks that one of the ssh-agent's keys is in `rootAuthorizedKeys`, that the bootstrap system evaluates, that the
  target runs the NixOS ISO and reaches `cache.nixos.org`, and that every disk of `installerData.<host>.disks` is a
  block device on the target.
- Shows the disks and asks you to type `install`. With `--reinstall`, the host's old state and pin are replaced at this
  point, not before.
- Copies the console helper to the target and has you type the LUKS passphrase at its console. It writes it to
  `/tmp/disko-password` on the ISO.

### 0.2 Base install

Runs on the provider, installing the target.

- Runs nixos-anywhere (pinned at 1.13.0, `--build-on remote`) with `<config>#<host>-bootstrap` and
  `--generate-hardware-config`. Only from the NixOS ISO: nixos-anywhere then does not kexec, so the passphrase file
  survives. A target that already runs the host is not installed again; anything else is refused.
- Waits up to 15 minutes for SSH, while someone types the LUKS passphrase at the console, and accepts only a host key
  that differs from the ISO's. It pins that key.
- Commits the generated hardware config and pushes it. A failed push is retried by the next run without reinstalling.

## Phase 1: identity

### 1.1 Readiness

Runs on the target. The bootstrap system has `age-keygen`, `runuser`, `git`, `sops`, `mkpasswd`, `vim`, `systemd-run`,
`nix` and `curl`; nix enables `nix-command`, `flakes` and `pipe-operators`; the target reaches GitHub and knows its SSH
host key.

### 1.2 Host identity

Runs on the target. Creates the host's age key at `/var/lib/sops-nix/key.txt`, unless one exists, and records its
public recipient. The private key never leaves the target.

**Re-run check:** no host recipient is recorded.

### 1.3 User identity

Runs on the target. Creates an age key for every normal user of the host, as that user, at
`~/.config/sops/age/keys.txt`, unless one exists, and records the recipients. A user who is no longer on the host
drops out of the record.

**Re-run check:** the config's users are not the users whose recipients are recorded.

## Phase 2: secrets

### 2.1 Secrets repository

Runs on the provider. Reads the secrets repository's layout, its `installerSecretsData` flake output, and records it
for the rest of the install. The secrets clone has nothing uncommitted besides `.sops.yaml`, the host's own files and
shared user files, and is level with its upstream. User and host names fit the layout.

### 2.2 Recipients

Runs on the provider. Makes `.sops.yaml` say exactly who decrypts the host's files: the `&host_<host>` and
`&user_<user>_<host>` anchors, and one rule per file. The edit is structural and idempotent.

- Fails, changing nothing, while another rule comes before one of the host's rules and also matches its file. Fix the
  order by hand.
- A missing rule is inserted before the first rule that matches its file, or last. When that rule has a `path_regex`,
  you confirm by typing `add`.
- An anchor with another recipient, as after a reinstall, is replaced after you type `replace`.
- Removes the rules, anchors and per-host files of users no longer on the host.
- A shared user file gets the host and the user's key on it added to its rule, next to the user's other hosts. A user
  who left the host is taken out of their shared file's rule; the file is deleted only when no recipient is left, and
  otherwise recorded for 2.3 to encrypt again.

### 2.3 Template authoring

Runs on the provider, with the writing done at the target's console.

- First, each shared user file whose recipients differ from its rule is encrypted to them again, on another host that
  reads it: over ssh as root (at its name, or the address `--rekey-via` gives), with that host's pinned key, or one you
  `trust`. `sops updatekeys` runs there, and `sops rotate` when a recipient was removed, so the plaintext stays on that
  host. When no other host reads the file, as after reinstalling its only host, it is written again from its template.
- The target checks each of the host's files: it exists, has the template's keys, and decrypts with the target's key.
- For the files that are not fine, the provider copies `.sops.yaml` and the templates to `/run/nixos-installer/repo` on
  the target and starts the console helper on tty9. There you write each file in vim and type its passwords. `mkpasswd`
  and `sops` on the target hash them and encrypt the file to its recipients.
- The provider reads back only the ciphertext, checks that every value is `ENC[…]`, puts it in the clone, and has the
  target check that each value decrypts.

### 2.4 Commit and push

Runs on the provider. Checks that every one of the host's files has sops metadata and only encrypted values, commits
`.sops.yaml` and the host's files as one commit (only when something changed), pushes it and records the commit.

**Re-run check:** 3.1 is done, but the config now locks another `nix-secrets` commit, as after installing another
host. 2.4 then records the commit the clone is at.

### 2.5 Pushed secrets

Runs on the provider. Reads the recorded commit with git, not from the clone, before 3.1 locks it:

- the commit is on the upstream;
- each of the host's files exists for the config's users, has only `ENC[…]` values, and is encrypted to the recipients
  of its own rule, which sops uses first;
- no file of a removed user is left.

Nothing is decrypted. It reports every problem, then reopens 2.1 and every stage after it, and fails: the next run
fixes what it can and pushes again. An error that is not a problem of the commit, such as a failed fetch, reopens
nothing.

## Phase 3: deploy

### 3.1 Flake input

Runs on the provider. With the config committed and level with its upstream, runs `nix flake update nix-secrets`,
requires the lock to name the recorded commit, then commits `flake.lock` (only when it changed), pushes it and records
the config revision. If the input locks another commit (its branch moved, or it points at another repository), it puts
`flake.lock` back, reopens 2.4, and asks you to pull the secrets clone.

**Re-run check:** no config revision is recorded.

### 3.2 Rebuild

Runs on the target. Checks that the provider's ssh-agent has keys and the target reaches both repositories with it,
then runs `nixos-rebuild switch --flake git+ssh://…?rev=<config revision>#<host>` on the target, forwarding the agent
for that command only.

With systemd 261 or newer, the switch runs in its own unit, so a connection that drops during the switch only ends
the watcher: 3.2 reconnects within 10 minutes, waits for the unit to end and checks that `/run/current-system` is the
system of the recorded revision. It checks the pinned host key again afterwards.

Then it checks the result: no failed units, `/run/secrets-for-users/passwords/<user>` for every user whose password
comes from sops, no failed `home-manager-<user>.service`. It logs `nixos-version`.

### 3.3 Cleanup

Runs on the target. Removes `/tmp/disko-password` if it is still there (with a warning) and the console helper's
`/run/nixos-installer`. Fails while a forwarded ssh-agent still answers on the target; sockets nobody listens on are
removed.
