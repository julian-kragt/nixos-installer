# Roadmap: the Go installer replaces `install-host.sh`

Companion to [CONTEXT.md](CONTEXT.md). Each row in "Order of work" has a prompt in [prompts/](prompts/) that you can give to a fresh chat to get a specific plan for that stage.

## Goal

Replace `config/scripts/install-host.sh` and `just install | enroll-sops | deploy-remote` with the Go installer. Spec for "what": the stage table in `docs/CONTEXT.md` §5 (the Pipeline doc itself is not in any repo). Reference for "how": the bash script.

## State of things (2026-10-13)

`docs/CONTEXT.md` §1, §4 and §7 describe the installer as it is now. §3 (nix-secrets) and §6 (open decisions) are stale; the points below and "Decisions" replace them.

- **installer**: stages are plain functions listed in `stages.All`; `pipeline.Machine` runs a command on the provider or, through the system `ssh`, on the target; `pipeline.State` (`Host`, `Disk`, `Completed`) is one JSON file per host. Stages 0.0 and 0.1 exist (`Machine` has `Quiet()`, `Loopback()`, split stdout/stderr; `internal/nix`, `internal/git` exist; 0.2 re-asks the passphrase with `askAndSendPassphrase` when `/tmp/disko-password` is gone). `pipeline.SSH` and `BootstrapKey` (`bootstrap.go`) still skip host key checking (a TODO in each). Build and tests are green.
- **nix-secrets**: already on `hosts/<host>.yaml` + `host-users/<host>-<user>.yaml`; `.sops.yaml` deduped with anchored regexes; `shared.yaml` and `users/` are gone. No entries for `broadway` or `media`. `hosts/workhorse.yaml` is 0 bytes. `templates/user.yaml` = `hashedPassword` only.
- **config**: reads `host-users/…`. User SSH keys no longer come from secrets, so the only per-user secret is `hashedPassword`, decrypted by system sops-nix with the host key. The config's `installer.<host>` adapter and `<host>-bootstrap` systems are gone: its `bootstrap/` mapper feeds the separate nixos-bootstrap flake, and the installer reads `<bootstrap>#installerData.<host>` (`internal/nix`, with the config passed by `--override-input config`). Still open: B3 below. `install-host.sh` and the justfile still read the removed `provisionSpec`, so **the bash path no longer runs**.
- **provider (workhorse)**: has `go` (`~/sdk/go1.27.1/bin/go`), `nix`, `git`, `ssh`; no `sops`, `age`, `ssh-to-age` in PATH.

## Decisions (settled)

| Topic | Decision |
|---|---|
| §6.2 who decrypts | Host key decrypts `host-users/*`; no operator key on the target. Legacy Phase 7.5 is not ported. |
| §6.3 target git auth | SSH agent forwarding (`ssh -A`), opt-in per command on `pipeline.Machine`. Nothing is written to the target; the `git+ssh://` flake URL stays. |
| §6.4 lock + rebuild | Provider updates, commits and pushes `flake.lock`; target runs `nixos-rebuild switch --flake git+ssh://…/nixos-config?rev=<ConfigRev>#<host>`. Only committed + pushed config is installed. |
| §6.5 user recipients | `.sops.yaml` anchors are the source of truth. `hosts/<h>.yaml` = `*host_<h>` + `*operator`. `host-users/<h>-<u>.yaml` = those two + `*user_<u>_<h>`, the user's age key on that host. No other host's key is ever added. Missing `&operator` → fail closed. |
| §6.6 reinstall | On `&host_<h>` or `&user_<u>_<h>` mismatch: show old/new, typed confirmation, replace the anchors, rekey the host's files from the provider with the operator key. |
| Keys | One key per role, none shared across machines. Host: SSH host key, generated on the target by NixOS and used through `sops.age.sshKeyPaths`; its recipient is converted from the public key (1.1). User on a host: standalone age key created on the target (1.3), never derived from an SSH key. Operator: standalone age key on the provider, never copied to a target. |
| §6.7 shared file | Moot: `shared.yaml` no longer exists. |
| Disk | The device is set by hand in the host's disko layout in the config, committed and pushed. Stage 0.1 reads the devices from `<bootstrap>#nixosConfigurations.<host>.config.disko.devices.disk`, requires the chosen disk (after `readlink -f` on the target) to be one of them and fails with what to change. No `install.json`. |

## Order of work

There is no separate plumbing milestone: shared plumbing is built by the first stage that needs it.

| # | Stage | Runs on | Needs | Introduces shared plumbing | Writes State | Prompt |
|---|---|---|---|---|---|---|
| – | Prep | provider | – | – | – | [00-prep.md](prompts/00-prep.md) |
| – | Config prerequisites | `~/src/config` | – | – | – | [config-prerequisites.md](prompts/config-prerequisites.md) |
| 0.1 | Provider preparation | provider (+target checks) | 0.0 | `Machine` stdout/stderr split and `Quiet()`, `pipeline.Loopback()`, disk resolution (moved from 0.0), `internal/nix`, `internal/git` | – | [0.1-provider-preparation.md](prompts/0.1-provider-preparation.md) |
| 0.2 | Base install | provider → target | 0.1 | host key pinning (`pipeline.SSH` and `BootstrapKey`), `Machine.SSHArgs()`, `pipeline.NewEnv`, `Inputs.StateDir` | `HostKeyFingerprint` | [0.2-base-install.md](prompts/0.2-base-install.md) |
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
| – | Retire bash, package | both repos | all | – | – | [retire-bash.md](prompts/retire-bash.md) |

## Prerequisites outside this repo

