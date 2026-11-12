# Architecture

How the code is organised and the rules it follows. Read this before changing a stage or adding one.

## Layout

```
cmd/nixos-installer/          main: flags, the state dir and log file, asking for missing inputs, running the pipeline
cmd/nixos-installer-console/  the console helper, run on the target on tty9: the LUKS passphrase and the secret files
internal/pipeline/            the engine: Inputs, State, Env, Stage, Run (the resume loop)
internal/stages/              one file per stage, which orchestrates; stages.go has All, the ordered list
internal/machine/             running commands here or on the target over ssh; host key pinning; key login
internal/runner/              the live task tree, the log file, questions to the operator
internal/secrets/             the secrets repository: file layout, the .sops.yaml editor, templates, checks
internal/console/             finding, installing and running the console helper on the target
internal/authoring/           writing one secret file from its template (vim, passwords, mkpasswd, sops); runs in the helper
internal/commands/<command>/  one package per command the stages run, each a type bound to a machine.Machine:
                              nix, git, luks, nixosanywhere, nixosrebuild, systemd, hostidentity,
                              hardwareconfig, sops, mkpasswd, vim
```

## How a run goes

1. `main` parses the flags into `pipeline.Inputs`.
2. `resolveInputs` asks for a missing host and loads `<state dir>/<host>.json`. Then it shows how to prepare the
   target, only when the state records no base install, and asks for a missing target.
3. `pipeline.Run(ctx, env, stages.All)` runs the stages in list order.
   - A stage whose ID is in `State.Completed` is skipped, unless its `Outdated` check gives a reason. Then that stage
     and every stage after it are removed from `Completed` and the state is saved before anything runs, so a run that
     stops on the way still runs them next time.
   - Every other stage runs, and is recorded and saved when it succeeds.
   - A stage with `Always: true` (0.0) runs every time and is never recorded.

## Three kinds of data

- **`pipeline.Inputs`**: what the operator chose for this run, from the flags and answers. Never persisted.
- **`pipeline.State`**: what must survive a restart: `Host`, `Completed`, `HostRecipient`, `UserRecipients`,
  `SecretsCommit`, `ConfigRev`. One JSON file per host, saved after every stage. No user list: stages read the users
  from `installerData.<host>` on every run, so a change in the config is never hidden by a stale copy.
- **`machine.Machine`**: where commands run. `env.Local` is the provider; `env.Remote` is the target, reached as
  `ssh … -l root <target>` with the system ssh, under `umask 077`.
  - `Run` returns stdout and streams stderr to the runner. The error text comes from stderr's tail.
  - `RunStdin` passes a secret on stdin.
  - `Quiet()` keeps a command's output away from the runner, the log and errors.
  - `WithAgent()` forwards the agent for one command.
  - `machine.Loopback()` is a Machine for tests, whose "ssh" is the local shell.

## Adding a stage

1. Create `internal/stages/<id>_<name>.go`, with the stage ID's dot as `_` (`2_3_template_authoring.go`). It holds
   `func <name>(ctx context.Context, env *pipeline.Env) error`.
2. Add one entry to `stages.All`, at its place in the order.
3. A fact a later run needs becomes a field on `pipeline.State`. A new flag becomes a field on `pipeline.Inputs` and a
   line in `cmd/nixos-installer/flags.go`.
4. If the stage must run again when what it recorded stops holding, give it an `Outdated` check. A stage that finds a
   problem a few stages back reopens with `reopenFrom`, which removes that stage and every stage after it, never a
   hand-picked list.

## Conventions

- **A stage file orchestrates.** It is the stage function plus flat glue: straight-line calls and messages. Parsing,
  polling, retry loops and protocol details go in a package under `internal/`, even when one stage uses them. A command
  (`nix eval`, `git`, `sops`) gets a package under `internal/commands/` with a type bound to a `machine.Machine`
  (`nix.Config`, `git.Repo`). `internal/stages` holds stage files only.
- **Stages are plain functions.** Everything they need comes in through `ctx` and `*pipeline.Env`. Sub-steps use
  `runner.Run` / `runner.Parallel` / `runner.RunSteps`. Parallel steps return values and never write `env.State`. A
  stage has no header of its own; `pipeline.Run` wraps it.
- **Idempotent.** Running a stage twice does nothing new the second time. Git commits only listed paths, and only when
  something changed.
- **Secrets.** Through `RunStdin`, never in arguments. A command that can print a secret runs `Quiet()`. Nothing secret
  is typed on the provider.
- **Cleanup after Ctrl+C** uses `context.WithoutCancel(ctx)`: a cancelled `ctx` makes every `Machine.Run` fail at once.
- **No interface without two implementations.**
- **State holds only what must survive a restart.**

## Design decisions

- **Secrets are typed at the target's console.** The provider only handles public keys and ciphertext, so a
  compromised provider exposes nothing. The console helper is cross-built for `x86_64-linux` and `aarch64-linux` by the
  flake, whose wrapper points `NIXOS_INSTALLER_CONSOLE` at it. Without that variable (`go run`), it is built with
  `go build` and cached.
- **One age key per role, no operator key.** See [security.md](security.md).
- **The installer owns the hardware config.** A host needs no file before its first install. 0.1 generates a missing
  one, and 0.2 regenerates, commits and pushes it. 0.1 refuses a config branch that is ahead of or behind its upstream,
  so that push can't be rejected after the wipe or publish other commits.
- **The disks come from the config.** The installer never picks a disk: 0.1 reads the devices from
  `installerData.<host>.disks`, requires each to be a block device on the target, and names them in the typed
  confirmation.
- **Only from the ISO.** The ISO's `VARIANT_ID=installer` makes nixos-anywhere skip kexec, so `/tmp/disko-password`
  from 0.1 survives and `--disk-encryption-keys` isn't needed.
- **The target builds the pushed config.** The provider locks, commits and pushes `flake.lock`. The target runs
  `nixos-rebuild switch --flake git+ssh://…?rev=<ConfigRev>#<host>` with the provider's agent forwarded for that
  command only. Nothing is written to the target, and the target never holds a repository key.
- **`.sops.yaml` is edited structurally** (`gopkg.in/yaml.v3` nodes, keeping anchors, aliases and comments). Its
  anchors are the source of truth for recipients. A missing rule goes before the first rule that would match its file,
  because sops uses the first match.
- **2.5 checks the pushed commit, not the clone,** before 3.1 locks it, so a bad commit is never deployed.
- **No `ControlMaster`.** OpenSSH applies the master's `ForwardAgent` to multiplexed sessions, which conflicts with
  per-command forwarding. Revisit only if commands are measured to be slow.

## Planned

- **Subcommands.** `install` (the default), `secrets` (1.1 to 3.3 only, after adding or removing a user) and
  `status`. `status` would derive the phase from `Completed`: 1.1 not done means installing, 3.3 not done means the
  secrets phase is pending, otherwise converged.
- **`--from` and `--only`**, to run stages again on purpose. A forced re-run first removes the stages it will run from
  `Completed` and saves, so a plain run after an interrupted one resumes it.
