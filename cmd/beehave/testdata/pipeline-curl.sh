#!/bin/sh
# The stand-in for curl in the ticket pipeline scripts. It stands for the ticket source (SOURCE_API). The program holds the work folder in place of @WORK@.
# It logs its arguments to curl.log, and a day to extracts.log for each first page that it serves.
#   GET <api>/days/<day>/status  answers {"closed": true}, or {"closed": false} when the file src/open-<day> exists.
#   GET <api>/tickets?day&cursor serves the tickets of the day. The file src/n-<day> holds the number of tickets (default 3).
#     A day of 4 tickets or more has two pages. The file src/fail-<day> makes the call exit 22, like curl --fail on an HTTP error.
url=
day=
cursor=
for arg in "$@"; do
  case "$arg" in
    http*) url=$arg ;;
    day=*) day=${arg#day=} ;;
    cursor=*) cursor=${arg#cursor=} ;;
  esac
done
printf '%s\n' "$*" >> "@WORK@/curl.log"
case "$url" in
  */days/*/status)
    d=${url#*/days/}
    d=${d%/status}
    if [ -e "@WORK@/src/open-$d" ]; then echo '{"closed": false}'; else echo '{"closed": true}'; fi
    ;;
  */tickets)
    [ -e "@WORK@/src/fail-$day" ] && { echo "boom $day" >&2; exit 22; }
    n=3
    [ -e "@WORK@/src/n-$day" ] && n=$(cat "@WORK@/src/n-$day")
    page=${cursor:-1}
    [ "$page" = 1 ] && echo "$day" >> "@WORK@/extracts.log"
    from=1
    to=$n
    next=
    if [ "$n" -ge 4 ]; then
      half=$((n / 2))
      if [ "$page" = 1 ]; then to=$half; next=2; else from=$((half + 1)); fi
    elif [ "$page" != 1 ]; then
      from=1
      to=0
    fi
    short=$(printf '%s' "$day" | tr -d '-')
    printf '{"tickets":['
    k=$from
    sep=
    while [ "$k" -le "$to" ]; do
      case $((k % 5)) in
        0) subject="Refund for a charge" ;;
        1) subject="App crash on start" ;;
        2) subject="How do I export" ;;
        3) subject="Password reset" ;;
        4) subject="Hello there" ;;
      esac
      printf '%s{"id":"%s-%s","subject":"%s","body":"text %s"}' "$sep" "$short" "$k" "$subject" "$k"
      sep=,
      k=$((k + 1))
    done
    printf '],"next":"%s"}\n' "$next"
    ;;
  *) echo "fake curl: unknown url $url" >&2; exit 22 ;;
esac