- **B2**: replaced by nixos-bootstrap, whose system has the packages, nix features and github.com known host.
- **B3 (before 3.2)** config: remove `users.users.root.initialPassword = "test"`; remove the `sopsUserAgeKeys` pending-key script; github.com known host in the full config.
- **nix-secrets**: fix or delete the empty `hosts/workhorse.yaml`; `&user_media` or rely on the primary-user fallback before installing broadway.

## Plumbing design

Direction from the 2026-10-13 structure review of the code against this roadmap. Nothing here is built up front: the stage named in each line builds it, as in "Order of work". The points exist so that stages 0.1 and 0.2 do not each decide them again.

| Area | Design | Built by |
|---|---|---|
| `Machine` output | Stdout and stderr are captured separately. `Run` returns stdout only; stderr streams to the runner unless quiet; the error text comes from the stderr tail. (Today both streams are one buffer, so `nix eval --json` breaks on a `warning:` line and a quiet command would leak its output into the error.) | 0.1 |
| `Machine` shape | A struct with fields (address, known-hosts path, flags), not a raw argv. Copy-returning modifiers instead of more `Run*` methods: `Quiet()`, `WithAgent()`, `Interactive()`; `SSHArgs()` for tools that spawn ssh themselves. Remote commands run with `umask 077` by default, so that rule does not depend on each stage. | `Quiet` 0.1, `SSHArgs` 0.2, `WithAgent` 2.0, `Interactive` 2.3 |
| Host key | Pins are keyed by host name (`HostKeyAlias=<host>`), in a per-host known_hosts under the state dir, not by IP (the target IP is a per-run input). The ISO key is pinned at first contact in 0.0 and its fingerprint shown; the installed system's key replaces it in 0.2. Applies to `pipeline.SSH` and to `BootstrapKey`. | 0.2 |
| `ControlMaster` | Not used until commands are measured slow. OpenSSH applies the master's `ForwardAgent` to multiplexed sessions (verify), which conflicts with per-command `-A`; if used, put the forwarding mode in `ControlPath`, and keep it short (macOS socket paths: 104 bytes). | – |
| Env wiring | `pipeline.NewEnv(inputs, state)` builds Local and Remote (pinned or not, from State); `Inputs.StateDir` with `--state-dir`. `main.go` builds `Remote` from the IP alone today. | 0.2 |
| Disk | Resolved in 0.1, not 0.0, and compared after `readlink -f` so by-id paths work. Stage 0.0 only checks the environment, and its root-password bootstrap runs only while 0.2 is not in `Completed`. | 0.1 |
| Users | No `State.Users`. A stage that needs the user list reads `users` from `<bootstrap>#installerData.<host>` itself (`nix.EvalHostInfo`): every normal user on the host, each with `uid` and `home`. There is no `secrets` flag, because the contract only takes standard NixOS options. 2.2 authors a host-user file for every user; an unused file does no harm. 3.2 checks `home-manager-<u>.service` for all users, and the password file only for users whose full config declares `sops.secrets."passwords/<u>"`, against the config at `ConfigRev`; 4.1 uses the same function. | 0.1 (`internal/nix`) |
| Phase | No `State.Marker`. `Completed` is the only record of progress. `status` derives the phase: 1.2 not in `Completed` = installing; 1.2 in, 3.3 not = awaiting the secrets phase; 3.3 in = converged. For that to stay true when the secrets phase runs again, a forced re-run (`secrets`, `--from`, `--only`) first removes the IDs it will run from `Completed` and saves, then runs. A plain `install` after an interrupted `secrets` run then resumes it. | 4.2 |
| Re-runs (4.2) | `pipeline.Run` takes run options (from / only / force) and the "secrets" group (1.1–4.1) is marked; `Completed` gets no duplicate IDs; `install --reinstall` (typed confirmation, archives the state file) is the way to run 0.1–1.1 again after a full install (§6.6, final acceptance test). | 4.2 |
| Tests | `pipeline.Loopback()` is an exported Machine whose "ssh" is the local shell. Target paths a stage uses are variables, so idempotency tests ("two runs, one commit") run against temp dirs. | 0.1 |
| Cleanup | A cleanup that must run after Ctrl+C uses `context.WithoutCancel(ctx)`; a cancelled `ctx` fails every `Machine.Run` at once. | first user (2.3) |

## Rules for every stage

- CONTEXT §4 "Adding a stage" and "Conventions" apply: a stage is a plain function in `internal/stages/NN_name.go` plus one entry in `stages.All`; no interface without two implementations; no new package unless a second stage needs the code; `internal/stages` holds stage files only.
- Secrets go through `RunStdin`, never into args. A command that can print a secret runs without output streaming, so nothing reaches the runner or the log file. Remote scripts run with `umask 077`.
- Idempotent and safe to re-run. Git commits only for listed paths and only when something changed.
- A stage has no own header (`pipeline.Run` wraps it); sub-steps use `runner.Run` / `runner.Parallel`; parallel steps return values and never write `env.State`.
- A fact a later run needs is a field on `pipeline.State`, added by the stage that first writes it. A new flag is a field on `pipeline.Inputs` and a line in `cmd/nixos-installer/flags.go`.
- `go build ./... && go vet ./... && go test -race ./...` green after every stage.

## End-to-end check

`go run ./cmd/nixos-installer --config ../config --host <h> --target <ip>` against a target booted from the NixOS ISO; run it a second time and confirm nothing is redone and no new commits appear in nix-secrets or config. A throwaway VM host in config (same CPU architecture as the Mac) avoids wiping atlas. Final acceptance: fresh install, `secrets` run after adding and after removing a `home/<u>/<host>.nix`, and a reinstall of the same host.
