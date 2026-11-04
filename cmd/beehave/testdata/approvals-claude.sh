#!/bin/sh
# The fake claude of the approval scripts. It logs one line "brief" per call to calls.log in the work folder.
# The program holds the work folder in place of @WORK@. It saves the prompt to prompt-brief-<n>.txt, where <n> is the number of the call.
# It writes a note that says the sign-offs were reset.
prompt=$(cat)
write=$(printf '%s\n' "$prompt" | sed -n 's/.* to \(\/[^ ]*\) as JSON.*/\1/p' | head -n 1)
echo "brief" >> "@WORK@/calls.log"
n=$(wc -l < "@WORK@/calls.log" | tr -d ' ')
printf '%s\n' "$prompt" > "@WORK@/prompt-brief-$n.txt"
printf '{"summary":"The document changed. Earlier sign-offs were reset."}\n' > "$write"
printf '{"type":"result","session_id":"s1","result":"done"}\n'
