{
  description = "nixos-installer: provisions a NixOS host from the config flake";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (
        pkgs:
        let
          # go.mod requires Go 1.27; the default `go` in nixpkgs may lag behind.
          buildGoModule = pkgs.buildGoModule.override { go = pkgs.go_1_27; };
        in
        {
          default = self.packages.${pkgs.stdenv.hostPlatform.system}.nixos-installer;

          nixos-installer = buildGoModule {
            pname = "nixos-installer";
            version = self.shortRev or self.dirtyShortRev or "dev";
            src = self;

            vendorHash = "sha256-dVtheik9hgc8Ig52xOR7v1ujtqj2bzV1+gl5KmKBB98=";

            nativeBuildInputs = [ pkgs.makeWrapper ];

            # ssh and git are shelled out to at runtime. nix itself is taken from
            # the operator's PATH so it matches their daemon.
            postInstall = ''
              wrapProgram $out/bin/nixos-installer \
                --suffix PATH : ${
                  pkgs.lib.makeBinPath [
                    pkgs.openssh
                    pkgs.git
                  ]
                }
            '';

            meta.mainProgram = "nixos-installer";
          };
        }
      );

      checks = forAllSystems (pkgs: {
        inherit (self.packages.${pkgs.stdenv.hostPlatform.system}) nixos-installer;
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.go_1_27
            pkgs.gopls
            pkgs.gotools
            pkgs.openssh
            pkgs.git
          ];
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
