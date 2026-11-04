# Installer Context — what the Go pipeline needs to know

Companion to [ROADMAP.md](ROADMAP.md). Captures what exists today in the repos, how it maps onto the pipeline phases, and what the Go installer has to keep in mind while it is built stage by stage. The original Pipeline doc is not in any repo; the stage table in §5 is the spec for "what". Where this file and the roadmap disagree, the roadmap is right.

Checked against the repos on 2026-11-03.

## Decisions so far

- **The Go installer replaces `install-host.sh` entirely.** The script and the `just install | enroll-sops | deploy-remote` recipes are removed from nixos-config, so until the installer is complete there is no other install path. The script stays the reference to port from: `git -C ../nixos-config show 06dd040:scripts/install-host.sh`. Line numbers in the prompts refer to that revision.
- **nix-secrets is a setup only and is not in use** (§3). The installer targets its layout, `hosts/<host>.yaml` and `host-users/<host>-<user>.yaml`, and does not need to support anything older.

---

## 1. The repos at a glance

All are siblings under `~/projects/nix/` on the provider (workhorse), except nix-secrets, which has no local clone.

| Repo | Local path | Remote | Role in pipeline |
|---|---|---|---|
| nixos-config | `../nixos-config` | `git@github.com:JulianKragt/nixos-config.git` | Source of host definitions, `hostSpec`, users and the `bootstrap.<host>` entries. Rebuilt in Phase 3. |
| nixos-bootstrap | `../nixos-bootstrap` | `git@github.com:julian-kragt/nixos-bootstrap.git` | Generic flake with the first, minimal system of a host. Installed in 0.2; `installerData.<host>` is what the installer reads. |
| nix-secrets | none | `git@github.com:JulianKragt/nix-secrets.git` (private) | `.sops.yaml` recipients + encrypted YAML. Setup only today; written in Phase 2. |
| installer | this repo | `git@github.com:julian-kragt/nixos-installer.git` | Go module `installer`, Go 1.27 (`nix develop`). Replaces `install-host.sh`. |

---

## Users — one way to add or remove

The users on a host are the files `home/<u>/<host>.nix` (`usersForHost` in `config/lib/helpers.nix` → `accounts.activeUsers`). Per-user policy lives in `accounts.users.<u>` (full config; today only `secrets`, default true). Install behaviour (hardware generation, SOPS enrollment, deploy, timeouts) is installer flags, not Nix.

- **Add**: create `home/<u>/<host>.nix` (+ `accounts/<u>/keys` if new to the repo), commit. **Delete**: remove the file, commit.
- **Apply** (planned, stage 4.2): `installer secrets --host <h> --target <ip>`. First install runs the same reconcile as later changes.
- **Reconcile** = `users` from `<bootstrap>#installerData.<h>` (ROADMAP "Plumbing design", Users) vs. `host-users/*`: add recipient + author `host-users/<h>-<u>.yaml` + commit/push for new users; prune file and recipient for removed users; then rebuild. The secret file must exist before the rebuild (`mutableUsers = false`).
- Not supported: `useradd`/`userdel` on the box; hand-editing recipients to add a user.
- Kinds: primary admin and additional human users (password + keys + Home Manager), shared login account (`media`, sops password), service users (`isSystemUser`, from service modules, invisible to the installer), operator/root key lists (`accounts/super/keys`, not accounts).

---

## 2. nixos-config — what the installer reads

### Flake shape
- flake-parts "dendritic" layout; every fragment is listed explicitly in `imports.nix` (new host files must be added there).
- Two flakes give a host its two systems. The config's `bootstrap/` entries (`bootstrap.<host>`) feed nixos-bootstrap, with the config passed as its `config` input:
  - `<bootstrap>#nixosConfigurations.<host>` → the minimal first install: root SSH, disko, hardware config and the tools an installer needs
  - `<config>#nixosConfigurations.<host>` → full system (host-spec, core/sops, users, home-manager…)
- Host is a valid install target iff it is an entry of the config's `bootstrap` output (`nix.ListHosts`).
- Hosts today: `atlas` (NixOS workstation, `/dev/nvme0n1`, already installed once), `broadway` (NixOS server, disk still `REPLACE_ME_BEFORE_INSTALL`, users jkragt + media), `workhorse` (darwin), `wsl`.

