#!/bin/sh
# The stand-in for curl in the desk scripts. The program holds the work folder in place of @WORK@.
# It logs its arguments to curl.log and the body that it is sent to curl.body. A read answers with the file resp/<path>, where each slash of the path is an underscore.
# A POST to /writes/<step> with an Idempotency-Key makes the write applied: the file resp/_writes_<key> then holds the status applied. The file resp/<step>.fail makes the write fail.
# A read of /writes/<step>-<key> with no file answers the status absent. The file resp/readback.fail makes every such read fail with exit 22,
# and its content is the body of the HTTP error, as curl --fail-with-body prints it.
# A POST to the chat address /send logs the body only. The file resp/_send.fail makes it fail.
url=
sends=0
post=0
header=
want=
for arg in "$@"; do
  if [ -n "$want" ]; then
    header="$arg"
    want=
    continue
  fi
  case "$arg" in
    http*) url="$arg" ;;
    @-) sends=1 ;;
    POST) post=1 ;;
    -H) want=1 ;;
  esac
done
printf '%s\n' "$*" >> "@WORK@/curl.log"
body=
if [ "$sends" -eq 1 ]; then
  body=$(cat)
  printf '%s\n' "$body" >> "@WORK@/curl.body"
fi
path=$(printf '%s' "$url" | sed -e 's#^[a-z]*://[^/]*##' -e 's#?.*##')
key=$(printf '%s' "$path" | sed -e 's#/#_#g')
if [ -e "@WORK@/resp/$key.fail" ]; then
  echo boom >&2
  exit 22
fi
if [ "$post" -eq 1 ]; then
  step=${path#/writes/}
  if [ "$step" != "$path" ]; then
    if [ -e "@WORK@/resp/$step.fail" ]; then
      echo "boom $step" >&2
      exit 22
    fi
    idem=
    for arg in "$@"; do
      case "$arg" in Idempotency-Key:*) idem=${arg#Idempotency-Key: } ;; esac
    done
    printf '{"status":"applied"}' > "@WORK@/resp/_writes_$idem"
    printf '%s\n' "$idem" >> "@WORK@/writes.log"
  fi
  exit 0
fi
case "$path" in
  /writes/*)
    if [ -e "@WORK@/resp/readback.fail" ]; then
      cat "@WORK@/resp/readback.fail"
      echo "boom read-back" >&2
      exit 22
    fi
    [ -e "@WORK@/resp/$key" ] || { printf '{"status":"absent"}'; exit 0; } ;;
esac
[ -e "@WORK@/resp/$key" ] && cat "@WORK@/resp/$key"
exit 0
