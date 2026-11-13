# Using nixos-installer

This guide follows a first install of `myhost` at `192.0.2.10`, then the runs after it. It assumes the config and
secrets repository are set up as in [setup.md](setup.md).

You work in two places: the terminal on the provider, where the installer runs, and the target's console, where
everything secret is typed. Keep both within reach.

## A first install

### 1. Boot the target

Boot the target from the NixOS ISO (USB or netboot). At its console:

```sh
sudo -i
passwd          # a throwaway password for the ISO's root, used once to install your SSH key
ip addr         # note the address
```

### 2. Start the installer

On the provider:

```sh
nix run github:julian-kragt/nixos-installer -- \
  --config ~/nixos-config --secrets ~/nix-secrets --host myhost --target 192.0.2.10
```

Leave out any flag and it asks for it. On a host with no progress yet it first repeats the preparation above and waits
for Enter.

### 3. Base install (stages 0.0 to 0.2)

- **0.0** checks the provider and connects to the target. It pins the ISO's SSH host key and shows its fingerprint,
  then installs your SSH keys on the ISO with root's password: it asks for the password unless you gave
  `--ssh-password`. A new ISO boot has a new host key: on a later run before the install, type `trust` to accept it.
- **0.1** checks the config repository and the target. If the host has no hardware config yet, it asks (type `yes`) and
  generates one from the target. It lists the disks it is about to wipe and asks you to type `install`.
- Then the target's console switches to a screen of the installer's own (tty9) and asks for the LUKS passphrase,
  twice. Type it there, not on the provider. The console switches back when you are done.
- **0.2** installs the bootstrap system with nixos-anywhere and reboots the target. When the provider says so, type
  the LUKS passphrase at the target's console again, to unlock the disk at boot. The installer waits up to 15 minutes
  for SSH, pins the installed system's host key, then commits and pushes the generated hardware config to the config.

### 4. Keys and secrets (stages 1.1 to 2.5)

- **1.1 to 1.3** check the bootstrap system and create the host's age key and one per user, on the target.
- **2.1 and 2.2** check the secrets clone and write the recipients and rules into `.sops.yaml`. If a new rule has to
  go before an existing rule that also matches its file, type `add` to confirm.
- **2.3**: for every file of the host that is missing or no longer matches its template, the target's console switches
  to tty9 again. For a file with values to fill in, vim opens with its template: replace the values marked `CHANGEME`
  or left empty, save and quit. Then type each password the file needs (each `hashedPassword` leaf), twice. The target hashes the passwords, encrypts the file and
  checks that it decrypts. The provider only receives the encrypted file.
- **2.4** commits the host's files to the secrets repository as one commit (`chore(myhost): update secrets`) and
  pushes it.
- **2.5** reads that commit back from git and checks that every file is there, encrypted, and encrypted to the right
  recipients.

### 5. Deploy (stages 3.1 to 3.3)

- **3.1** locks the config's `nix-secrets` input at that commit and pushes `flake.lock`
  (`chore(myhost): lock nix-secrets at <commit>`).
- **3.2** runs `nixos-rebuild switch` on the target for that pushed revision, with your ssh-agent forwarded so it can
  fetch both repositories. Then it checks that no unit failed, every user's password from sops is in place and no
  Home Manager service failed.
- **3.3** removes what the install left on the target and checks that no forwarded agent is left behind.

The host now runs its full config. A user whose password comes from sops can log in with it.

## Running it again

Run the same command again at any time. Stages that are done are skipped, so on a finished host only 0.0 runs, plus any
stage whose recorded facts no longer hold:

| When | What runs again |
|---|---|
| The host's age key is missing from the state | 1.2 and every stage after it |
| The config's users differ from the users with recorded keys | 1.3 and every stage after it |
| The config locks another `nix-secrets` commit, as after installing another host | 2.4 and every stage after it |
| The pushed secrets commit has a problem | 2.1 and every stage after it |

Stage 0.0 runs every time; it never asks for a password once the bootstrap system is installed.

## Add or remove a user

The users of a host are its normal users in the config (`users.users`, as `installerData.<host>.users` shows them).

1. Add or remove the user in the config, commit and push.
2. Run the installer for the host.

Stage 1.3 sees the change and runs again, with everything after it. A new user gets an age key, a rule and a file, which
you fill in at the console in 2.3. A removed user's rule, anchor (if nothing else uses it) and file are removed. Then the
host is rebuilt. Don't add or remove users on the host itself: `users.mutableUsers = false` undoes it.

## Reinstall a host

Boot the target from the NixOS ISO again, then:

```sh
nix run github:julian-kragt/nixos-installer -- --reinstall --host myhost --target 192.0.2.10 ...
```

The run starts from an empty state and pins the ISO's key to a file of its own. Nothing saved for the host is replaced
until you type `install` in 0.1; stop before that and the host's state is as it was.

The reinstalled host has new age keys, and nobody can re-encrypt its files without the old ones. So 2.2 shows the old
and new recipients and asks you to type `replace`, and 2.3 asks for the values of every file again. Keep a backup of
any secret you can't type again.

## When a stage fails

The installer stops at the first stage that fails, says why, and prints where the full log is. Fix the cause and run it
again: it continues at the stage that failed.

| Message | What to do |
|---|---|
| The config or secrets repository has uncommitted changes, or is ahead of or behind its upstream | Commit, push or pull, then run again. |
| A disk is not a block device on the target | Fix the device in the host's disko layout, commit and push. |
| None of the agent's keys is in `rootAuthorizedKeys` | `ssh-add` the key, or add it to root's keys in the config. |
| The host key changed | Before the install, type `trust` for a new ISO boot. After it, a changed key is refused: check the target, or start over with `--reinstall`. |
| The config's `nix-secrets` input locks another commit (3.1) | `git -C ~/nix-secrets pull`, then run again. 2.4 and the stages after it run again. |
| The rebuild failed (3.2) | Fix the config, commit and push. Then remove `"3.1"` from `Completed` in the state file (`~/.local/state/nixos-installer/myhost.json`) and run again, so the new revision is locked and built. |
| A tool or nix feature is missing on the target (1.1) | The bootstrap system is too old: `nix flake update nixos-bootstrap` in the config, commit, push and reinstall. |

Ctrl+C stops the installer and anything it started on the target's console. Press it twice to quit at once.

## Commits the installer makes

| Repository | Stage | Message | Files |
|---|---|---|---|
| config | 0.2 | `chore(<host>): update generated hardware config` | the host's hardware config |
| secrets | 2.4 | `chore(<host>): update secrets` | `.sops.yaml`, the host's file, its users' per-host and shared files, and the shared files of users who left it |
| config | 3.1 | `chore(<host>): lock nix-secrets at <commit>` | `flake.lock` |

Each commit holds only those files and is made only when something changed.
