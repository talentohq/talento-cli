# Development checkouts deliberately report a development version. Before a
# release tag is created, run scripts/stamp-nix-version.sh VERSION and commit
# this one-line stamp. That command also dates CHANGELOG.md for every version
# other than *-dev, and refuses a release with no notes. The release workflow
# refuses a tag that disagrees with this stamp or with that changelog section.
"1.0.9-dev"