### Host discovery (`bootstrap/default.nix` in config, `installerData` in nixos-bootstrap)
`provisionSpec`, `installHosts` and the config's `installer/` adapter were removed. `nix eval --json <bootstrap>#installerData.<host> --override-input config <ConfigDir> --no-write-lock-file` returns `{ system, users, rootAuthorizedKeys, hardwareConfigPath, disks }` (`users` maps each normal user to `{ name, uid, home }`, `disks` each disk of the layout to its device). Go: `nix.EvalHostInfo` reads all but `disks` and `users.<u>.name`; `nix.EvalDisks` reads the devices from the bootstrap system. The former install-policy options now live in the installer:

| Former option | Now |
|---|---|
| `generateHardware`, `hardwareConfigPath` | flag / the entry's `hardwareConfigPath` |
| `luksPasswordFile` | fixed `/tmp/disko-password` (matches `disko.nix`) |
| `enrollSops`, `pushSecrets`, `deployFullConfig`, `sshWaitTimeout`, `nixosAnywhereExtra`, `nixSecretsPath` | installer flags with defaults |
| `provisionUserAgeKey` | dropped (obsolete, see §6.2) |

The bootstrap system also sets systemd-boot and root SSH keys, includes git / sops / age / ssh-to-age / vim, enables the nix features an installer needs, and knows the github.com host key.

### Other useful evaluations
- Disks: `nix eval --json <bootstrap>#nixosConfigurations.<host>.config.disko.devices.disk --apply 'builtins.mapAttrs (_: d: d.device)' --override-input config <ConfigDir>` (`nix.EvalDisks`); a `REPLACE_ME` device fails the comparison in 0.1.
- Users on a host: `users` in `installerData.<host>`.

### Disk / boot facts that affect the flow
- Disko layout: GPT, 1G ESP, **LUKS** (`cryptroot`) → btrfs subvolumes `@root @home @nix @swap`. Passphrase file `/tmp/disko-password` must exist on the ISO before nixos-anywhere runs.
- No initrd SSH unlock → after reboot (Stage 0.2) someone must type the LUKS passphrase at the console before SSH comes up. The SSH wait timeout covers this.
- Before install the target is a NixOS ISO; legacy flow asks the operator to run `sudo passwd` so root SSH works.

### SSH / host identity
- openssh: ed25519 only, `/etc/ssh/ssh_host_ed25519_key`, `PermitRootLogin = prohibit-password`, no password auth. NixOS generates the host key on first boot → Stage 1.1 mostly *reads* it.
- Legacy flow: TOFU while waiting, then `ssh-keyscan -t ed25519` pins the key into a temp known_hosts with `StrictHostKeyChecking=yes`.

### How the full config consumes secrets
This wiring is in place, but the secrets behind it are not in use yet (§3).
- Host (`hosts/common/core/sops.nix`): `defaultSopsFile = ${nix-secrets}/hosts/<host>.yaml`; identity = host SSH key (`age.sshKeyPaths`) + `/var/lib/sops-nix/key.txt`; `validateSopsFiles = false`.
- User passwords (`hosts/common/accounts/secrets.nix`): `sops.secrets."passwords/<u>"` from `host-users/<host>-<u>.yaml` key `hashedPassword`, `neededForUsers = true`. `users.mutableUsers = false`, so a missing or undecryptable password = no console login.
- Home Manager (`home/common/core/sops.nix`): `defaultSopsFile = host-users/<host>-<u>.yaml`, identity `~/.config/sops/age/keys.txt`.
- Activation script `sopsUserAgeKeys` moves `/var/lib/sops-nix/pending-user-age-keys/<u>` into `~/.config/sops/age/keys.txt`. Only `install-host.sh` filled that directory, so the script has nothing left to do; removing it is part of B3 (ROADMAP).
- Flake input: `nix-secrets = git+ssh://git@github.com/JulianKragt/nix-secrets.git?ref=main&shallow=1`, `flake = false`.

---

## 3. nix-secrets — setup only, not in use

The repo is set up, but no secret in it is in use and the provider has no clone of it. It gets real content the first time the secrets phase (2.x) runs against it. Do not treat its current files as data to preserve or as proof that a host can decrypt anything.

