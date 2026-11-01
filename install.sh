#!/bin/sh
# Installs t2c from GitHub Releases, then adds the shortcut to your shell.
set -eu

REPO="${T2C_REPO:-mr-ahsan-ghaffar/text2command}"
BASE_URL="${T2C_BASE_URL:-https://github.com/$REPO/releases/latest/download}"

fail() {
  echo "t2c install: $1" >&2
  exit 1
}

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "t2c supports macOS and Linux, not $(uname -s)." ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "t2c doesn't have a build for $(uname -m)." ;;
esac

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
else
  fail "curl or wget is required."
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
file="t2c-$os-$arch"
echo "Downloading $file..."
fetch "$BASE_URL/$file" "$tmp/$file" || fail "couldn't download $BASE_URL/$file"
fetch "$BASE_URL/checksums.txt" "$tmp/checksums.txt" || fail "couldn't download the checksums"

want=$(awk -v f="$file" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "$file isn't listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$file" | awk '{ print $1 }')
else
  got=$(shasum -a 256 "$tmp/$file" | awk '{ print $1 }')
fi
[ "$want" = "$got" ] || fail "the download's checksum doesn't match, so it wasn't installed."

if [ -n "${T2C_INSTALL_DIR:-}" ]; then
  dir=$T2C_INSTALL_DIR
elif [ "$(id -u)" = 0 ]; then
  dir=/usr/local/bin
else
  dir="$HOME/.local/bin"
fi
mkdir -p "$dir"
chmod 755 "$tmp/$file"
mv "$tmp/$file" "$dir/t2c"
echo "Installed t2c to $dir/t2c"

"$dir/t2c" setup || echo "Add the shortcut later by running: t2c setup"

echo
echo "Next: open a new terminal and run 't2c login' to connect an AI model."
echo "Then type what you want and press Ctrl+G."
echo "To remove everything later, run: t2c uninstall"
