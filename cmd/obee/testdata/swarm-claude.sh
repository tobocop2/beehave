#!/bin/sh
# The fake claude of the swarm scripts. It reads the prompt from standard input.
# A task prompt holds the line "<id>: <title>" after the heading "## The task", then the body. The body names the file that the task writes.
# A resolve prompt starts with "The pull request of the branch <branch>". Each call adds one line to calls.log, and the cost of the call is the file cost (default 0.50).
# While it runs, a task holds a marker in the folder running, and concurrency.log gets the number of markers, so a script can prove the slot limit.
# If the file unresolvable exists, a resolve call leaves the branch as it is, so the pull request conflicts again.
first=
id=
body=
state=0
while IFS= read -r line; do
  [ -z "$first" ] && first=$line
  case $state in
    0) [ "$line" = '## The task' ] && state=1 ;;
    1) if [ -n "$line" ]; then id=${line%%:*}; state=2; fi ;;
    2) if [ -n "$line" ]; then body=$line; state=3; fi ;;
  esac
done
work=@WORK@
cost=0.50
[ -f "$work/cost" ] && cost=$(cat "$work/cost")
who='-c user.name=dev -c user.email=agent@example.com'
case $first in
  'The pull request of the branch '*)
    branch=${first#The pull request of the branch }
    branch=${branch%% *}
    echo "resolve $branch" >> "$work/calls.log"
    if [ ! -f "$work/unresolvable" ]; then
      {
        git fetch -q origin
        git checkout -q -B "$branch" "origin/$branch"
        git $who merge -q -X theirs -m resolve origin/main
        git push -q origin "$branch"
      } > /dev/null 2>&1
    fi
    ;;
  *)
    echo "task $id" >> "$work/calls.log"
    mkdir -p "$work/running"
    : > "$work/running/$id"
    # Rendezvous, only if the file rendezvous exists. The first two tasks to arrive wait (at most 20 seconds) for the marker of each other,
    # and then hold their markers (at most 5 seconds) until a third task arrives. So two runs overlap by construction, and a third slot shows as the count 3.
    if [ -f "$work/rendezvous" ] && { mkdir "$work/ticket-1" 2> /dev/null || mkdir "$work/ticket-2" 2> /dev/null; }; then
      peer=
      n=0
      while [ -z "$peer" ] && [ $n -lt 100 ]; do
        for m in "$work"/running/*; do
          [ "${m##*/}" != "$id" ] && peer=${m##*/}
        done
        [ -z "$peer" ] && sleep 0.2
        n=$((n + 1))
      done
      set -- "$work"/running/*
      echo $# >> "$work/concurrency.log"
      : > "$work/saw-$id"
      n=0
      while [ -n "$peer" ] && [ ! -e "$work/saw-$peer" ] && [ $n -lt 100 ]; do sleep 0.2; n=$((n + 1)); done
      n=0
      while [ ! -e "$work/arrived-3" ] && [ $n -lt 25 ]; do sleep 0.2; n=$((n + 1)); done
    else
      : > "$work/arrived-3"
      set -- "$work"/running/*
      echo $# >> "$work/concurrency.log"
    fi
    # A task with a body that starts with late waits (at most 20 seconds) until a resolve call is in calls.log, and writes the file starved if none came.
    case $body in
      late*)
        n=0
        while ! grep -q '^resolve ' "$work/calls.log" && [ $n -lt 100 ]; do sleep 0.2; n=$((n + 1)); done
        grep -q '^resolve ' "$work/calls.log" || : > "$work/starved"
        ;;
    esac
    file=${body:-$id.txt}
    echo "$id" > "$file"
    echo "$id" > "$id.txt"
    git add "$file" "$id.txt"
    git $who commit -q -m "task $id"
    git push -q origin "HEAD:refs/heads/$id"
    mv "$work/running/$id" "$work/ended-$id"
    ;;
esac
echo '{"type":"result","subtype":"success","session_id":"s1","result":"done","total_cost_usd":'"$cost"'}'