`main` at `9f435ef` (2026-10-08):

```
.sops.yaml          keys + creation_rules
hosts/<host>.yaml   atlas, workhorse (0 bytes)
host-users/<h>-<u>.yaml  atlas-jkragt, workhorse-jkragt
templates/host.yaml, templates/user.yaml   user template: hashedPassword only
justfile            helpers for editing and rekeying
flake.nix           devShell with the sops tools
```

`.sops.yaml` has the anchors `&host_atlas`, `&user_jkragt`, `&host_workhorse` and one anchored rule per file above. There is no `&operator` yet; stage 2.2 needs it and fails closed without it (ROADMAP "Decisions").

What the old setup got wrong, which the installer must not reintroduce:
- **Duplicate rules**: the bash script checked with `grep` for a different string than it wrote, so every re-run appended a rule. The Go version edits `.sops.yaml` structurally (e.g. `gopkg.in/yaml.v3` Node API, which preserves anchors, aliases and comments).
- sops uses the **first matching** creation rule: insert specific host/user rules before any catch-all and dedupe by `path_regex`.
- Strip template comments when materializing templates, or they end up encrypted in the file (`#ENC[...]`).
- Templates are effectively the schema for Stage 2.3 validation, so they have to agree with what config reads.
- Repeated `add age recipient` / `update flake lock` commits were a symptom of non-idempotent re-runs. Commit only listed paths and only when something changed.

---

## 4. Installer (Go) — current state

**Go is not on PATH on the provider; use it via `nix develop`**, either as a shell or for one command: `nix develop -c go test -race ./...`.

```
cmd/nixos-installer/     main.go    signals, log file, runner, wiring
                         flags.go   --target --host --config --bootstrap --disk --ssh-password --verbose
                         inputs.go  asks for what the flags left empty, loads the host's state
internal/runner/         live task tree, log file, questions to the operator; see docs/RUNNER.md
internal/pipeline/       the engine every stage uses
                         pipeline.go  Inputs, Env, Stage, Run (resume loop)
                         machine.go   Machine: run a command here or on the target over ssh
                         bootstrap.go BootstrapKey: install the provider's keys on the target with the root password
                         state.go     State, LoadState, Save
internal/nix/             nix eval of <bootstrap>#installerData, hosts, disks, the bootstrap system
internal/git/            prefix, upstream, dirty paths, add, commit
internal/stages/         one file per stage, nothing else
                         stages.go    All: the ordered stage list
                         00_environment_preparation.go
                         01_provider_preparation.go
```

How a run goes: `main` parses the flags into `pipeline.Inputs`, `resolveInputs` asks for the missing target and host and loads `<state dir>/<host>.json`, then `pipeline.Run(ctx, env, stages.All)` runs the stages in list order. A stage whose ID is in `State.Completed` is skipped; every other stage is recorded and the state saved when it succeeds. A stage with `Always: true` (0.0) runs every time and is never recorded.

Three kinds of data, kept apart:

- `pipeline.Inputs`: what the operator chose for this run (flags and answers). Never persisted.
- `pipeline.State`: facts that must survive a restart (install disk, completed stages, later recipients and commits). One JSON file per host.
- `pipeline.Machine`: where commands run. `env.Local` is the provider, `env.Remote` the target (`ssh … -l root <target>` with the system ssh, under `umask 077`). `Quiet()` keeps a command's output away from the runner, log and errors; `pipeline.Loopback()` is a Machine for tests whose "ssh" is the local shell.

### Adding a stage

1. `internal/stages/<nn>_<name>.go` with `func <name>(ctx context.Context, env *pipeline.Env) error`. Sub-steps are `runner.Run` / `runner.Parallel`; commands are `env.Local.Run(ctx, "nix", …)` and `env.Remote.Run(ctx, "lsblk", …)`; secrets go through `RunStdin`, never through args.
2. One entry in `stages.All`, at its place in the order.
3. A fact a later run needs becomes a field on `pipeline.State`. A new flag becomes a field on `pipeline.Inputs` and a line in `flags.go`.
4. Code only this stage uses lives in the stage's file, as the disk detection does in `01_provider_preparation.go`. A tool several stages call (`nix eval`, `git`, `sops`) gets its own package named after the tool (`internal/nix`, `internal/git`), with functions taking a `pipeline.Machine`. `internal/stages` holds stage files only.

