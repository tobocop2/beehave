#!/bin/sh
# The store of the research desk, as plain files under RESEARCH_HOME/<run>/. Usage: store.sh <verb> <run> [args]
#   init                     make the folder of the run
#   put-plan                 save the plan from the standard input (a JSON array of title and question). It keeps an earlier plan.
#   get-plan                 print n, title and question of each section, tab separated. Exit 1 when the run has no plan.
#   gen N                    print the newest generation of section N (1 when it has none)
#   bump-gen N               start the next generation of section N with the note on the standard input, and print it
#   note N                   print the note of the newest generation of section N
#   status N GEN [STATUS]    print the status of a generation, or set it
#   take-call MAX            count one agent call. Exit 2 when MAX calls (or the raised cap) are used.
#   raise-cap MAX N          allow N more calls than the larger of MAX and the cap so far
#   set-abstract, get-abstract
#   prose N GEN              print the prose of a generation
#   sections-text            print the title and prose of the newest generation of each section
set -eu
. "$(dirname "$0")/lib.sh"

[ $# -ge 2 ] || die "usage: store.sh <verb> <run> [args]"
verb=$1
run=$2
shift 2
dir=$(run_dir "$run")

case $verb in
  init)
    mkdir -p "$dir/snap"
    ;;
  put-plan)
    plan=$(cat)
    printf '%s' "$plan" | jq -e 'type == "array" and length > 0 and all(.[]; (.title | type == "string" and . != "") and (.question | type == "string" and . != ""))' >/dev/null ||
      die "the plan is not a non-empty array of sections with a title and a question"
    # A run that has a plan keeps it, so a restart does not plan again.
    [ -e "$dir/plan.json" ] || printf '%s\n' "$plan" > "$dir/plan.json"
    ;;
  get-plan)
    [ -e "$dir/plan.json" ] || exit 1
    jq -r 'to_entries[] | "\(.key + 1)\t\(.value.title | gsub("[\t\n]"; " "))\t\(.value.question | gsub("[\t\n]"; " "))"' "$dir/plan.json"
    ;;
  gen)
    n=$1
    uint "$n" || die "N must be a whole number"
    cat "$dir/gen.$n" 2>/dev/null || echo 1
    ;;
  bump-gen)
    n=$1
    next=$(($(sh "$0" gen "$run" "$n") + 1))
    mkdir -p "$(section_dir "$run" "$n" "$next")"
    cat > "$(section_dir "$run" "$n" "$next")/note"
    echo "$next" > "$dir/gen.$n"
    echo "$next"
    ;;
  note)
    cat "$(section_dir "$run" "$1" "$(sh "$0" gen "$run" "$1")")/note" 2>/dev/null || true
    ;;
  status)
    folder=$(section_dir "$run" "$1" "$2")
    mkdir -p "$folder"
    if [ $# -ge 3 ]; then
      case $3 in
        open | done | empty | thin | judge-void | cites-failed) printf '%s\n' "$3" > "$folder/status" ;;
        *) die "unknown status: $3" ;;
      esac
    else
      cat "$folder/status" 2>/dev/null || echo open
    fi
    ;;
  take-call)
    uint "$1" || die "MAX must be a whole number"
    used=$(cat "$dir/calls" 2>/dev/null || echo 0)
    max=$1
    raised=$(cat "$dir/cap" 2>/dev/null || echo 0)
    [ "$raised" -le "$max" ] || max=$raised
    if [ "$used" -ge "$max" ]; then
      printf 'The run used all %s agent calls that it may use.\n' "$max"
      exit 2
    fi
    echo $((used + 1)) > "$dir/calls"
    ;;
  raise-cap)
    uint "$1" && uint "$2" || die "MAX and the number of calls must be whole numbers"
    raised=$(cat "$dir/cap" 2>/dev/null || echo 0)
    [ "$raised" -le "$1" ] && raised=$1
    echo $((raised + $2)) > "$dir/cap"
    ;;
  set-abstract)
    cat > "$dir/abstract.md"
    ;;
  get-abstract)
    cat "$dir/abstract.md"
    ;;
  prose)
    cat "$(section_dir "$run" "$1" "$2")/prose.md"
    ;;
  sections-text)
    sh "$0" get-plan "$run" | while IFS="$(printf '\t')" read -r n title _; do
      printf '## %s\n\n' "$title"
      sh "$0" prose "$run" "$n" "$(sh "$0" gen "$run" "$n")"
      printf '\n'
    done
    ;;
  *) die "unknown verb: $verb" ;;
esac
