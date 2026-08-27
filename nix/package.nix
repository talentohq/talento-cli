{ lib, buildGoModule, version, commit, date, sourceRoot }:

buildGoModule {
  pname = "talento";
  inherit version;
  src = sourceRoot;
  vendorHash = null;
  subPackages = [ "cmd/talento" ];
  ldflags = [
    "-s" "-w"
    "-X github.com/talentohq/talento-cli/internal/buildinfo.Version=${version}"
    "-X github.com/talentohq/talento-cli/internal/buildinfo.Commit=${commit}"
    "-X github.com/talentohq/talento-cli/internal/buildinfo.Date=${date}"
    "-X github.com/talentohq/talento-cli/internal/buildinfo.Source=nix"
    "-X github.com/talentohq/talento-cli/internal/buildinfo.MetadataFingerprint=talento-release-metadata:${version}:${commit}:${date}:nix"
  ];
  meta = {
    description = "Native command-line client for TalentoHQ";
    homepage = "https://github.com/talentohq/talento-cli";
    changelog = "https://github.com/talentohq/talento-cli/releases/tag/v${version}";
    license = lib.licenses.mit;
    mainProgram = "talento";
  };
}