### Conventions

- No interface without two implementations, no new package unless a second stage needs the code.
- Stages are plain functions; everything they need comes in through `ctx` and `*pipeline.Env`.
- State holds only what must survive a restart.
- Cleanup that has to run after Ctrl+C uses `context.WithoutCancel(ctx)`: a cancelled `ctx` makes every `Machine.Run` fail at once.
- How `Machine`, host key pinning, the env wiring and re-runs are meant to grow: `docs/ROADMAP.md` "Plumbing design".

---

## 5. Legacy bash → Pipeline mapping

Legacy = `install-host.sh` at `06dd040` in nixos-config.

| Pipeline stage | Runs on | Legacy `install-host.sh` equivalent | Port notes |
|---|---|---|---|
| 0.0 Environment preparation | provider (+target) | arg parsing, tool checks | Built. Always runs (not recorded in `State.Completed`). Checks local tools, internet, target reachable on :22, SSH access (installs the provider's keys with the root password while 0.2 is not in `Completed`), config flake. |
| 0.1 Provider preparation | provider (+target checks) | preflight: disk not `REPLACE_ME`, ping, `ssh root@ip`, remote `nix`/`lsblk`/internet check, `test -b $DEVICE`, show `lsblk`, type "install", LUKS prompt → `/tmp/disko-password` | Built. Disk from `--disk`, else the non-removable whole disks on the target: the one recorded by an earlier run, a lone disk, or the operator's pick; compared with the config's disko device after `readlink -f`. Also checks the config repo, the bootstrap keys and system, and the target. Shows the disk, takes a typed confirmation, streams the LUKS passphrase via stdin. |
| 0.2 Base install | provider → target | `nix run github:nix-community/nixos-anywhere -- [--generate-hardware-config …] --build-on-remote --flake <wrapper flake>#<host> --target-host root@ip`; commit hardware config; skip if `/run/current-system` exists (a check that never worked) | Then wait for SSH (`--ssh-wait-timeout`, poll 5s), TOFU then pin ed25519 host key → state. |
| 1.1 Host identity | target | `cat /etc/ssh/ssh_host_ed25519_key.pub \| ssh-to-age` (run on provider) | Can be done in Go with `github.com/Mic92/ssh-to-age` (library) — no tool needed. Store the recipient in state; the fingerprint comes from 0.2. |
| 1.2 Readiness | target | partial (internet ping) | Add `git ls-remote`-style GitHub reachability + `git sops vim nix` presence. |
| 1.3 User identity | target | — (copied the operator key to the target) | New: a standalone age key per user on the host, created on the target. |
| 2.0 Auth bootstrap | target | — (relied on operator's SSH agent on provider) | New. See §6.3. |
| 2.1 Sync repo | target | — (used local `../nix-secrets`) | New: clone/fetch/reset on target. |
| 2.2 Recipients / .sops.yaml | target | python insert of `&host_<h>` + append rule; conflict check when anchor exists with another key | Structural YAML edit, idempotent, also host-users rules. |
| 2.3 Template authoring | target | `cp templates/host.yaml` + `sops --encrypt --in-place` | New: per-user templates, interactive vim over `ssh -t`, schema validation. |
| 2.4 Encrypt/commit/push | target | `sops updatekeys -y hosts/<h>.yaml host-users/<h>-<u>.yaml`, `git add -A; commit; push` | Commit only the listed paths (not `-A`). Verify `sops:` metadata in every secret file. Persist SHA. |
| 3.1 Flake input | provider | `nix flake update nix-secrets` + commit `flake.lock` (in config, on provider) | See §6.4. |
| 3.2 Rebuild | target | `nixos-rebuild --flake .#<h> --target-host root@ip --build-host root@ip switch` (from provider) | Pipeline wants it on target. Validation list from legacy Phase 9 is reusable. |
| 3.3 Cleanup | target | `trap rm KNOWN_HOSTS` only | New: wipe temp creds, optional upstream revoke. |
| 4.1 Policy | provider/target | — | New: no plaintext, rule coverage for every `hosts/*`/`host-users/*`. |
| 4.2 Recovery | — | `--enroll-sops`, `--deploy-remote` modes | Becomes "resume from persisted state". |

Legacy Phase 9 validation checks worth keeping: `systemctl --failed`, `sops-nix` active, `/run/secrets-for-users/passwords/<u>` readable, `home-manager-<u>.service` not failed, `nixos-version`.

---

## 6. Questions that were open, and their outcome

The numbers are the ones the roadmap and the stage prompts refer to. The roadmap's "Decisions" table has the full wording.

- **6.1 Secret paths**: `hosts/<host>.yaml` and `host-users/<host>-<user>.yaml`. Config and nix-secrets both use them.
- **6.2 Who decrypts**: the host key decrypts `host-users/*`; no operator key on the target. Legacy Phase 7.5 is not ported.
- **6.3 Target git auth**: SSH agent forwarding, opt-in per command. Nothing is written to the target; the `git+ssh://` flake URL stays.
- **6.4 Lock + rebuild**: the provider updates, commits and pushes `flake.lock`; the target rebuilds from that pushed revision.
- **6.5 User recipients**: `.sops.yaml` anchors are the source of truth; a missing `&operator` fails closed.
- **6.6 Reinstall**: on an anchor mismatch, show old and new, take a typed confirmation, replace the anchors, rekey from the provider with the operator key.
- **6.7 Shared file**: moot, `shared.yaml` no longer exists. Whether `sops` on the target can decrypt with the host SSH key is still to verify ([00-prep.md](prompts/00-prep.md)).
- **6.8 Bootstrap additions**: done by nixos-bootstrap (§2).
- **6.9 Housekeeping, still open**: `hosts/nixos/atlas/default.nix` sets `users.users.root.initialPassword = "test"` (B3 in the roadmap); `broadway/disko.nix` device is still `REPLACE_ME_BEFORE_INSTALL`.

---

## 7. Direction for the Go code

- **State**: `pipeline.State`, JSON per host in `~/.local/state/nixos-installer/<host>.json`, saved after every stage. Has `Host`, `Disk`, `Completed`. To add as stages need them (no user list: stages read `installerData.<host>` themselves, see ROADMAP "Plumbing design"): `HostKeyFingerprint`, `HostRecipient`, `UserRecipients map[string]string`, `SecretsCommit`, `ConfigRev`. No phase marker: `Completed` says whether 1.2 and 3.3 are done (ROADMAP "Plumbing design", Phase).
- **Stage IDs**: strings matching the Pipeline (`"0.1"`, `"2.3"`…); the order is the order of `stages.All`; resume = skip what is in `State.Completed`.
- **Machines**: `pipeline.Machine` runs a command locally or through the system `ssh`, with stderr streamed to the runner, stdin for secrets (`RunStdin`) and `Quiet()` for commands that print secrets. Still to add with the stage that needs it: an interactive mode (`ssh -t`, attach the terminal, stop the spinner first) for vim.
- **Host key**: `pipeline.SSH` still disables host key checking (TODO in `machine.go`). Stage 0.2 should pin the key (TOFU into a known_hosts file under the state dir) by extending that option list, and pass the same options to nixos-anywhere / nixos-rebuild so there is one SSH configuration.
- **Tool helpers**: `internal/nix` and `internal/git` exist. To add, each in its own package once a second stage needs it (§4): `nixos-anywhere`, `ssh-keyscan`, `sops`, `nixos-rebuild`. Do ssh→age conversion and `.sops.yaml` editing in-process.
- **CLI** (`cmd/nixos-installer/flags.go`, `inputs.go`): `--target` (prompted when empty; IPv4, IPv6, or link-local IPv6 with zone such as `fe80::1%eth0`), `--host` (picked from the config's `bootstrap` output when empty), `--config` (prompted when empty), `--bootstrap` (flake reference, default `github:julian-kragt/nixos-bootstrap`), `--disk` (picked on the target when empty), `--ssh-password` (root password for the first key install; prompted when key auth fails and it is empty), `--verbose`. Planned: `install --host --target` (full) and `secrets --host --target` (Phase 2–3 resume, as in Pipeline 4.2, by filtering `stages.All`); `--state-dir`.
- **Secrets hygiene**: never put secrets in argv or logs (the bash script followed this — keep it), always `umask 077` on target, cleanup in a deferred step even on failure.
