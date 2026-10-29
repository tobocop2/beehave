#!/bin/sh
# Assemble the report of a research item from its notes.
#
# usage: report.sh <notes folder> <report file>
#
# The title comes from the variable TITLE. The variable SUMMARY holds the summary that an agent wrote. When it holds text,
# the report has a "Summary" section after the title.
# When the variable OPEN holds text, the review did not approve the notes. The report then starts with that text under the heading
# "Open findings", before the summary, and the script exits with code 3 after it writes the report, so that the item does not read as a success.
# The report holds the notes in file order, then the list of sources without repeats.
#
# Exit code 0: the report is written. Exit code 3: the report is written and the review left findings open. Exit code 2: wrong arguments.
set -eu

folder=${1:-}
report=${2:-}
[ -d "$folder" ] && [ -n "$report" ] || { echo "usage: report.sh <notes folder> <report file>"; exit 2; }

mkdir -p "$(dirname "$report")"

{
  printf '# %s\n\n' "${TITLE:-Research report}"

  if [ -n "${OPEN:-}" ]; then
    printf '## Open findings\n\nThe review did not approve these notes. These findings are still open.\n\n%s\n\n' "$OPEN"
  fi

  if [ -n "${SUMMARY:-}" ]; then
    printf '## Summary\n\n%s\n\n' "$SUMMARY"
  fi

  for file in "$folder"/*.md; do
    cat "$file"
    printf '\n'
  done

  printf '## Sources\n\n'
  cat "$folder"/*.md | grep -o '\[source: [^]]*\]' | sed 's/^\[source: \(.*\)\]$/- \1/' | sort -u
} > "$report"

if [ -n "${OPEN:-}" ]; then
  echo "report: wrote $report. The review left findings open, so the item ends as stopped."
  exit 3
fi

echo "report: wrote $report"
