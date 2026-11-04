#!/bin/sh
# The stand-in for curl in the research desk scripts. The program holds the work folder in place of @WORK@.
# It logs its arguments to curl.log. A GET of a path answers with the file resp/<path>, where each slash is an underscore. It writes the answer to the file of -o, or to the standard output.
# The search server answers /search with resp/_search. The pages are under /page/. The wiki is /pages/<key>:
# a PUT keeps the file of --data-binary as resp/_pages_<key> and logs the key to wiki.log, and a GET reads it back. The file resp/wiki.corrupt makes the read-back differ.
url=
out=
data=
method=GET
want=
for arg in "$@"; do
  case $want in
    o) out=$arg; want=; continue ;;
    d) data=${arg#@}; want=; continue ;;
    x) method=$arg; want=; continue ;;
    skip) want=; continue ;;
  esac
  case $arg in
    http*) url=$arg ;;
    -o) want=o ;;
    --data-binary) want=d ;;
    -X) want=x ;;
    -H | --data-urlencode | --max-filesize) want=skip ;;
  esac
done
printf '%s\n' "$*" >> "@WORK@/curl.log"
path=$(printf '%s' "$url" | sed -e 's#^[a-z]*://[^/]*##' -e 's#?.*##')
key=$(printf '%s' "$path" | sed -e 's#/#_#g')
if [ -e "@WORK@/resp/$key.fail" ]; then
  echo boom >&2
  exit 22
fi
if [ "$method" = PUT ]; then
  cat "$data" > "@WORK@/resp/$key"
  cat "$data" > "@WORK@/wiki.page"
  printf '%s\n' "${path#/pages/}" >> "@WORK@/wiki.log"
  exit 0
fi
[ -e "@WORK@/resp/$key" ] || { echo "no answer for $path" >&2; exit 22; }
if [ -n "$out" ]; then
  cat "@WORK@/resp/$key" > "$out"
  [ -e "@WORK@/resp/wiki.corrupt" ] && case $path in /pages/*) echo tampered >> "$out" ;; esac
else
  cat "@WORK@/resp/$key"
fi
exit 0
