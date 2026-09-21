#!/bin/sh
# Installs the latest Kubereach release on macOS or Linux.
# Usage: curl -fsSL https://raw.githubusercontent.com/edereagzi/kubereach/main/install.sh | sh
set -eu

base="https://github.com/edereagzi/kubereach/releases/latest/download"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

case "$(uname -s)" in
Darwin)
  curl -fsSL "$base/kubereach_darwin_universal.zip" -o "$tmp/kubereach.zip"
  ditto -x -k "$tmp/kubereach.zip" "$tmp"
  dest=/Applications
  [ -w "$dest" ] || dest="$HOME/Applications"
  mkdir -p "$dest"
  rm -rf "$dest/Kubereach.app"
  mv "$tmp/Kubereach.app" "$dest/"
  echo "Installed $dest/Kubereach.app"
  ;;
Linux)
  [ "$(uname -m)" = x86_64 ] || { echo "Unsupported architecture: $(uname -m)" >&2; exit 1; }
  bin="$HOME/.local/bin"
  share="$HOME/.local/share"
  mkdir -p "$bin" "$share/applications" "$share/icons"
  curl -fsSL "$base/kubereach_linux_amd64.tar.gz" | tar -xzf - -C "$tmp"
  mv "$tmp/kubereach" "$bin/kubereach"
  mv "$tmp/kubereach.png" "$share/icons/kubereach.png"
  sed "s|^Exec=.*|Exec=$bin/kubereach|" "$tmp/kubereach.desktop" > "$share/applications/kubereach.desktop"
  echo "Installed $bin/kubereach"
  case ":$PATH:" in *":$bin:"*) ;; *) echo "Add $bin to your PATH to run kubereach from a terminal" ;; esac
  ;;
*)
  echo "Unsupported OS: $(uname -s). On Windows run: irm https://raw.githubusercontent.com/edereagzi/kubereach/main/install.ps1 | iex" >&2
  exit 1
  ;;
esac
