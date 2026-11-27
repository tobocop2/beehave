#!/bin/sh
# The stand-in for curl in the approval scripts. It stands for the notifier. The program holds the work folder in place of @WORK@.
# It logs its arguments to curl.log. A POST to /messages adds a line "<idempotency key> <people>" to notices.log.
# The file resp/_messages.fail makes the call fail with exit 22.
url=
key=
body=
prev=
for arg in "$@"; do
  case "$prev" in -d) body="$arg" ;; esac
  case "$arg" in
    http*) url="$arg" ;;
    Idempotency-Key:*) key="${arg#Idempotency-Key: }" ;;
  esac
  prev="$arg"
done
printf '%s\n' "$*" >> "@WORK@/curl.log"
path=$(printf '%s' "$url" | sed -e 's#^[a-z]*://[^/]*##' -e 's#?.*##')
name=$(printf '%s' "$path" | sed -e 's#/#_#g')
if [ -e "@WORK@/resp/$name.fail" ]; then
  echo "boom $path" >&2
  exit 22
fi
case "$path" in
  /messages)
    people=$(printf '%s' "$body" | sed -n 's/.*"to":\[\([^]]*\)\].*/\1/p' | tr -d '"')
    printf '%s %s\n' "$key" "$people" >> "@WORK@/notices.log" ;;
esac
exit 0
