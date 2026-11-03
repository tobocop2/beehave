#!/bin/sh
# The signer's side of the approval scripts. The program holds the work folder in place of @WORK@. It signs with the key keys/<name>
# (made by approvalskeys) and writes the two files into the inbox of the desk, as a person would send them.
#   signoff <name> <role> <decision> <req> <doc> [<hash>]    a sign-off; the hash is the hash of the document now if it is not given
#   signoff delegate <name> <role> <to> <seconds>             a delegation from <name> to <to> for <seconds> seconds
set -eu
work="@WORK@"
desk="$work/home/.approval"
sign="$work/ops/scripts/sign.sh"
n=$(date +%s)-$$
if [ "$1" = delegate ]; then
  sh "$sign" "$work/keys/$2" "$desk/inbox" "d-$n" type=delegate role="$3" by="$2" to="$4" until=$(( $(date +%s) + $5 )) reason=away
  exit 0
fi
hash="${6:-$(git hash-object "$desk/docs/$5.md")}"
sh "$sign" "$work/keys/$1" "$desk/inbox" "s-$n" type=signoff req="$4" doc="$5" hash="$hash" role="$2" by="$1" decision="$3" reason="$3 by $1"
