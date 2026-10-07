#!/usr/bin/env bash
# Prints the section of CHANGELOG.md for a version (e.g. 0.8.1), or fails when it has none.
set -euo pipefail
notes=$(awk -v head="## [$1]" '/^## /{p = index($0, head) == 1; next} p' CHANGELOG.md)
if ! grep -q '[^[:space:]]' <<<"$notes"; then
  echo "CHANGELOG.md has no section for $1" >&2
  exit 1
fi
printf '%s\n' "$notes"
