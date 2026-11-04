#!/bin/sh
# The fake claude of the maintenance example. It logs the step to calls.log in the work folder and saves the prompt to prompt-<step>.txt.
# update: runs the command of the prompt in the worktree and writes the verdict for its exit code: 0 updated, 11 unchanged, else failed.
# fix: writes the first line of the file fix-verdicts (fixed or gave-up); the file is read once for each call. With no file the verdict is fixed.
prompt=$(cat)
write=$(printf '%s\n' "$prompt" | sed -n 's/.* to \(\/[^ ]*\) as JSON.*/\1/p' | head -n 1)
case $prompt in
  'You update the dependencies'*) step=update ;;
  'You fix a repository'*) step=fix ;;
  *) step=unknown ;;
esac
echo "$step" >> "@WORK@/calls.log"
printf '%s\n' "$prompt" > "@WORK@/prompt-$step.txt"
case $step in
  update)
    command=$(printf '%s\n' "$prompt" | sed -n '/^sh .*apply\.sh /p' | head -n 1)
    sh -c "$command" >> "@WORK@/apply.log" 2>&1
    rc=$?
    case $rc in 0) verdict=updated ;; 11) verdict=unchanged ;; *) verdict=failed ;; esac
    printf '{"verdict":"%s"}\n' "$verdict" > "$write"
    ;;
  fix)
    verdict=fixed
    if [ -f "@WORK@/fix-verdicts" ]; then
      verdict=$(head -n 1 "@WORK@/fix-verdicts")
      tail -n +2 "@WORK@/fix-verdicts" > "@WORK@/fix-verdicts.new"
      mv "@WORK@/fix-verdicts.new" "@WORK@/fix-verdicts"
    fi
    printf '{"verdict":"%s"}\n' "$verdict" > "$write"
    ;;
esac
printf '{"type":"result","session_id":"s1","result":"done"}\n'
