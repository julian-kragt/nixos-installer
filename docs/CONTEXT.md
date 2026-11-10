# Installer Context — what the Go pipeline needs to know

Companion to [ROADMAP.md](ROADMAP.md). Captures what exists today in the repos, how it maps onto the pipeline phases, and what the Go installer has to keep in mind while it is built stage by stage. The original Pipeline doc is not in any repo; the stage table in §5 is the spec for "what". Where this file and the roadmap disagree, the roadmap is right.

Checked against the repos on 2026-11-03.

## Decisions so far

- **The Go installer replaces `install-host.sh` entirely.** The script and the `just install | enroll-sops | deploy-remote` recipes are removed from nixos-config, so until the installer is complete there is no other install path. The script stays the reference to port from: `git -C ../nixos-config show 06dd040:scripts/install-host.sh`. Line numbers in the prompts refer to that revision.
- **nix-secrets is a setup only and is not in use** (§3). The installer targets its layout, `hosts/<host>.yaml` and `host-users/<host>-<user>.yaml`, and does not need to support anything older. So that every file, rule and anchor names one host and user, the secrets stages refuse a user name with a dash, uppercase or a leading digit (`^[a-z_][a-z0-9_]*$`) and a host name with `_` (`secrets.CheckNames`).

---

## 1. The repos at a glance

All are siblings under `~/projects/nix/` on the provider (workhorse).

| Repo | Local path | Remote | Role in pipeline |
|---|---|---|---|
| nixos-config | `../nixos-config` | `git@github.com:JulianKragt/nixos-config.git` | Source of host definitions, `hostSpec`, users and the `bootstrapEntries.<host>` entries. Rebuilt in Phase 3. |
| nixos-bootstrap | `../nixos-bootstrap` | `git@github.com:julian-kragt/nixos-bootstrap.git` | Generic flake with the first, minimal system of a host, an input of the config. Installed in 0.2; `installerData.<host>`, which it adds to the config, is what the installer reads. |
| nix-secrets | `../nix-secrets` (`--secrets`) | `git@github.com:julian-kragt/nix-secrets.git` (private) | `.sops.yaml` recipients + encrypted YAML. Setup only today; written by the provider in Phase 2. |
| installer | this repo | `git@github.com:julian-kragt/nixos-installer.git` | Go module `installer`, Go 1.27 (`nix develop`). Replaces `install-host.sh`. |

---

## Users — one way to add or remove

The users on a host are the files `home/<u>/<host>.nix` (`usersForHost` in `config/lib/helpers.nix` → `accounts.activeUsers`). Per-user policy lives in `accounts.users.<u>` (full config; today only `secrets`, default true). Install behaviour (hardware generation, SOPS enrollment, deploy, timeouts) belongs to the installer, not Nix.

- **Add**: create `home/<u>/<host>.nix` (+ `accounts/<u>/keys` if new to the repo), commit. **Delete**: remove the file, commit.
- **Apply** (planned, stage 4.2): `installer secrets --host <h> --target <ip>`. First install runs the same reconcile as later changes.
- **Reconcile** = `users` from `<config>#installerData.<h>` (ROADMAP "Plumbing design", Users) vs. `host-users/*`: add recipient + author `host-users/<h>-<u>.yaml` + commit/push for new users; prune file and recipient for removed users; then rebuild. The secret file must exist before the rebuild (`mutableUsers = false`).
- Not supported: `useradd`/`userdel` on the box; hand-editing recipients to add a user.
- Kinds: primary admin and additional human users (password + keys + Home Manager), shared login account (`media`, sops password), service users (`isSystemUser`, from service modules, invisible to the installer), operator/root key lists (`accounts/super/keys`, not accounts).

---

## 2. nixos-config — what the installer reads

### Flake shape
- flake-parts "dendritic" layout; every fragment is listed explicitly in `imports.nix` (new host files must be added there).
- The config flake gives a host its two systems. Its `bootstrap/` entries (`bootstrapEntries.<host>`) feed nixos-bootstrap, an input of the config, which adds the first system to the config's outputs:
  - `<config>#nixosConfigurations.<host>-bootstrap` → the minimal first install: root SSH, disko, hardware config and the tools an installer needs
  - `<config>#nixosConfigurations.<host>` → full system (host-spec, core/sops, users, home-manager…)
