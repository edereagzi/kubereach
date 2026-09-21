#!/bin/sh
# Renders the Homebrew cask, Scoop manifest and winget manifests for one release.
# Usage: build/release/render.sh <version> <dist dir with release assets> <out dir>
set -eu

VERSION=$1
dist=$2
out=$3
src=$(dirname "$0")

sha() { sha256sum "$dist/$1" | cut -d' ' -f1; }
SHA256_DARWIN=$(sha kubereach_darwin_universal.zip)
SHA256_WINDOWS_ZIP=$(sha kubereach_windows_amd64.zip)
SHA256_WINDOWS_INSTALLER=$(sha kubereach_windows_amd64_installer.exe | tr a-f A-F)
export VERSION SHA256_DARWIN SHA256_WINDOWS_ZIP SHA256_WINDOWS_INSTALLER
vars='$VERSION $SHA256_DARWIN $SHA256_WINDOWS_ZIP $SHA256_WINDOWS_INSTALLER'

mkdir -p "$out/winget"
envsubst "$vars" < "$src/kubereach.rb" > "$out/kubereach.rb"
envsubst "$vars" < "$src/kubereach.json" > "$out/kubereach.json"
for f in "$src"/winget/*.yaml; do
  envsubst "$vars" < "$f" > "$out/winget/$(basename "$f")"
done
