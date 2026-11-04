# The signer's tool. It runs on the signer's own machine, never on the desk, and the private key never leaves that machine.
# Usage: sign.sh <private-key-file> <out-folder> <name> key=value ...
# It writes <out-folder>/<name>.msg and <out-folder>/<name>.msg.sig. Send both files to the inbox of the desk
# (a mail attachment, a form, a file copy). The desk checks the signature and does not care how the files came.
# Example: sign.sh ~/.ssh/id_ed25519 . s1 type=signoff req=REQ-1 doc=msa hash=<hash> role=legal by=lee decision=approve reason=ok
set -eu
[ $# -ge 4 ] || { echo "usage: sign.sh <key> <out> <name> key=value ..." >&2; exit 2; }
key="$1"; out="$2"; name="$3"; shift 3
: > "$out/$name.msg"
for kv in "$@"; do printf '%s\n' "$kv" >> "$out/$name.msg"; done
ssh-keygen -Y sign -q -f "$key" -n beehave-signoff "$out/$name.msg"
