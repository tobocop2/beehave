#!/bin/sh
# The fake claude of the review loop scripts. It names the step from the first words of the shipped prompt.
# The draft step writes "Draft 1.", and the revise step number n writes "Draft n+1.".
# The review step number n takes its verdict from line n of the file verdicts, and its notes are "Fix round n.".
# Each call logs the step and the text that came to it by message: the draft for a review, the notes for a revise.
step=
write=
section=
got=
while IFS= read -r line; do
  case $line in
    'Write a first draft'*) step=draft ;;
    'Review the draft'*) step=review ;;
    'Revise the draft'*) step=revise ;;
    'Write the draft to '*) write=${line#Write the draft to }; write=${write%% as JSON*} ;;
    'Write the new draft to '*) write=${line#Write the new draft to }; write=${write%% as JSON*} ;;
    'Write the verdict to '*) write=${line#Write the verdict to }; write=${write%% as JSON*} ;;
    '## The draft') section=draft ;;
    '## The notes of the reviewer') section=notes ;;
    '## '*) section= ;;
    '') ;;
    *)
      if [ "$step" = review ] && [ "$section" = draft ] && [ -z "$got" ]; then got=$line; fi
      if [ "$step" = revise ] && [ "$section" = notes ] && [ -z "$got" ]; then got=$line; fi
      ;;
  esac
done
# With a file hold in the work folder, the first review is the crash point: it writes the draft it got to held, and waits for the kill of the engine without an answer.
if [ "$step" = review ] && [ -f "@WORK@/hold" ] && [ ! -f "@WORK@/held" ]; then
  echo "$got" > "@WORK@/held"
  sleep 30
  exit 1
fi
echo "$step${got:+ $got}" >> "@WORK@/calls.log"
n=$(grep -c "^$step" "@WORK@/calls.log")
case $step in
  draft) printf '{"draft":"Draft 1."}\n' > "$write" ;;
  revise) printf '{"draft":"Draft %d."}\n' $((n + 1)) > "$write" ;;
  review)
    verdict=$(sed -n "${n}p" "@WORK@/verdicts")
    notes="Fix round $n."
    [ "$verdict" = approve ] && notes=
    printf '{"verdict":"%s","notes":"%s"}\n' "$verdict" "$notes" > "$write"
    ;;
esac
printf '{"type":"result","session_id":"s1","result":"done"}\n'
