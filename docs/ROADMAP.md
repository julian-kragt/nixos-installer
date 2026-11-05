# Roadmap: the Go installer replaces `install-host.sh`

Companion to [CONTEXT.md](CONTEXT.md). Each row in "Order of work" has a prompt in [prompts/](prompts/) that you can give to a fresh chat to get a specific plan for that stage.

## Goal

Replace `install-host.sh` and `just install | enroll-sops | deploy-remote` with the Go installer. Spec for "what": the stage table in `docs/CONTEXT.md` §5 (the Pipeline doc itself is not in any repo). Reference for "how": the bash script, removed from nixos-config and kept in its history (`git -C ../nixos-config show 06dd040:scripts/install-host.sh`).

## State of things (2026-11-03)

- **installer**: stages are plain functions listed in `stages.All`; `machine.Machine` runs a command on the provider or, through the system `ssh`, on the target; `pipeline.State` (`Host`, `Completed`) is one JSON file per host. Stages 0.0, 0.1 and 0.2 exist and orchestrate; their logic lives in `internal/machine`, `internal/commands/nix`, `internal/commands/git`, `internal/commands/luks` and `internal/commands/nixosanywhere` (0.2 re-asks the passphrase with `luks.Ensure` when `/tmp/disko-password` is gone). `machine.SSH` and `InstallAuthorizedKeys` check the host key pinned in `<state dir>/<host>.known_hosts`. Build and tests are green, but none of these stages has finished on a real target yet.
- **nix-secrets**: a setup only, not in use (CONTEXT §3). The layout is `hosts/<host>.yaml` + `host-users/<host>-<user>.yaml`, and `.sops.yaml` has anchored regexes, but no secret in it is relied on and the provider has no clone. `templates/user.yaml` = `hashedPassword` only.
- **config**: reads `host-users/…`. User SSH keys no longer come from secrets, so the only per-user secret is `hashedPassword`, decrypted by system sops-nix with the host key. The config's `installer.<host>` adapter and hand-written `<host>-bootstrap` systems are gone: its `bootstrap/` entries feed the nixos-bootstrap input, which generates `<host>-bootstrap` and `installerData.<host>`, and the installer reads `<config>#installerData.<host>` (`internal/commands/nix`). Still open: B3 below. `install-host.sh` and its just recipes are removed, so **the Go installer is the only install path, and it is not complete yet**.
- **provider (workhorse)**: has `nix`, `git`, `ssh`; `go` comes from `nix develop`; no `sops`, `age`, `ssh-to-age` in PATH.

## Decisions (settled)

| Topic | Decision |
|---|---|
| §6.2 who decrypts | Host key decrypts `host-users/*`; no operator key on the target. Legacy Phase 7.5 is not ported. |
| §6.3 target git auth | SSH agent forwarding (`ssh -A`), opt-in per command on `machine.Machine`. Nothing is written to the target; the `git+ssh://` flake URL stays. |
| §6.4 lock + rebuild | Provider updates, commits and pushes `flake.lock`; target runs `nixos-rebuild switch --flake git+ssh://…/nixos-config?rev=<ConfigRev>#<host>`. Only committed + pushed config is installed. |
| §6.5 user recipients | `.sops.yaml` anchors are the source of truth. `hosts/<h>.yaml` = `*host_<h>` + `*operator`. `host-users/<h>-<u>.yaml` = those two + `*user_<u>_<h>`, the user's age key on that host. No other host's key is ever added. Missing `&operator` → fail closed. |
| §6.6 reinstall | On `&host_<h>` or `&user_<u>_<h>` mismatch: show old/new, typed confirmation, replace the anchors, rekey the host's files from the provider with the operator key. |
| Keys | One key per role, none shared across machines. Host: SSH host key, generated on the target by NixOS and used through `sops.age.sshKeyPaths`; its recipient is converted from the public key (1.1). User on a host: standalone age key created on the target (1.3), never derived from an SSH key. Operator: standalone age key on the provider, never copied to a target. |
| §6.7 shared file | Moot: `shared.yaml` no longer exists. |
| Disk | The device is set by hand in the host's disko layout in the config, committed and pushed. The installer does not choose a disk: stage 0.1 reads the devices from `<config>#installerData.<host>.disks`, requires each to be a block device on the target and fails with what to change. No `install.json`. |

## Order of work

There is no separate plumbing milestone: shared plumbing is built by the first stage that needs it.

Built: 0.0, 0.1 and 0.2, not yet run to the end on a real target. Next: run them against atlas, then `00-prep` on the installed system, then 1.1.

