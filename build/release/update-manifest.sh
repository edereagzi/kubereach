#!/bin/sh
# Writes update.json, the Wails update manifest that installed releases read to update themselves. Each file it
# offers is signed with Ed25519 over its SHA-256 digest, which the updater checks against the key built into Kubereach.
# Usage: UPDATE_SIGNING_KEY=<private key, PEM> build/release/update-manifest.sh <version> <dist dir> <notes file> <out file>
set -eu

version=$1
dist=$2
notes=$3
out=$4
: "${UPDATE_SIGNING_KEY:?is not set}"
base=https://github.com/edereagzi/kubereach/releases/download/v$version

tmp=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/update-manifest.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
(umask 077 && printf '%s\n' "$UPDATE_SIGNING_KEY" > "$tmp/key.pem")

# artifact <platform> <arch> <file> adds the file's entry; an empty arch matches every one.
artifact() {
  openssl dgst -sha256 -binary -out "$tmp/digest" "$dist/$3"
  openssl pkeyutl -sign -rawin -inkey "$tmp/key.pem" -in "$tmp/digest" -out "$tmp/signature"
  jq -n --arg platform "$1" --arg arch "$2" --arg filename "$3" --arg url "$base/$3" \
    --argjson size "$(wc -c < "$dist/$3")" \
    --arg digest "$(openssl base64 -A -in "$tmp/digest")" \
    --arg signature "$(openssl base64 -A -in "$tmp/signature")" \
    '{$platform, $arch, $url, $filename, $size, digestAlgo: "sha256", $digest, signatureAlgo: "ed25519", $signature}' \
    >> "$tmp/artifacts"
}
artifact darwin "" kubereach_darwin_universal.zip
artifact linux amd64 kubereach_linux_amd64
artifact linux arm64 kubereach_linux_arm64
artifact windows amd64 kubereach_windows_amd64.zip

jq -s --arg version "$version" --rawfile notes "$notes" --arg publishedAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{schemaVersion: 1, $version, name: "Kubereach \($version)", $notes, $publishedAt, artifacts: .}' \
  "$tmp/artifacts" > "$out"