- Host is a valid install target iff it is an entry of the config's `bootstrapEntries` output (`nix.Config.Hosts`).
- Hosts today: `atlas` (NixOS workstation, `/dev/nvme0n1`, already installed once), `broadway` (NixOS server, disk still `REPLACE_ME_BEFORE_INSTALL`, users jkragt + media), `workhorse` (darwin), `wsl`.

### Host discovery (`bootstrap/default.nix` in config, `installerData` from nixos-bootstrap)
`provisionSpec`, `installHosts` and the config's `installer/` adapter were removed. `nix eval --json <ConfigDir>#installerData.<host> --no-write-lock-file` returns `{ system, users, rootAuthorizedKeys, hardwareConfigPath, disks }` (`users` maps each normal user to `{ name, uid, home }`, `disks` each disk of the layout to its device). Go: `nix.Config.HostInfo` reads all but `users.<u>.name`. The former install-policy options now live in the installer:

| Former option | Now |
|---|---|
| `generateHardware`, `sshWaitTimeout` | fixed in stage 0.2: always generated, 15 minutes |
| `hardwareConfigPath` | the entry's `hardwareConfigPath` |
| `luksPasswordFile` | fixed `/tmp/disko-password` (matches `disko.nix`) |
| `enrollSops`, `pushSecrets`, `deployFullConfig`, `nixosAnywhereExtra`, `nixSecretsPath` | installer flags with defaults |
| `provisionUserAgeKey` | dropped (obsolete, see §6.2) |

The bootstrap system also sets systemd-boot and root SSH keys, includes git / sops / age / ssh-to-age / vim, enables the nix features an installer needs, and knows the github.com host key.

### Other useful evaluations
- Disks: `disks` in `installerData.<host>` (`nix.HostInfo.Disks`); a `REPLACE_ME` device fails the block device check in 0.1.
- Users on a host: `users` in `installerData.<host>`.

### Disk / boot facts that affect the flow
- Disko layout: GPT, 1G ESP, **LUKS** (`cryptroot`) → btrfs subvolumes `@root @home @nix @swap`. Passphrase file `/tmp/disko-password` must exist on the ISO before nixos-anywhere runs.
- No initrd SSH unlock → after reboot (Stage 0.2) someone must type the LUKS passphrase at the console before SSH comes up. The SSH wait timeout covers this.
- Before install the target is a NixOS ISO; legacy flow asks the operator to run `sudo passwd` so root SSH works.

### SSH / host identity
- openssh: ed25519 only, `/etc/ssh/ssh_host_ed25519_key`, `PermitRootLogin = prohibit-password`, no password auth. NixOS generates the host key on first boot. It only identifies the machine to SSH; secrets use the age key from Stage 1.2.
- Legacy flow: TOFU while waiting, then `ssh-keyscan -t ed25519` pins the key into a temp known_hosts with `StrictHostKeyChecking=yes`.

### How the full config consumes secrets
This wiring is in place, but the secrets behind it are not in use yet (§3).
- Host (`hosts/common/core/sops.nix`): `defaultSopsFile = ${nix-secrets}/hosts/<host>.yaml`; identity = `/var/lib/sops-nix/key.txt`, created by Stage 1.2, no SSH key (`age.sshKeyPaths = [ ]`) and no generated key (`age.generateKey = false`); `validateSopsFiles = false`.
- User passwords (`hosts/common/accounts/secrets.nix`): `sops.secrets."passwords/<u>"` from `host-users/<host>-<u>.yaml` key `hashedPassword`, `neededForUsers = true`. `users.mutableUsers = false`, so a missing or undecryptable password = no console login.
- Home Manager (`home/common/core/sops.nix`): `defaultSopsFile = host-users/<host>-<u>.yaml`, identity `~/.config/sops/age/keys.txt`.
- Flake input: `nix-secrets = git+ssh://git@github.com/JulianKragt/nix-secrets.git?ref=main&shallow=1`, `flake = false`.

---

## 3. nix-secrets — setup only, not in use

The repo is set up, but no secret in it is in use. The provider's clone is `../nix-secrets`, passed with `--secrets`. It gets real content the first time the secrets phase (2.x) runs against it. Do not treat its current files as data to preserve or as proof that a host can decrypt anything.