| # | Stage | Runs on | Needs | Introduces shared plumbing | Writes State | Prompt |
|---|---|---|---|---|---|---|
| – | Prep | provider | – | – | – | [00-prep.md](prompts/00-prep.md) |
| – | Config prerequisites | `../nixos-config` | – | – | – | [config-prerequisites.md](prompts/config-prerequisites.md) |
| 0.0 | Environment preparation | provider (+target checks) | – | `machine.Machine`, `InstallAuthorizedKeys` | – | – |
| 0.1 | Provider preparation | provider (+target checks) | 0.0 | `Machine` stdout/stderr split and `Quiet()`, `machine.Loopback()`, `internal/commands/nix`, `internal/commands/git` | – | [0.1-provider-preparation.md](prompts/0.1-provider-preparation.md) |
| 0.2 | Base install | provider → target | 0.1 | host key pinning (`machine.SSH` and `InstallAuthorizedKeys`), `pipeline.NewEnv` | – | [0.2-base-install.md](prompts/0.2-base-install.md) |
| 1.1 | Host identity | target | 0.2 | ssh→age in-process | `HostRecipient` | [1.1-host-identity.md](prompts/1.1-host-identity.md) |
| 1.2 | Readiness | target | 1.1, B2 | – | – | [1.2-readiness.md](prompts/1.2-readiness.md) |
| 1.3 | User identity | target | 1.2 | – | `UserRecipients` | [1.3-user-identity.md](prompts/1.3-user-identity.md) |
| 2.0 | Auth bootstrap | target | 1.3 | agent forwarding on `Machine` | `SecretsURL` | [2.0-auth-bootstrap.md](prompts/2.0-auth-bootstrap.md) |
| 2.1 | Sync repo | target | 2.0 | – | – | [2.1-sync-repo.md](prompts/2.1-sync-repo.md) |
| 2.2 | Recipients | target | 2.1 | structural `.sops.yaml` editor | – | [2.2-recipients.md](prompts/2.2-recipients.md) |
| 2.3 | Template authoring | target | 2.2 | interactive `ssh -t` + runner hand-over, secret-file checks | – | [2.3-template-authoring.md](prompts/2.3-template-authoring.md) |
| 2.4 | Encrypt/commit/push | target | 2.3 | – | `SecretsCommit` | [2.4-commit-push.md](prompts/2.4-commit-push.md) |
| 3.1 | Flake input | provider | 2.4 | – | `ConfigRev` | [3.1-flake-input.md](prompts/3.1-flake-input.md) |
| 3.2 | Rebuild | target | 3.1, B3 | – | – | [3.2-rebuild.md](prompts/3.2-rebuild.md) |
| 3.3 | Cleanup | target | 3.2 | – | – | [3.3-cleanup.md](prompts/3.3-cleanup.md) |
| 4.1 | Policy | provider | 2.4 | – | – | [4.1-policy.md](prompts/4.1-policy.md) |
| 4.2 | Recovery (CLI) | provider | all | subcommands, re-run of done stages | – | [4.2-recovery-cli.md](prompts/4.2-recovery-cli.md) |
| – | Finish retiring bash | both repos | all | – | – | [retire-bash.md](prompts/retire-bash.md) |

## Prerequisites outside this repo

- **B2**: replaced by nixos-bootstrap, whose system has the packages, nix features and github.com known host.
- **B3 (before 3.2)** config: remove `users.users.root.initialPassword = "test"`; remove the `sopsUserAgeKeys` pending-key script; github.com known host in the full config.
- **nix-secrets (before 2.2)**: add the `&operator` anchor. The rest of its content is setup only: 2.2 and 2.3 write the host and user entries.

## Plumbing design

Direction from the 2026-10-13 structure review of the code against this roadmap. Nothing here is built up front: the stage named in each line builds it, as in "Order of work". The points exist so that stages 0.1 and 0.2 do not each decide them again.

