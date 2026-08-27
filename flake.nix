{
  description = "Native TalentoHQ CLI";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      eachSystem = nixpkgs.lib.genAttrs systems;
      version = import ./nix/version.nix;
      commit =
        if self ? rev then self.rev
        else if self ? dirtyRev then self.dirtyRev
        else "unknown";
      date = if self ? lastModified then toString self.lastModified else "unknown";
    in {
      packages = eachSystem (system:
        let pkgs = import nixpkgs { inherit system; };
        in {
          default = pkgs.callPackage ./nix/package.nix {
            inherit version commit date;
            sourceRoot = self;
          };
        });
      apps = eachSystem (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/talento";
        };
      });
    };
}
