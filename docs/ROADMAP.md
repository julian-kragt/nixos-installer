# Roadmap: the Go installer replaces `install-host.sh`

Companion to [CONTEXT.md](CONTEXT.md). Each row in "Order of work" has a prompt in [prompts/](prompts/) that you can give to a fresh chat to get a specific plan for that stage.

## Goal

Replace `install-host.sh` and `just install | enroll-sops | deploy-remote` with the Go installer. Spec for "what": the stage table in `docs/CONTEXT.md` §5 (the Pipeline doc itself is not in any repo). Reference for "how": the bash script, removed from nixos-config and kept in its history (`git -C ../nixos-config show 06dd040:scripts/install-host.sh`).

## State of things (2026-11-07)

- **installer**: stages are plain functions listed in `stages.All`; `machine.Machine` runs a command on the provider or, through the system `ssh`, on the target; `pipeline.State` (`Host`, `Completed`, `HostRecipient`, `UserRecipients`, `SecretsCommit`, `ConfigRev`) is one JSON file per host. Stages 0.0 to 3.2 exist and orchestrate; their logic lives in `internal/machine`, `internal/secrets` (the nix-secrets layout, the `.sops.yaml` editor, templates and checks), `internal/commands/nix`, `internal/commands/git`, `internal/commands/luks`, `internal/commands/nixosanywhere`, `internal/commands/hostidentity`, `internal/commands/sops`, `internal/commands/mkpasswd`, `internal/commands/vim`, `internal/commands/nixosrebuild` and `internal/commands/systemd` (0.2 re-asks the passphrase with `luks.Ensure` when `/tmp/disko-password` is gone). `machine.SSH` and `InstallAuthorizedKeys` check the host key pinned in `<state dir>/<host>.known_hosts`. Build and tests are green. 0.0 to 2.4 have finished on atlas (2026-11-06; its `SecretsCommit` is `5c6778e`); 3.1 and 3.2 have not run there yet.
- **nix-secrets**: a setup only, not in use (CONTEXT §3). The layout is `hosts/<host>.yaml` + `host-users/<host>-<user>.yaml`, and `.sops.yaml` has anchored regexes, but no secret in it is relied on. The provider has a clone at `../nix-secrets`, which the installer gets with `--secrets`. `templates/user.yaml` = `hashedPassword` only.
- **config**: reads `host-users/…`. User SSH keys no longer come from secrets, so the only per-user secret is `hashedPassword`, decrypted by system sops-nix with the host's age key. The config's `installer.<host>` adapter and hand-written `<host>-bootstrap` systems are gone: its `bootstrap/` entries feed the nixos-bootstrap input, which generates `<host>-bootstrap` and `installerData.<host>`, and the installer reads `<config>#installerData.<host>` (`internal/commands/nix`). B3 below is done. `install-host.sh` and its just recipes are removed, so **the Go installer is the only install path, and it is not complete yet**.
- **provider (workhorse)**: has `nix`, `git`, `ssh`; `go` comes from `nix develop`; no `sops`, `age`, `ssh-to-age` in PATH. The secrets phase needs `sops`, `mkpasswd` and `vim` there: the installer's wrapper and `nix develop` add them (the system's vim comes first).

## Decisions (settled)

