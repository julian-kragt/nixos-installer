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
          version = self.shortRev or self.dirtyShortRev or "dev";
          vendorHash = "sha256-+BWPX4u+EBCABeyjszTew6WmKBfrRhhA5TnatJVkPTU=";

          # The console helper runs on the target, so it is built for Linux
          # from whatever system the installer runs on: pure Go, static, no
          # cross toolchain needed. go install puts a cross build in bin/linux_<arch>.
          consoleHelper =
            arch:
            buildGoModule {
              pname = "nixos-installer-console";
              inherit version vendorHash;
              src = self;
              subPackages = [ "cmd/nixos-installer-console" ];
              preBuild = ''
                export GOOS=linux GOARCH=${arch} CGO_ENABLED=0
              '';
              postInstall = ''
                if [ -d $out/bin/linux_${arch} ]; then
                  mv $out/bin/linux_${arch}/* $out/bin/
                  rmdir $out/bin/linux_${arch}
                fi
              '';
              # The tests ran for the installer; the host's tools cannot touch a Linux binary.
              doCheck = false;
              dontFixup = true;
            };

          # <system>/nixos-installer-console for each system a host can have.
          consoleHelpers = pkgs.linkFarm "nixos-installer-console" [
            {
              name = "x86_64-linux/nixos-installer-console";
              path = "${consoleHelper "amd64"}/bin/nixos-installer-console";
            }
            {
              name = "aarch64-linux/nixos-installer-console";
              path = "${consoleHelper "arm64"}/bin/nixos-installer-console";
            }
          ];
        in
        {
          default = self.packages.${pkgs.stdenv.hostPlatform.system}.nixos-installer;

          nixos-installer-console = consoleHelpers;

          nixos-installer = buildGoModule {
            pname = "nixos-installer";
            inherit version vendorHash;
            src = self;

            nativeBuildInputs = [ pkgs.makeWrapper ];

            # The tests drive real git repositories, encrypt with sops and a
            # throwaway age key, and run the target's probes (hostname) locally.
            nativeCheckInputs = [
              pkgs.git
              pkgs.inetutils
              pkgs.openssh
              pkgs.sops
              pkgs.mkpasswd
              pkgs.age
            ];

            # ssh and git are shelled out to at runtime; sops, mkpasswd and vim
            # run on the target, in the console helper. nix itself is taken from
            # the operator's PATH so it matches their daemon.
            postInstall = ''
              # Built for this system with the rest; the one for the target is in consoleHelpers.
              rm $out/bin/nixos-installer-console
              wrapProgram $out/bin/nixos-installer \
                --suffix PATH : ${
                  pkgs.lib.makeBinPath [
                    pkgs.openssh
                    pkgs.git
                  ]
                } \
                --set NIXOS_INSTALLER_CONSOLE ${consoleHelpers}
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
            pkgs.sops
            pkgs.mkpasswd
            pkgs.vim
            pkgs.age # tests encrypt with a throwaway key
          ];
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
