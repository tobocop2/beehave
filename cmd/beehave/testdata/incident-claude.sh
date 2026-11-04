#!/bin/sh
# The fake claude of the incident scripts. It names the step from the first words of the shipped prompt, and logs the step to calls.log in the work folder.
# The program holds the work folder in place of @WORK@. It saves the prompt of each step to prompt-<step>.txt.
# The analyst reads a marker in the alert:
#   ROLLBACK proposes a rollback of checkout to v412 (a safe row). RESTART proposes a restart of checkout (a safe row).
#   SCALE proposes a scale of checkout to 6 replicas (a risky row). NOACTION says that no action is needed.
#   HUMAN asks for a person. BADKIND proposes a kind that the runbook does not hold. BADARGS proposes a rollback with no version.
#   PROCNOTE adds a procedure note to a rollback proposal.
# The postmortem writes a draft that holds the first line of the timeline it was given.
prompt=$(cat)
write=$(printf '%s\n' "$prompt" | sed -n 's/.* to \(\/[^ ]*\) as JSON.*/\1/p' | head -n 1)
case $prompt in
  'You are the analyst'*) step=analyst ;;
  'You write the first draft'*) step=postmortem ;;
  *) step=unknown ;;
esac
echo "$step" >> "@WORK@/calls.log"
printf '%s\n' "$prompt" > "@WORK@/prompt-$step.txt"
case $step in
  analyst)
    note=
    case $prompt in *PROCNOTE*) note="The runbook has no row for a cache flush." ;; esac
    case $prompt in
      *NOACTION*) mitigation=; decision=no-action ;;
      *BADKIND*) mitigation='{\"kind\": \"delete_everything\", \"args\": {}}'; decision=propose ;;
      *BADARGS*) mitigation='{\"kind\": \"rollback\", \"args\": {\"service\": \"checkout\"}}'; decision=propose ;;
      *SCALE*) mitigation='{\"kind\": \"scale\", \"args\": {\"service\": \"checkout\", \"replicas\": 6}}'; decision=propose ;;
      *RESTART*) mitigation='{\"kind\": \"restart\", \"args\": {\"service\": \"checkout\"}}'; decision=propose ;;
      *ROLLBACK*) mitigation='{\"kind\": \"rollback\", \"args\": {\"service\": \"checkout\", \"to_version\": \"v412\"}}'; decision=propose ;;
      *) mitigation=; decision=human ;;
    esac
    printf '{"decision":"%s","mitigation":"%s","summary":"Checkout fails after the deploy. The error rate rose with v413. A rollback fits.","confidence":"high","procedure_note":"%s"}\n' "$decision" "$mitigation" "$note" > "$write"
    ;;
  postmortem)
    first=$(printf '%s\n' "$prompt" | sed -n '/^The desk wrote this list/,/^## The alert/p' | sed -n '3p' | tr -d '\t"\\')
    printf '{"draft":"# Impact\\nCheckout failed. First entry: %s"}\n' "$first" > "$write"
    ;;
esac
printf '{"type":"result","session_id":"s1","result":"done"}\n'
