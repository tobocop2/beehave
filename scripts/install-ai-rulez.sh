#!/bin/sh
# Install one release of ai-rulez into a folder, after checking the archive against the checksums of the release.
# `make ai-rulez-install` runs it with AI_RULEZ_VERSION from the Makefile, so setup and CI get the version the gate expects.
#
# Usage: install-ai-rulez.sh <version, for example v5.9.2> <folder>
set -eu

version=${1:?usage: install-ai-rulez.sh <version> <folder>}
folder=${2:?usage: install-ai-rulez.sh <version> <folder>}
number=${version#v}

case $(uname -s) in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) echo "install-ai-rulez.sh: no ai-rulez release for $(uname -s)" >&2; exit 1 ;;
esac

case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) echo "install-ai-rulez.sh: no ai-rulez release for $(uname -m)" >&2; exit 1 ;;
esac

archive=ai-rulez_${number}_${os}_${arch}.tar.gz
base=https://github.com/Goldziher/ai-rulez/releases/download/$version
work=$(mktemp -d)

curl -fsSL --retry 3 -o "$work/$archive" "$base/$archive"
curl -fsSL --retry 3 -o "$work/checksums.txt" "$base/checksums.txt"

want=$(awk -v name="$archive" '$2 == name { print $1 }' "$work/checksums.txt")
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$work/$archive" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$work/$archive" | awk '{ print $1 }')
fi

if [ -z "$want" ] || [ "$want" != "$got" ]; then
	echo "install-ai-rulez.sh: the checksum of $archive does not match the release" >&2
	exit 1
fi

tar -xzf "$work/$archive" -C "$work" ai-rulez
mkdir -p "$folder"
install -m 0755 "$work/ai-rulez" "$folder/ai-rulez"
"$folder/ai-rulez" --version
