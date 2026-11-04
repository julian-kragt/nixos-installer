Check three assumptions the roadmap depends on.

1. Run `go build ./... && go vet ./... && go test -race ./...` in `nix develop` and report the result.
2. On atlas (already installed), with `ssh -A root@<atlas-ip>`, check the following and write the outcome into a new "Verified assumptions" section of `docs/ROADMAP.md`:
   - with the forwarded agent as root: `ssh-add -l`, `git ls-remote git@github.com:JulianKragt/nix-secrets.git`, and `nix flake metadata 'git+ssh://git@github.com/JulianKragt/nix-secrets.git'` all work;
   - `SOPS_AGE_SSH_PRIVATE_KEY_FILE=/etc/ssh/ssh_host_ed25519_key sops -d <file>` works with the sops version in nixpkgs; if not, whether `SOPS_AGE_KEY="$(ssh-to-age -private-key -i /etc/ssh/ssh_host_ed25519_key)"` does. nix-secrets is a setup only, so use a throwaway file encrypted to atlas' host key rather than relying on `hosts/atlas.yaml`;
   - whether git over ssh to GitHub runs without a host-key prompt.

Ask me for the atlas IP. Do not change anything on atlas.
