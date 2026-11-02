# Shared setup for the gateway and eval scripts. The desk folder is outside the repository and every worktree.
DESK_HOME="${DESK_HOME:-$HOME/.desk}"
. "$DESK_HOME/config"          # sets DESK_API, MAILDIR, CHAT_SPOOL, CHAT_API, MASK_SALT
VAULT="$DESK_HOME/vault.tsv"   # token <TAB> value, mode 600
umask 077

# The SHA-256 hash of stdin in hex. Linux has sha256sum, and macOS has shasum.
sha256() { if command -v sha256sum > /dev/null 2>&1; then sha256sum; else shasum -a 256; fi; }

die() { printf '%s\n' "$*" >&2; exit 2; }

# A call to the system of record. The credential is a header file, not an environment value or an argument.
api() { curl -sS --fail-with-body -H @"$DESK_HOME/credential.header" "$@"; }
