#!/bin/sh
# Install the pinned tools of the real-tools suite into a folder: gh 2.101.0 (sha256 checked), dolt and bd through install-contract-tools.sh, and claude 2.1.285 through npm ci.
# `make test-real-tools` runs it. A new gh needs a new sha256: take it from gh_<version>_checksums.txt of the release, and refresh internal/ghcontract/gh-<version>.txt.
# The gh-stack extension is pinned at v0.1.1 and installs only when GH_TOKEN is set, because gh needs the token to fetch an extension.
#
# Usage: install-real-tools.sh <folder>
set -eu

folder=${1:?usage: install-real-tools.sh <folder>}
here=$(cd "$(dirname "$0")" && pwd)

gh_version=2.101.0
gh_sha256=9bca2d1c16825f109907a23307628a2f0698fbf99662b73a5cf0b020293072b8
claude_version=2.1.285
stack_version=v0.1.1

if [ "$(uname -s)" != Linux ] || { [ "$(uname -m)" != x86_64 ] && [ "$(uname -m)" != amd64 ]; }; then
	echo "install-real-tools.sh: the pinned archives are for linux amd64 only" >&2
	exit 1
fi

work=$(mktemp -d)
mkdir -p "$folder"

archive=$work/gh.tar.gz
curl -fsSL --retry 3 --retry-all-errors --retry-delay 2 -o "$archive" "https://github.com/cli/cli/releases/download/v$gh_version/gh_${gh_version}_linux_amd64.tar.gz"

if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$archive" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$archive" | awk '{ print $1 }')
fi

if [ "$got" != "$gh_sha256" ]; then
	echo "install-real-tools.sh: the checksum of the gh archive does not match the pin" >&2
	exit 1
fi

tar -xzf "$archive" -C "$work"
install -m 0755 "$work/gh_${gh_version}_linux_amd64/bin/gh" "$folder/gh"

sh "$here/install-contract-tools.sh" "$folder"

# npm ci reads the integrity hash of claude from the lock file, so the install cannot drift from the pin. The install script of claude-code puts the native binary in place, so scripts run.
if command -v npm >/dev/null 2>&1; then
	npm ci --prefix "$here/real-tools" --no-audit --no-fund
	ln -sf "$here/real-tools/node_modules/.bin/claude" "$folder/claude"
else
	echo "install-real-tools.sh: npm is not on PATH, so claude is not installed and the claude rows skip" >&2
fi

if [ -n "${GH_TOKEN:-}" ]; then
	PATH="$folder:$PATH" gh extension install github/gh-stack --pin "$stack_version" || echo "install-real-tools.sh: gh-stack $stack_version did not install: the stack rows skip" >&2
fi

"$folder/gh" --version
if [ -x "$folder/claude" ]; then "$folder/claude" --version; fi
echo "claude $claude_version pinned in scripts/real-tools/package-lock.json"
