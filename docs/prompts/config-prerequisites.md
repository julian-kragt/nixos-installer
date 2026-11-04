The Go installer in `../nixos-installer` (see its `docs/ROADMAP.md`, "Prerequisites outside this repo") needs these changes in this flake. Plan what is still open and tell me which installer stage it unblocks.

- **B1**: done, and superseded: the config's `installer.<host>` adapter is gone. The config's `bootstrap/` entries feed nixos-bootstrap's `installerData.<host>`.
- **B2**: replaced by nixos-bootstrap, whose system already has these packages, nix features and the GitHub host key.
- **B3**: remove `users.users.root.initialPassword = "test"` from `hosts/nixos/atlas/default.nix`; remove the `sopsUserAgeKeys` pending-key activation script from `hosts/common/accounts/secrets.nix` (the operator key is no longer copied to hosts); add the GitHub known host to the full config as well.

`scripts/install-host.sh` and its just recipes are already gone. Verify with `nix flake check` or at least `nix build .#nixosConfigurations.atlas.config.system.build.toplevel --dry-run`.
