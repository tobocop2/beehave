#!/bin/sh
# The fake claude of the ticket pipeline scripts. It is the classify agent. It reads the records file that the prompt names and logs "classify <day>" to calls.log in the work folder.
# The program holds the work folder in place of @WORK@. The file mode-<day> in the work folder changes the labels of that day:
#   unsure labels every ticket unsure. short drops the last ticket. spam labels the first ticket with a label that is not allowed.
prompt=$(cat)
write=$(printf '%s\n' "$prompt" | sed -n 's/.* to \(\/[^ ]*\) as JSON.*/\1/p' | head -n 1)
day=$(printf '%s\n' "$prompt" | sed -n 's/^Read the tickets of \([0-9-]*\) in .*/\1/p' | head -n 1)
records=$(printf '%s\n' "$prompt" | sed -n 's/^Read the tickets of [0-9-]* in `\([^`]*\)`.*/\1/p' | head -n 1)
echo "classify $day" >> "@WORK@/calls.log"
mode=ok
[ -e "@WORK@/mode-$day" ] && mode=$(cat "@WORK@/mode-$day")
labels=$(jq -c '{id, label: (if (.subject | test("refund|charge"; "i")) then "billing"
                          elif (.subject | test("crash|error"; "i")) then "bug"
                          elif (.subject | test("how"; "i")) then "how-to"
                          elif (.subject | test("password"; "i")) then "account"
                          else "other" end)}' "$records")
case $mode in
  unsure) labels=$(printf '%s\n' "$labels" | jq -c '.label = "unsure"') ;;
  short) labels=$(printf '%s\n' "$labels" | sed '$d') ;;
  spam) labels=$(printf '%s\n' "$labels" | sed '1s/"label":"[a-z-]*"/"label":"spam"/') ;;
esac
jq -c -n --arg v "$labels" '{labels: $v}' > "$write"
printf '{"type":"result","session_id":"s1","result":"done"}\n'
