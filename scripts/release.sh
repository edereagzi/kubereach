#!/usr/bin/env bash
# Releases a version: CHANGELOG.md's Unreleased section becomes the version's,
# committed as "Kubereach X.Y.Z" and tagged vX.Y.Z on that commit; pushing the tag runs release.yml.
# Usage: scripts/release.sh 0.9.0
set -euo pipefail
cd "$(dirname "$0")/.."
version=${1:?usage: scripts/release.sh X.Y.Z}
tag=v$version

[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "$version is not X.Y.Z" >&2; exit 1; }
[[ $(git branch --show-current) == main ]] || { echo "not on main" >&2; exit 1; }
[[ -z $(git status --porcelain) ]] || { echo "working tree is not clean" >&2; exit 1; }
git fetch -q --tags origin main
[[ $(git rev-parse HEAD) == $(git rev-parse origin/main) ]] || { echo "main is not the same as origin/main" >&2; exit 1; }
! git rev-parse -q --verify "refs/tags/$tag" >/dev/null || { echo "$tag already exists" >&2; exit 1; }
scripts/changelog-section.sh Unreleased >/dev/null

# A new, empty Unreleased section goes above the released one.
awk -v rel="## [$version] - $(date +%F)" '
  !done && $0 == "## [Unreleased]" { print; print ""; print rel; done = 1; next } { print }
' CHANGELOG.md > CHANGELOG.md.tmp && mv CHANGELOG.md.tmp CHANGELOG.md
scripts/changelog-section.sh "$version"

read -rp "Release $tag with the notes above? [y/N] " ok
[[ $ok == y ]] || { git checkout -- CHANGELOG.md; exit 1; }
git commit -qm "Kubereach $version" CHANGELOG.md
git tag -a "$tag" -m "Kubereach $version"
git push --atomic origin main "$tag"
