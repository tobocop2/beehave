#!/bin/sh
# The fake claude of the research scripts. It names the step from the first words of the shipped prompt.
# The scope step writes two sub-questions, or six when the file six exists. A research step writes a notes file with one claim and
# the address of the server of the script. The file bad holds the number of times that the notes of sub-question 2 get a claim with no source.
# When the file revise-breaks exists, the first revise step puts a claim with no source into the notes of sub-question 2.
# A review step takes its verdict from the line of the file verdicts that matches the number of the call:
#   approve   writes the verdict approve.
#   changes   writes the verdict changes with one finding, and a last message that does not repeat the finding.
#   silent    writes the verdict changes with a finding and no finding text.
#   broken    writes a file that is not JSON.
#   auto      reads the summary in the prompt: a summary that says "by 40 percent" gets a finding, any other summary is approved.
# When the file bad-summary exists, a summary step with no problem to fix writes "by 40 percent", a claim that the notes never state.
# A summary step that gets the problems of the review in its prompt writes the clean summary, and logs the finding line to found-summary.log.
# When the file revise-extra exists, each revise step writes notes/6.md.
step=
write=
number=
fixing=
lie=
sumline=
while IFS= read -r line; do
  case $line in
    'Split the question'*) step=scope ;;
    'Research the sub-question'*) step=research; number=${line#*notes/}; number=${number%%.md*} ;;
    'Try to refute'*) step=review ;;
    'Fix each finding'*) step=revise ;;
    'Write a short summary'*) step=summary ;;
    'Write the verdict to '*) write=${line#Write the verdict to }; write=${write%% as JSON*} ;;
    'Write the sub-questions to '*) write=${line#Write the sub-questions to }; write=${write%% as JSON*} ;;
    'Write the summary to '*) write=${line#Write the summary to }; write=${write%% as JSON*} ;;
    'FAIL notes/'*) echo "$line" >> "@WORK@/found.log" ;;
    'The review found these problems in your earlier summary'*) fixing=1 ;;
    'summary: '*) sumline=$line ;;
    *'by 40 percent'*) lie=1 ;;
  esac
done
echo "$step${number:+ $number}" >> "@WORK@/calls.log"
base=$(cat "@WORK@/server.url")
text=done
bad=0; [ -e "@WORK@/bad" ] && bad=$(cat "@WORK@/bad")
made=0; [ -e "@WORK@/made" ] && made=$(cat "@WORK@/made")
case $step in
  scope)
    if [ -e "@WORK@/six" ]; then
      printf '{"questions":"What is A?\\nWhat is B?\\nWhat is C?\\nWhat is D?\\nWhat is E?\\nWhat is F?"}\n' > "$write"
    else
      printf '{"questions":"What is A?\\nWhat is B?"}\n' > "$write"
    fi
    ;;
  research)
    mkdir -p notes
    claim="- Claim $number. [source: $base/ok]"
    if [ "$number" = 2 ] && [ "$made" -lt "$bad" ]; then echo $((made + 1)) > "@WORK@/made"; claim='- Claim 2 has no source.'; fi
    case $number in 1) letter=A ;; 2) letter=B ;; 3) letter=C ;; 4) letter=D ;; 5) letter=E ;; *) letter=F ;; esac
    printf '# What is %s?\n\n%s\n' "$letter" "$claim" > "notes/$number.md"
    ;;
  revise)
    if [ -e "@WORK@/revise-extra" ]; then
      printf '# What is F?\n\n- Claim 6. [source: %s/ok]\n' "$base" > notes/6.md
    fi
    if [ -e "@WORK@/revise-breaks" ] && [ ! -e "@WORK@/revise-broke" ]; then
      : > "@WORK@/revise-broke"
      printf '# What is B?\n\n- Claim 2 was revised and has no source.\n' > notes/2.md
    fi
    ;;
  review)
    n=$(grep -c '^review' "@WORK@/calls.log")
    case $(sed -n "${n}p" "@WORK@/verdicts") in
      approve) printf '{"verdict":"approve","findings":[],"open":""}\n' > "$write" ;;
      changes)
        printf '{"verdict":"changes","findings":[{"class":"blocking","text":"notes/1.md: the source does not state the claim"}],"open":"notes/1.md: the source does not state the claim"}\n' > "$write"
        text='I reviewed the notes and found problems.'
        ;;
      silent)
        printf '{"verdict":"changes","findings":[{"class":"blocking","text":"notes/1.md: the source does not state the claim"}],"open":""}\n' > "$write"
        text='notes/1.md: the source does not state the claim'
        ;;
      auto)
        # The reviewer reads the summary in its prompt: a number that the notes never state is a finding.
        if [ -n "$lie" ]; then
          printf '{"verdict":"changes","findings":[{"class":"blocking","text":"summary: the claim by 40 percent is not in the notes"}],"open":"summary: the claim '"'"'by 40 percent'"'"' is not in the notes"}\n' > "$write"
        else
          printf '{"verdict":"approve","findings":[],"open":""}\n' > "$write"
        fi
        ;;
      broken) printf 'this is not JSON\n' > "$write" ;;
    esac
    ;;
  summary)
    if [ -e "@WORK@/bad-summary" ] && [ -z "$fixing" ]; then
      printf '{"summary":"A beats B by 40 percent."}\n' > "$write"
    else
      printf '{"summary":"A and B are compared in the notes."}\n' > "$write"
    fi
    [ -z "$fixing" ] || echo "$sumline" >> "@WORK@/found-summary.log"
    ;;
esac
printf '{"type":"result","session_id":"s1","result":"%s"}\n' "$text"