`main` at `9f435ef` (2026-10-08):

```
.sops.yaml          keys + creation_rules
hosts/<host>.yaml   atlas, workhorse (0 bytes)
host-users/<h>-<u>.yaml  atlas-jkragt, workhorse-jkragt
templates/host.yaml, templates/user.yaml   user template: hashedPassword only
justfile            helpers for editing and rekeying
flake.nix           devShell with the sops tools
```

`.sops.yaml` has the anchors `&host_atlas`, `&user_jkragt`, `&host_workhorse` and one anchored rule per file above. There is no operator key and none is added (ROADMAP "Decisions", §6.5). The atlas files are encrypted to atlas' previous host key (and the user file also to the old SSH-derived `&user_jkragt`), so atlas cannot decrypt them; 2.3 writes them again.

What the old setup got wrong, which the installer must not reintroduce:
- **Duplicate rules**: the bash script checked with `grep` for a different string than it wrote, so every re-run appended a rule. The Go version edits `.sops.yaml` structurally (e.g. `gopkg.in/yaml.v3` Node API, which preserves anchors, aliases and comments).
- sops uses the **first matching** creation rule: insert specific host/user rules before any catch-all and dedupe by `path_regex`.
- Strip template comments when materializing templates, or they end up encrypted in the file (`#ENC[...]`).
- Templates are effectively the schema for Stage 2.3 validation, so they have to agree with what config reads. They may nest to any depth; a leaf can be a scalar, a multi-line scalar or a list.
- Repeated `add age recipient` / `update flake lock` commits were a symptom of non-idempotent re-runs. Commit only listed paths and only when something changed.

---

## 4. Installer (Go) — current state

**Go is not on PATH on the provider; use it via `nix develop`**, either as a shell or for one command: `nix develop -c go test -race ./...`.

```
cmd/nixos-installer/     main.go    signals, log file, runner, wiring
                         flags.go   --target --host --config --secrets --ssh-password --reinstall --generate-hardware --verbose
                         inputs.go  asks for what the flags left empty, loads the host's state
internal/runner/         live task tree, log file, questions to the operator; see docs/RUNNER.md
internal/pipeline/       the engine every stage runs in
                         pipeline.go        Inputs, Env, Stage, Run (resume loop)
                         state.go           State, LoadState, Save, IsCompleted
internal/machine/        running commands on a machine, and trusting and logging in to the target
                         machine.go         Machine: run a command here or on the target over ssh
                         access.go          Access: check the host key, set up key login; agent keys, forwarded agents left on a machine
                         authorized_keys.go InstallAuthorizedKeys: install the provider's keys on the target with the root password
                         hostkey.go         HostPin, ScanHostKey, WaitForNewHostKey: the one SSH host key trusted for a host
internal/commands/       the commands the stages run, one package each
                         nix/           Config: nix eval of <config>#installerData, hosts, the bootstrap system
                         git/           Repo: prefix, upstream, dirty and deleted paths, commit, RequireCommitted
                         luks/          the LUKS passphrase on the target, and its removal
                         nixosanywhere/ run nixos-anywhere, tell what a target runs
                         nixosrebuild/  nixos-rebuild switch on the target, waiting out a dropped connection
                         systemd/       failed units on a machine
                         hostidentity/  the age keys of the host and of its users, created on the target
                         sops/          encrypt on the provider; check on the target that a file decrypts
                         mkpasswd/      the crypt hash of a login password
                         vim/           write a file in vim on the provider, leaving no copy behind
internal/secrets/        the nix-secrets repo: file layout, .sops.yaml editor, templates, secret-file checks
internal/stages/         one file per stage, which orchestrates; nothing else
                         stages.go    All: the ordered stage list
                         <id>_<name>.go, named by stage ID: 0_0_environment_preparation.go
                         0_1_provider_preparation.go … 3_3_cleanup.go
```

