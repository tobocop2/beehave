#!/bin/sh
# The stand-in for curl in the incident scripts. It stands for the ops API. The program holds the work folder in place of @WORK@.
# It logs its arguments to curl.log and the body that it is sent to curl.body. A read answers with the file resp/<path>, where each slash of the path is an underscore.
# The file resp/<path>.fail makes the call fail with exit 22.
# A POST to /mitigations/<kind> with an Idempotency-Key makes the mitigation applied: the file resp/_mitigations_<key> then holds the state applied, and the key is a line of writes.log.
# The file resp/mitigate.fail makes the POST fail. A POST to /incidents/<id>/page adds a line "<id> <role>" to pages.log.
# A POST to /services/<name>/restore adds a line "<name> <key>" to restores.log. A POST to /incidents/<id>/status adds a line "<id> <state>" to status.log.
url=
sends=0
post=0
idem=
want=
for arg in "$@"; do
  if [ -n "$want" ]; then
    case "$arg" in Idempotency-Key:*) idem=${arg#Idempotency-Key: } ;; esac
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
  echo "boom $path" >&2
  exit 22
fi
if [ "$post" -eq 1 ]; then
  case "$path" in
    /mitigations/*)
      if [ -e "@WORK@/resp/mitigate.fail" ]; then
        echo "boom mitigate" >&2
        exit 22
      fi
      printf '{"state":"applied"}' > "@WORK@/resp/_mitigations_$idem"
      printf '%s %s\n' "${path#/mitigations/}" "$idem" >> "@WORK@/writes.log" ;;
    /incidents/*/page)
      role=$(printf '%s' "$body" | sed -n 's/.*"role":"\([a-z]*\)".*/\1/p')
      id=${path#/incidents/}
      printf '%s %s\n' "${id%/page}" "$role" >> "@WORK@/pages.log" ;;
    /services/*/restore)
      id=${path#/services/}
      printf '%s %s\n' "${id%/restore}" "$idem" >> "@WORK@/restores.log" ;;
    /incidents/*/status)
      id=${path#/incidents/}
      state=$(printf '%s' "$body" | sed -n 's/.*"state":"\([a-z-]*\)".*/\1/p')
      printf '%s %s\n' "${id%/status}" "$state" >> "@WORK@/status.log" ;;
  esac
  exit 0
fi
[ -e "@WORK@/resp/$key" ] && cat "@WORK@/resp/$key"
exit 0
