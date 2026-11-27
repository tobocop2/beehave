#!/bin/sh
# Install the tools of the contract tests against a real bd (dolt and bd 1.3.2) into a folder, after checking each archive against a pinned sha256.
# `make contract-tools-install` runs it, so CI and the build host get the same two binaries. CI sets BEEHEST_BD_132 to the bd in the folder.
# A new version needs a new sha256: take it from the digest of the asset in the release (`gh release view <tag> -R <repo> --json assets`).
#
# Usage: install-contract-tools.sh <folder>
set -eu

folder=${1:?usage: install-contract-tools.sh <folder>}

dolt_version=1.82.4
dolt_sha256=94f528d2873ab1deaf245f4a89cd06b48be9398af9eb87df0d6808bb87fc022a
bd_version=1.3.2
bd_sha256=c6ea3281f6ff82628351b2d0967c9eb85862c8a0ec0ebc24e63e645c8b02462e

if [ "$(uname -s)" != Linux ] || { [ "$(uname -m)" != x86_64 ] && [ "$(uname -m)" != amd64 ]; }; then
	echo "install-contract-tools.sh: the pinned archives are for linux amd64 only" >&2
	exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# fetch <url> <sha256> <binary name>: download one archive, check it, and install the binary of that name.
fetch() {
	archive=$work/$(basename "$1")
	curl -fsSL --retry 3 --retry-all-errors --retry-delay 2 -o "$archive" "$1"

	if command -v sha256sum >/dev/null 2>&1; then
		got=$(sha256sum "$archive" | awk '{ print $1 }')
	else
		got=$(shasum -a 256 "$archive" | awk '{ print $1 }')
	fi

	if [ "$got" != "$2" ]; then
		echo "install-contract-tools.sh: the checksum of $(basename "$1") does not match the pin" >&2
		exit 1
	fi

	mkdir "$work/$3"
	tar -xzf "$archive" -C "$work/$3"
	binary=$(find "$work/$3" -type f -name "$3" | head -n 1)
	[ -n "$binary" ] || { echo "install-contract-tools.sh: no $3 in $(basename "$1")" >&2; exit 1; }
	mkdir -p "$folder"
	install -m 0755 "$binary" "$folder/$3"
}

fetch "https://github.com/dolthub/dolt/releases/download/v$dolt_version/dolt-linux-amd64.tar.gz" "$dolt_sha256" dolt
fetch "https://github.com/gastownhall/beads/releases/download/v$bd_version/beads_${bd_version}_linux_amd64.tar.gz" "$bd_sha256" bd

"$folder/dolt" version
"$folder/bd" --version