| Topic | Decision |
|---|---|
| §6.2 who decrypts | The host's age key decrypts `host-users/*`; there is no operator key. Legacy Phase 7.5 is not ported. |
| Where secrets are written | On the provider, in the nix-secrets clone given with `--secrets`. The operator writes each file in vim and types its passwords, which are hashed; the file is encrypted with the provider's `sops` to the public keys read from the target, then committed and pushed from the provider. Plaintext exists in the installer's memory and, while vim is open, in a mode-600 file in a new mode-700 directory under `$TMPDIR`, removed as soon as vim exits; vim runs with `--clean -n -i NONE` and no swap, backup or undo file. Never in an argument or the log. The provider holds no key that decrypts them afterwards; whether a file decrypts is checked on the target, which reports key names and a class only. |
| §6.3 target git auth | SSH agent forwarding (`ssh -A`), opt-in per command on `machine.Machine`, only for the rebuild in 3.2 (the config's `nix-secrets` input). The target never clones nix-secrets. Nothing is written to the target; the `git+ssh://` flake URL stays. |
| §6.4 lock + rebuild | Provider updates, commits and pushes `flake.lock`; target runs `nixos-rebuild switch --flake git+ssh://…/nixos-config?rev=<ConfigRev>#<host>`. Only committed + pushed config is installed. With systemd 261 or newer (262 on atlas and its bootstrap system), nixos-rebuild runs switch-to-configuration in the unit `nixos-rebuild-switch-to-configuration` with `systemd-run --wait --output=cat`, so a connection that drops during the switch only ends the watcher: 3.2 reconnects, waits for the unit to end and checks that `/run/current-system` is the system of `ConfigRev`. |
| §6.5 user recipients | `.sops.yaml` anchors are the source of truth. `hosts/<h>.yaml` = `*host_<h>`. `host-users/<h>-<u>.yaml` = `*host_<h>` + `*user_<u>_<h>`, the user's age key on that host. No other host's key and no operator key is ever added. |
| §6.6 reinstall | On `&host_<h>` or `&user_<u>_<h>` mismatch: show old/new, typed confirmation, replace the anchors. Nobody can rekey without the lost key, so 2.3 asks again for the values of every file whose recipients changed. A secret that cannot be typed again needs its own backup. |
| Keys | One key per role, none shared across machines. Host: standalone age key created on the target at `/var/lib/sops-nix/key.txt` (1.2), never derived from an SSH key and never read off the target. The SSH host key only identifies the machine to SSH, so replacing it does not touch secrets. User on a host: standalone age key created on the target (1.3), never derived from an SSH key. No operator key: the provider only ever holds public keys. |
| §6.7 shared file | Moot: `shared.yaml` no longer exists. |
| Disk | The device is set by hand in the host's disko layout in the config, committed and pushed. The installer does not choose a disk: stage 0.1 reads the devices from `<config>#installerData.<host>.disks`, requires each to be a block device on the target and fails with what to change. No `install.json`. |
| Hardware config | The installer owns it, so a host needs no file before its first install. Stage 0.1 reads the path from `<config>#bootstrapEntries.<host>.hardwareConfigPath`, because `installerData` fails while the file is missing, and, once the operator confirms the path or passes `--generate-hardware`, generates a missing file from the target with `nixos-generate-config --no-filesystems --show-hardware-config`. Stage 0.2 regenerates it during the install, then commits and pushes it. Stage 0.1 refuses a config branch that is ahead of or behind its upstream, so the push cannot be rejected after the wipe or publish other commits. `--config` is a local directory only. |

## Order of work

There is no separate plumbing milestone: shared plumbing is built by the first stage that needs it.

Built: 0.0 to 3.2; on atlas 0.0 to 2.4 have run to the end. `00-prep` is done (see "Verified assumptions"). Next: run 3.1 and 3.2 on atlas, then 3.3.

| # | Stage | Runs on | Needs | Introduces shared plumbing | Writes State | Prompt |
|---|---|---|---|---|---|---|
| – | Prep | provider | – | – | – | [00-prep.md](prompts/00-prep.md) |
| – | Config prerequisites | `../nixos-config` | – | – | – | [config-prerequisites.md](prompts/config-prerequisites.md) |
| 0.0 | Environment preparation | provider (+target checks) | – | `machine.Machine`, `InstallAuthorizedKeys` | – | – |
| 0.1 | Provider preparation | provider (+target checks) | 0.0 | `Machine` stdout/stderr split and `Quiet()`, `machine.Loopback()`, `internal/commands/nix`, `internal/commands/git` | – | [0.1-provider-preparation.md](prompts/0.1-provider-preparation.md) |
| 0.2 | Base install | provider → target | 0.1 | host key pinning (`machine.SSH` and `InstallAuthorizedKeys`), `pipeline.NewEnv` | – | [0.2-base-install.md](prompts/0.2-base-install.md) |
| 1.1 | Readiness | target | 0.2, B2 | – | – | [1.1-readiness.md](prompts/1.1-readiness.md) |
| 1.2 | Host identity | target | 1.1 | – | `HostRecipient` | [1.2-host-identity.md](prompts/1.2-host-identity.md) |
| 1.3 | User identity | target | 1.2 | – | `UserRecipients` | [1.3-user-identity.md](prompts/1.3-user-identity.md) |
| 2.1 | Secrets repository | provider | 1.3 | `--secrets` | – | [2.1-secrets-repo.md](prompts/2.1-secrets-repo.md) |
| 2.2 | Recipients | provider | 2.1 | structural `.sops.yaml` editor | – | [2.2-recipients.md](prompts/2.2-recipients.md) |
| 2.3 | Template authoring | provider (+target checks) | 2.2 | `internal/commands/sops`, `internal/commands/vim`, `runner.Hand`, secret-file checks | – | [2.3-template-authoring.md](prompts/2.3-template-authoring.md) |
| 2.4 | Commit/push | provider | 2.3 | – | `SecretsCommit` | [2.4-commit-push.md](prompts/2.4-commit-push.md) |
| 3.1 | Flake input | provider | 2.4 | – | `ConfigRev` | [3.1-flake-input.md](prompts/3.1-flake-input.md) |
| 3.2 | Rebuild | target | 3.1, B3 | agent forwarding on `Machine` | – | [3.2-rebuild.md](prompts/3.2-rebuild.md) |
| 3.3 | Cleanup | target | 3.2 | – | – | [3.3-cleanup.md](prompts/3.3-cleanup.md) |
| 4.1 | Policy | provider | 2.4 | – | – | [4.1-policy.md](prompts/4.1-policy.md) |
| 4.2 | Recovery (CLI) | provider | all | subcommands, re-run of done stages | – | [4.2-recovery-cli.md](prompts/4.2-recovery-cli.md) |
| – | Finish retiring bash | both repos | all | – | – | [retire-bash.md](prompts/retire-bash.md) |

## Verified assumptions

Checked on atlas on 2026-11-05, on the installed bootstrap system, with sops 3.13.3 on both the provider and atlas.

| Assumption | Outcome |
|---|---|
| Re-run does nothing new | Holds. Only 0.0 ran, 0.1 and 0.2 were skipped, no password prompt, no new commit in the config. |
| Agent forwarding as root | Holds. With `ssh -A`: `ssh-add -l` lists the provider's key, and `git ls-remote` and `nix flake metadata 'git+ssh://…/nix-secrets.git'` both work. Without `-A`, `git ls-remote` fails, so atlas has no access of its own. |
| GitHub host key | Holds. git over ssh runs without a host-key prompt. |
| `SOPS_AGE_KEY_FILE=/var/lib/sops-nix/key.txt sops -d` | Holds, on a file encrypted to `State.HostRecipient`. Stages that decrypt on the target use this form. |

`nix flake metadata` copies the repo to the target's Nix store, which any user there can read, and the copy stays after the session ends. On atlas it is `/nix/store/ldj51r9pfq20fkpzajn0dk4c05hnb132-source`.

## Prerequisites outside this repo

- **B2**: replaced by nixos-bootstrap, whose system has the packages, nix features and github.com known host.
- **B3 (before 3.2)**, done 2026-11-07: config: remove `users.users.root.initialPassword = "test"`; remove the `sopsUserAgeKeys` pending-key script; github.com known host in the full config; in `nixosSops` (`hosts/common/core/sops.nix`) set `age.sshKeyPaths = [ ]` and `age.generateKey = false`, so sops-nix only uses the key from 1.2 and a missing key fails instead of being replaced by a new one that cannot decrypt anything.
- **nix-secrets (before 2.3)**: `templates/host.yaml` = `placeholder: unused`. Nothing reads it, and a value that is not a placeholder is copied without asking. The rest of its content is setup only: 2.2 and 2.3 write the host and user entries.

## Plumbing design

Direction from the 2026-10-13 structure review of the code against this roadmap. Nothing here is built up front: the stage named in each line builds it, as in "Order of work". The points exist so that stages 0.1 and 0.2 do not each decide them again.

| Area | Design | Built by |
|---|---|---|
| `Machine` output | Stdout and stderr are captured separately. `Run` returns stdout only; stderr streams to the runner unless quiet; the error text comes from the stderr tail. | 0.1 |
| `Machine` shape | A struct with fields (address, known-hosts path, flags), not a raw argv. Copy-returning modifiers instead of more `Run*` methods: `Quiet()`, `WithAgent()`. `SSH` sets `ForwardAgent=no`, so the operator's ssh config cannot forward the agent; `WithAgent()` sets `yes` instead. No interactive mode: nothing runs interactively over ssh; the one interactive program, vim in 2.3, runs on the provider through `runner.Hand`. Remote commands run with `umask 077` by default, so that rule does not depend on each stage. | `Quiet` 0.1, `WithAgent` 3.2 |
| Host key | Pins are keyed by host name (`HostKeyAlias=<host>`), in a per-host known_hosts under the state dir, not by IP (the target IP is a per-run input). The ISO key is pinned at first contact in 0.0 and its fingerprint shown; the installed system's key replaces it in 0.2. Applies to `machine.SSH` and to `InstallAuthorizedKeys`. Until 0.2 is done the operator can accept a changed key by typing `trust` (every ISO boot makes a new one); after that it is refused, and `--reinstall` is the way to start over. nixos-anywhere cannot be pinned: its own `StrictHostKeyChecking=no` comes first and ssh takes the first value, whatever `--ssh-option` says. So 0.2 checks the pin over its own connection right before starting it, which is why there is no `Machine.SSHArgs()`. | 0.2 |
| `ControlMaster` | Not used until commands are measured slow. OpenSSH applies the master's `ForwardAgent` to multiplexed sessions (verify), which conflicts with per-command `-A`; if used, put the forwarding mode in `ControlPath`, and keep it short (macOS socket paths: 104 bytes). | – |
| Env wiring | `pipeline.NewEnv(inputs, state)` builds Remote and its pin, which lives next to the state file. | 0.2 |
| Disk | Not chosen by the installer: an install wipes the devices in `installerData.<host>.disks`. 0.1 checks that each is a block device on the target and names them in the typed confirmation. Stage 0.0 only checks the environment, and its root-password bootstrap runs only while 0.2 is not in `Completed` and the target offers password login, so an installed system whose 0.2 was interrupted gets no password prompt. | 0.1 |
| Users | No `State.Users`. A stage that needs the user list reads `users` from `<config>#installerData.<host>` itself (`nix.Config.HostInfo`): every normal user on the host, each with `uid` and `home`. There is no `secrets` flag, because the contract only takes standard NixOS options. 2.2 authors a host-user file for every user; an unused file does no harm. 3.2 checks `home-manager-<u>.service` for all users, and the password file only for users whose full config declares `sops.secrets."passwords/<u>"`, against the config at `ConfigRev`; 4.1 uses the same function. | 0.1 (`internal/commands/nix`) |
| Phase | No `State.Marker`. `Completed` is the only record of progress. `status` derives the phase: 1.1 not in `Completed` = installing; 1.1 in, 3.3 not = awaiting the secrets phase; 3.3 in = converged. For that to stay true when the secrets phase runs again, a forced re-run (`secrets`, `--from`, `--only`) first removes the IDs it will run from `Completed` and saves, then runs. A plain `install` after an interrupted `secrets` run then resumes it. | 4.2 |
| Re-runs (4.2) | `pipeline.Run` takes run options (from / only / force) and the "secrets" group (1.1–4.1) is marked; `Completed` gets no duplicate IDs; `--reinstall` (built: deletes the state file and the host key pin; the wipe is still confirmed by typing `install` in 0.1) is the way to run 0.1–1.2 again after a full install (§6.6, final acceptance test). | 4.2 |
| Tests | `machine.Loopback()` is an exported Machine whose "ssh" is the local shell. Target paths a stage uses are variables, so idempotency tests ("two runs, one commit") run against temp dirs. | 0.1 |
| Cleanup | A cleanup that must run after Ctrl+C uses `context.WithoutCancel(ctx)`; a cancelled `ctx` fails every `Machine.Run` at once. | first user |

## Rules for every stage

- CONTEXT §4 "Adding a stage" and "Conventions" apply: a stage is a plain function in `internal/stages/<id>_<name>.go` plus one entry in `stages.All`; a stage file orchestrates what happens in that stage and holds no complex code, which lives in packages, even when one stage uses it; no interface without two implementations; `internal/stages` holds stage files only.
- Secrets go through `RunStdin`, never into args. A command that can print a secret runs without output streaming, so nothing reaches the runner or the log file. Remote scripts run with `umask 077`.
- Idempotent and safe to re-run. Git commits only for listed paths and only when something changed.
- A stage has no own header (`pipeline.Run` wraps it); sub-steps use `runner.Run` / `runner.Parallel`; parallel steps return values and never write `env.State`.
- A fact a later run needs is a field on `pipeline.State`, added by the stage that first writes it. A new flag is a field on `pipeline.Inputs` and a line in `cmd/nixos-installer/flags.go`.
- `go build ./... && go vet ./... && go test -race ./...` (in `nix develop`) green after every stage.

## End-to-end check

`nix develop -c go run ./cmd/nixos-installer --config ../nixos-config --secrets ../nix-secrets --host <h> --target <ip>` against a target booted from the NixOS ISO; run it a second time and confirm nothing is redone and no new commits appear in nix-secrets or config. A throwaway VM host in config (same CPU architecture as the Mac) avoids wiping atlas. Final acceptance: fresh install, `secrets` run after adding and after removing a `home/<u>/<host>.nix`, and a reinstall of the same host.