How a run goes: `main` parses the flags into `pipeline.Inputs`, `resolveInputs` asks for the missing host, loads `<state dir>/<host>.json`, shows how to prepare the target (boot the ISO, set root's password) only when that state has no completed stage, and asks for the missing target, then `pipeline.Run(ctx, env, stages.All)` runs the stages in list order. A stage whose ID is in `State.Completed` is skipped; every other stage is recorded and the state saved when it succeeds. A completed stage with an `Outdated` check runs again when the check gives a reason: it and every stage after it are first removed from `Completed` and the state saved, so a run that stops on the way still runs them next time. 1.2 is outdated without a recorded host recipient, 1.3 when the config's users are not the ones whose recipients are recorded, so a user added to or removed from the host runs the secrets phase again. 3.1 is outdated when the config no longer locks the recorded `SecretsCommit`. A stage with `Always: true` (0.0) runs every time and is never recorded.

Three kinds of data, kept apart:

- `pipeline.Inputs`: what the operator chose for this run (flags and answers). Never persisted.
- `pipeline.State`: facts that must survive a restart (completed stages, later recipients and commits). One JSON file per host.
- `machine.Machine`: where commands run. `env.Local` is the provider, `env.Remote` the target (`ssh … -l root <target>` with the system ssh, under `umask 077`). `Quiet()` keeps a command's output away from the runner, log and errors; `machine.Loopback()` is a Machine for tests whose "ssh" is the local shell.

### Adding a stage

1. `internal/stages/<id>_<name>.go`, with the stage ID's dot as `_` (`2_3_template_authoring.go`), with `func <name>(ctx context.Context, env *pipeline.Env) error`. Sub-steps are `runner.Run` / `runner.Parallel`; commands are `env.Local.Run(ctx, "nix", …)` and `env.Remote.Run(ctx, "lsblk", …)`; secrets go through `RunStdin`, never through args.
2. One entry in `stages.All`, at its place in the order.
3. A fact a later run needs becomes a field on `pipeline.State`. A new flag becomes a field on `pipeline.Inputs` and a line in `flags.go`.
4. A stage file orchestrates what happens in that stage and holds no complex code: the stage function plus flat glue, meaning straight-line calls and messages. Parsing, polling, retry loops and protocol details go in a package under `internal/`, even when one stage uses them. A command (`nix eval`, `git`, `sops`) gets a package named after it under `internal/commands/`, with a type bound to a `machine.Machine` (`nix.Config`, `git.Repo`). `internal/stages` holds stage files only.

### Conventions

- No interface without two implementations.
- Stages are plain functions; everything they need comes in through `ctx` and `*pipeline.Env`.
- State holds only what must survive a restart.
- Cleanup that has to run after Ctrl+C uses `context.WithoutCancel(ctx)`: a cancelled `ctx` makes every `Machine.Run` fail at once.
- How `Machine`, host key pinning, the env wiring and re-runs are meant to grow: `docs/ROADMAP.md` "Plumbing design".

---

## 5. Legacy bash → Pipeline mapping

Legacy = `install-host.sh` at `06dd040` in nixos-config.

| Pipeline stage | Runs on | Legacy `install-host.sh` equivalent | Port notes |
|---|---|---|---|
| 0.0 Environment preparation | provider (+target) | arg parsing, tool checks | Built. Always runs (not recorded in `State.Completed`). Checks local tools, internet, target reachable on :22, SSH access (installs the provider's keys with the root password while 0.2 is not in `Completed` and the target offers password login; the installed system takes keys only), config flake. |
| 0.1 Provider preparation | provider (+target checks) | preflight: disk not `REPLACE_ME`, ping, `ssh root@ip`, remote `nix`/`lsblk`/internet check, `test -b $DEVICE`, show `lsblk`, type "install", LUKS prompt → `/tmp/disko-password` | Built. The disks are those in `installerData.<host>.disks`, each of which must be a block device on the target. Also checks the config repo (no uncommitted changes, level with its upstream), generates a missing hardware config from the target once the operator confirms the path (`--generate-hardware` skips the question), and checks the bootstrap keys and system, and the target. Shows the disks, takes a typed confirmation, streams the LUKS passphrase via stdin. |
| 0.2 Base install | provider → target | `nix run github:nix-community/nixos-anywhere -- [--generate-hardware-config …] --build-on-remote --flake <config>#<host>-bootstrap --target-host root@ip`; commit hardware config; skip if `/run/current-system` exists (a check that never worked) | Built. Only from the ISO: its `VARIANT_ID=installer` makes nixos-anywhere (pinned at 1.13.0) skip kexec, so `/tmp/disko-password` from 0.1 survives and `--disk-encryption-keys` is not needed. Any other system that is not the host is refused. Then wait for SSH (up to 15 minutes, poll 5s), accept only a key that differs from the ISO's, pin it. Finally commit the hardware config and push it; a re-run retries a failed push without reinstalling. |
| 1.1 Readiness | target | partial (internet ping) | Add `git ls-remote`-style GitHub reachability + `git sops vim nix` presence. |
| 1.2 Host identity | target | `cat /etc/ssh/ssh_host_ed25519_key.pub \| ssh-to-age` (run on provider) | Replaced: generates a standalone age key on the target unless it exists (`internal/commands/hostidentity`); the recipient is stored in state. |
| 1.3 User identity | target | — (copied the operator key to the target) | New: a standalone age key per user on the host, created on the target. |
| 2.1 Secrets repository | provider | used local `../nix-secrets` | The `--secrets` clone: committed apart from the host's own files, level with its upstream. |
| 2.2 Recipients / .sops.yaml | provider | python insert of `&host_<h>` + append rule; conflict check when anchor exists with another key | Structural YAML edit in-process, idempotent, also host-users rules. Changes nothing and fails while a rule comes before a secret file's own rule and matches it too (`Config.Shadowed`, every `hosts/*`/`host-users/*` in the clone); the operator fixes the order by hand. A new rule goes last. |
| 2.3 Template authoring | provider (+target checks) | `cp templates/host.yaml` + `sops --encrypt --in-place` | The operator writes the file in vim (no swap, backup, undo or viminfo; temp file removed on exit) and types its passwords; `sops` on the provider encrypts it to the target's public keys; the target checks that each value decrypts, per path with `--extract`. No `updatekeys`: the provider cannot decrypt. |
| 2.4 Commit/push | provider | `sops updatekeys -y hosts/<h>.yaml host-users/<h>-<u>.yaml`, `git add -A; commit; push` | Commit only the listed paths (not `-A`). Verify `sops:` metadata in every secret file. Persist SHA. |
| 3.1 Flake input | provider | `nix flake update nix-secrets` + commit `flake.lock` (in config, on provider) | See §6.4. |
| 3.2 Rebuild | target | `nixos-rebuild --flake .#<h> --target-host root@ip --build-host root@ip switch` (from provider) | Replaced: `nixos-rebuild switch --flake git+ssh://…?rev=<ConfigRev>#<h>` on the target with agent forwarding (`internal/commands/nixosrebuild`); legacy Phase 9's validation without the user-age-key checks. |
| 3.3 Cleanup | target | `trap rm KNOWN_HOSTS` only | Built. Removes a `/tmp/disko-password` still on the installed system, with a warning (`luks.Remove`), and fails while a forwarded agent still answers on the target (`Machine.ForwardedAgents`: `/root/.ssh/agent/*` since OpenSSH 10.1, `/tmp/ssh-*/agent.*` before; sockets nobody listens on are removed). Nothing to revoke: no deploy key exists. |
| 4.1 Pushed secrets | provider | — | New: checks the host's secrets as pushed, at `SecretsCommit` read with git: the commit is on the remote; for the users of the config at `ConfigRev`, each file exists, has only `ENC[…]` values and is encrypted to its own rule's recipients, which sops uses first; no file of a removed user is left. No decryption. Reports every problem, then fails. |
| 4.2 Recovery | — | `--enroll-sops`, `--deploy-remote` modes | Becomes "resume from persisted state". |

Legacy Phase 9 validation checks worth keeping: `systemctl --failed`, `sops-nix` active, `/run/secrets-for-users/passwords/<u>` readable, `home-manager-<u>.service` not failed, `nixos-version`.

---

## 6. Questions that were open, and their outcome

The numbers are the ones the roadmap and the stage prompts refer to. The roadmap's "Decisions" table has the full wording.

- **6.1 Secret paths**: `hosts/<host>.yaml` and `host-users/<host>-<user>.yaml`. Config and nix-secrets both use them.
- **6.2 Who decrypts**: the host's age key decrypts `host-users/*`; there is no operator key. Legacy Phase 7.5 is not ported.
- **6.3 Target git auth**: SSH agent forwarding, opt-in per command, only for the rebuild in 3.2. The secrets are written and pushed on the provider. Nothing is written to the target; the `git+ssh://` flake URL stays.
- **6.4 Lock + rebuild**: the provider updates, commits and pushes `flake.lock`; the target rebuilds from that pushed revision.
- **6.5 User recipients**: `.sops.yaml` anchors are the source of truth; the host key, plus the user's key on that host for host-user files. No operator key.
- **6.6 Reinstall**: on an anchor mismatch, show old and new, take a typed confirmation, replace the anchors; 2.3 then asks again for the values of the files whose recipients changed.
- **6.7 Shared file**: moot, `shared.yaml` no longer exists. Whether `sops` on the target can decrypt with the host SSH key is still to verify ([00-prep.md](prompts/00-prep.md)).
- **6.8 Bootstrap additions**: done by nixos-bootstrap (§2).
- **6.9 Housekeeping, still open**: `broadway/disko.nix` device is still `REPLACE_ME_BEFORE_INSTALL`.

---

## 7. Direction for the Go code

- **State**: `pipeline.State`, JSON per host in `~/.local/state/nixos-installer/<host>.json`, saved after every stage. Has `Host`, `Completed`, `HostRecipient`, `UserRecipients`, `SecretsCommit` and `ConfigRev` (the pushed config commit 3.2 builds). No user list: stages read `installerData.<host>` themselves, see ROADMAP "Plumbing design". The secrets repo is an input (`--secrets`), not state. No phase marker: `Completed` says whether 1.1 and 3.3 are done (ROADMAP "Plumbing design", Phase).
- **Stage IDs**: strings matching the Pipeline (`"0.1"`, `"2.3"`…); the order is the order of `stages.All`; resume = skip what is in `State.Completed`, unless the stage's `Outdated` check says it must run again.
- **Machines**: `machine.Machine` runs a command locally or through the system `ssh`, with stderr streamed to the runner, stdin for secrets (`RunStdin`) and `Quiet()` for commands that print secrets. `SSH` turns agent forwarding off; `WithAgent()` turns it on for one command, used only by 3.2 for the commands that need GitHub. `LostConnection(err)` tells ssh's own failure (exit 255) from the command's. No interactive mode over ssh; vim runs on the provider through `runner.Hand`.
- **Host key**: `machine.HostPin` keeps one key per host in `<state dir>/<host>.known_hosts`, and `machine.SSH` and `InstallAuthorizedKeys` accept only that key. nixos-anywhere cannot be given the pin (ROADMAP "Plumbing design", Host key). Stage 3.2 runs `nixos-rebuild` on the target, so it needs no ssh options from here, and checks the pin again after the switch.
- **Tool helpers**: `internal/commands/nix`, `internal/commands/git`, `internal/commands/luks`, `internal/commands/nixosanywhere`, `internal/commands/hostidentity`, `internal/commands/sops`, `internal/commands/nixosrebuild` and `internal/commands/systemd` exist, each in its own package (§4). `.sops.yaml` editing is in-process.
- **CLI** (`cmd/nixos-installer/flags.go`, `inputs.go`): `--target` (prompted when empty; IPv4, IPv6, or link-local IPv6 with zone such as `fe80::1%eth0`), `--host` (picked from the config's `bootstrapEntries` output when empty), `--config` (prompted when empty), `--secrets` (the nix-secrets clone; prompted when empty, must hold a `.sops.yaml`), `--ssh-password` (the ISO's root password for the first key install; prompted when key auth fails, it is empty and the target offers password login), `--reinstall` (deletes the chosen host's `<host>.json` and `<host>.known_hosts` before the state is loaded, so all stages run again; the target must be booted from the ISO to be installed again), `--generate-hardware` (generates a missing hardware config from the target in 0.1 without asking), `--verbose`. Planned: `install --host --target` (full) and `secrets --host --target` (Phase 2–3 resume, as in Pipeline 4.2, by filtering `stages.All`).
- **Secrets hygiene**: never put secrets in argv or logs (the bash script followed this — keep it), always `umask 077` on target. No cleanup runs when a stage fails, because nothing secret is left to clean: `/tmp/disko-password` lives only in the ISO's RAM, from 0.1 until 0.2 reboots into the installed system, and a retry of 0.2 needs it; sshd removes a forwarded agent when its session ends, Ctrl+C and a dropped connection included; the sops check on the target holds ciphertext only, in a temp file its `trap` removes. 3.3 checks the end state once, on the installed system.
