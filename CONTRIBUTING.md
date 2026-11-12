# Contributing

Issues and pull requests are welcome. For anything bigger than a fix, open an issue first, so we can agree on the
approach before you write it.

## Development environment

Everything comes from the flake's dev shell: Go (the version `go.mod` requires), gopls, staticcheck, and the tools the
tests use (`git`, `ssh`, `sops`, `age`, `mkpasswd`, `vim`).

```sh
nix develop
```

Or run one command in it, as below.

## Build and test

```sh
nix develop -c go build ./...
nix develop -c go vet ./...
nix develop -c staticcheck ./...
nix develop -c go test -race ./...
nix flake check                      # builds both packages and runs the tests, as CI does
```

The tests drive real git repositories in temp dirs, encrypt with sops and a throwaway age key, and stand in for the
target with `machine.Loopback()`, whose "ssh" is the local shell. They need no network and no target.

Run the installer from the source tree with:

```sh
nix develop -c go run ./cmd/nixos-installer --config ~/nixos-config --secrets ~/nix-secrets --host myhost --target 192.0.2.10
```

Without `NIXOS_INSTALLER_CONSOLE` set, it builds the console helper for the target with `go build` and caches it.

If you change dependencies, update `vendorHash` in `flake.nix`: set it to `pkgs.lib.fakeHash`, run `nix build` and copy
the hash it reports.

## Testing on a real target

The unit tests can't cover an install. For a change to a stage, run a full install against a machine you can wipe,
booted from the NixOS ISO, and then run it a second time: the second run must do nothing new and make no commits. Say in
the pull request what you ran it against.

## Code

[docs/architecture.md](docs/architecture.md) explains the layout and the rules. In short:

- A stage file only orchestrates; logic goes in a package under `internal/`, a command's wrapper in
  `internal/commands/<command>`.
- Stages are idempotent. Commit only listed paths, and only when something changed.
- Secrets go on stdin, never in arguments, and never reach the terminal or log on the provider.
- No interface without two implementations.
- `gofmt`, `go vet` and `staticcheck` are clean; every exported identifier has a doc comment.
- Format Nix with `nix fmt`.

## Pull requests

- One change per pull request, with tests for new behaviour and for the bug a fix fixes.
- Update the docs in the same pull request when behaviour, flags, or what the installer expects of a config change.
- Commit messages: a short subject in the imperative ("Check the pinned key after the switch"), and a body that says
  why when it isn't obvious.

By contributing, you agree that your contribution is licensed under the [MIT License](LICENSE).