| Area | Design | Built by |
|---|---|---|
| `Machine` output | Stdout and stderr are captured separately. `Run` returns stdout only; stderr streams to the runner unless quiet; the error text comes from the stderr tail. | 0.1 |
| `Machine` shape | A struct with fields (address, known-hosts path, flags), not a raw argv. Copy-returning modifiers instead of more `Run*` methods: `Quiet()`, `WithAgent()`, `Interactive()`. Remote commands run with `umask 077` by default, so that rule does not depend on each stage. | `Quiet` 0.1, `WithAgent` 2.0, `Interactive` 2.3 |
| Host key | Pins are keyed by host name (`HostKeyAlias=<host>`), in a per-host known_hosts under the state dir, not by IP (the target IP is a per-run input). The ISO key is pinned at first contact in 0.0 and its fingerprint shown; the installed system's key replaces it in 0.2. Applies to `machine.SSH` and to `InstallAuthorizedKeys`. Until 0.2 is done the operator can accept a changed key by typing `trust` (every ISO boot makes a new one); after that it is refused. nixos-anywhere cannot be pinned: its own `StrictHostKeyChecking=no` comes first and ssh takes the first value, whatever `--ssh-option` says. So 0.2 checks the pin over its own connection right before starting it, which is why there is no `Machine.SSHArgs()`. | 0.2 |
| `ControlMaster` | Not used until commands are measured slow. OpenSSH applies the master's `ForwardAgent` to multiplexed sessions (verify), which conflicts with per-command `-A`; if used, put the forwarding mode in `ControlPath`, and keep it short (macOS socket paths: 104 bytes). | – |
| Env wiring | `pipeline.NewEnv(inputs, state)` builds Remote and its pin, which lives next to the state file. | 0.2 |
| Disk | Not chosen by the installer: an install wipes the devices in `installerData.<host>.disks`. 0.1 checks that each is a block device on the target and names them in the typed confirmation. Stage 0.0 only checks the environment, and its root-password bootstrap runs only while 0.2 is not in `Completed` and the target offers password login, so an installed system whose 0.2 was interrupted gets no password prompt. | 0.1 |
| Users | No `State.Users`. A stage that needs the user list reads `users` from `<config>#installerData.<host>` itself (`nix.Config.HostInfo`): every normal user on the host, each with `uid` and `home`. There is no `secrets` flag, because the contract only takes standard NixOS options. 2.2 authors a host-user file for every user; an unused file does no harm. 3.2 checks `home-manager-<u>.service` for all users, and the password file only for users whose full config declares `sops.secrets."passwords/<u>"`, against the config at `ConfigRev`; 4.1 uses the same function. | 0.1 (`internal/commands/nix`) |
| Phase | No `State.Marker`. `Completed` is the only record of progress. `status` derives the phase: 1.2 not in `Completed` = installing; 1.2 in, 3.3 not = awaiting the secrets phase; 3.3 in = converged. For that to stay true when the secrets phase runs again, a forced re-run (`secrets`, `--from`, `--only`) first removes the IDs it will run from `Completed` and saves, then runs. A plain `install` after an interrupted `secrets` run then resumes it. | 4.2 |
| Re-runs (4.2) | `pipeline.Run` takes run options (from / only / force) and the "secrets" group (1.1–4.1) is marked; `Completed` gets no duplicate IDs; `install --reinstall` (typed confirmation, archives the state file) is the way to run 0.1–1.1 again after a full install (§6.6, final acceptance test). | 4.2 |
| Tests | `machine.Loopback()` is an exported Machine whose "ssh" is the local shell. Target paths a stage uses are variables, so idempotency tests ("two runs, one commit") run against temp dirs. | 0.1 |
| Cleanup | A cleanup that must run after Ctrl+C uses `context.WithoutCancel(ctx)`; a cancelled `ctx` fails every `Machine.Run` at once. | first user (2.3) |

## Rules for every stage

- CONTEXT §4 "Adding a stage" and "Conventions" apply: a stage is a plain function in `internal/stages/NN_name.go` plus one entry in `stages.All`; a stage file orchestrates what happens in that stage and holds no complex code, which lives in packages, even when one stage uses it; no interface without two implementations; `internal/stages` holds stage files only.
- Secrets go through `RunStdin`, never into args. A command that can print a secret runs without output streaming, so nothing reaches the runner or the log file. Remote scripts run with `umask 077`.
- Idempotent and safe to re-run. Git commits only for listed paths and only when something changed.
- A stage has no own header (`pipeline.Run` wraps it); sub-steps use `runner.Run` / `runner.Parallel`; parallel steps return values and never write `env.State`.
- A fact a later run needs is a field on `pipeline.State`, added by the stage that first writes it. A new flag is a field on `pipeline.Inputs` and a line in `cmd/nixos-installer/flags.go`.
- `go build ./... && go vet ./... && go test -race ./...` (in `nix develop`) green after every stage.

## End-to-end check

`nix develop -c go run ./cmd/nixos-installer --config ../nixos-config --host <h> --target <ip>` against a target booted from the NixOS ISO; run it a second time and confirm nothing is redone and no new commits appear in nix-secrets or config. A throwaway VM host in config (same CPU architecture as the Mac) avoids wiping atlas. Final acceptance: fresh install, `secrets` run after adding and after removing a `home/<u>/<host>.nix`, and a reinstall of the same host.
