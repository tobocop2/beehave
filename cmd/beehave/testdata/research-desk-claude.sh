#!/bin/sh
# The fake claude of the research desk scripts. It names the step from the first words of the shipped prompt and logs the step to calls.log in the work folder.
# The program holds the work folder in place of @WORK@.
#   planner: two sections, one about the cold and one about the cost.
#   reader: the section about the cold gets a true claim and a claim whose quote is not on the page. The section about the cost gets one true claim.
#   judge: grades every claim supported, except a claim that says "opposite". The file credulous in the work folder makes it grade that claim supported too.
#   writer: one paragraph for each claim line, with its marker. The file bad-prose makes the first paragraph state a number that no quote holds.
#   abstract: one line. The file drift makes it change the page /page/a, so that the quote of the first section left its page.
prompt=$(cat)
printf '%s\n' "$prompt" > "@WORK@/last-prompt.txt"
write=$(printf '%s\n' "$prompt" | sed -n 's/.* to \(\/[^ ]*\) as JSON.*/\1/p' | head -n 1)
case $prompt in
  'You split a research question'*) step=plan ;;
  'You read pages'*) step=read ;;
  'You check claims'*) step=judge ;;
  'You write one section'*) step=write ;;
  'You write the abstract'*) step=abstract ;;
  *) step=unknown ;;
esac
case $step in
  read)
    question=$(printf '%s\n' "$prompt" | sed -n '/^## The question/{n;n;p;}')
    case $question in *cold*) topic=cold ;; *) topic=cost ;; esac
    echo "read $topic" >> "@WORK@/calls.log"
    page=$(printf '%s\n' "$prompt" | grep -E '^s[0-9]+	' | while IFS="	" read -r id _ path; do
      if { [ "$topic" = cold ] && grep -q 'output fell' "$path"; } || { [ "$topic" = cost ] && grep -q 'euro per month' "$path"; }; then echo "${id#s}"; fi
    done | head -n 1)
    if [ "$topic" = cold ]; then
      claims=$(jq -c -n --argjson s "$page" '[{text: "Cold cut the output by 30 percent.", source: $s, quote: "At -20 C the output fell by 30 percent."}, {text: "The output fell by 90 percent at -40 C.", source: $s, quote: "At -40 C the output fell by 90 percent."}]')
    else
      claims=$(jq -c -n --argjson s "$page" '[{text: "The unit costs 12 euro per month.", source: $s, quote: "The unit costs 12 euro per month."}]')
    fi
    jq -c -n --arg v "$claims" '{claims: $v}' > "$write"
    ;;
  judge)
    echo judge >> "@WORK@/calls.log"
    list=$(printf '%s\n' "$prompt" | grep '^\[{')
    credulous=false
    [ -e "@WORK@/credulous" ] && credulous=true
    verdicts=$(printf '%s' "$list" | jq -c --argjson c "$credulous" 'map({(.id): (if (.text | test("opposite")) and ($c | not) then "unsupported" else "supported" end)}) | add')
    jq -c -n --arg v "$verdicts" '{verdicts: $v}' > "$write"
    ;;
  write)
    title=$(printf '%s\n' "$prompt" | sed -n '/^## The section/{n;n;p;}' | sed 's/:.*//')
    echo "write $title" >> "@WORK@/calls.log"
    revised=
    printf '%s\n' "$prompt" | grep -q '^## The reviewer asked' && revised=' Revised per the note.'
    prose=$(printf '%s\n' "$prompt" | grep '^\[c[0-9]*\] ' | while IFS= read -r line; do
      id=${line%%]*}
      id=${id#[}
      text=${line#* }
      text=${text%% -- quote:*}
      if [ -e "@WORK@/bad-prose" ]; then text="The output fell by 50 percent."; fi
      printf '%s%s [%s]\n\n' "$text" "$revised" "$id"
    done)
    jq -c -n --arg v "$prose" '{prose: $v}' > "$write"
    ;;
  abstract)
    echo abstract >> "@WORK@/calls.log"
    if [ -e "@WORK@/drift" ]; then
      sed 's/30 percent/3 percent/' "@WORK@/resp/_page_a" > "@WORK@/resp/_page_a.next"
      cat "@WORK@/resp/_page_a.next" > "@WORK@/resp/_page_a"
    fi
    jq -c -n '{abstract: "The report covers the cold and the cost."}' > "$write"
    ;;
  plan)
    echo plan >> "@WORK@/calls.log"
    sections='[{"title":"Output","question":"How does the cold change the output?"},{"title":"Cost","question":"What does the unit cost?"}]'
    jq -c -n --arg v "$sections" '{sections: $v}' > "$write"
    ;;
  *) echo unknown >> "@WORK@/calls.log" ;;
esac
printf '{"type":"result","session_id":"s1","result":"done"}\n'
