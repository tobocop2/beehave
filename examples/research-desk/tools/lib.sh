# Shared helpers of the research desk scripts. Source this file: . "$(dirname "$0")/lib.sh"
# RESEARCH_HOME is the folder of the store. One folder for each run holds the plan, the page snapshots, the claims of each section and the call counter.
set -eu

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

: "${RESEARCH_HOME:?RESEARCH_HOME is not set}"

# The SHA-256 of the text on the standard input.
sha() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | cut -d ' ' -f 1
  else
    shasum -a 256 | cut -d ' ' -f 1
  fi
}

# A whole number of zero or more.
uint() {
  case $1 in
    '' | *[!0-9]*) return 1 ;;
  esac
}

# The folder of one run. The run name comes from the workflow, and it has no slash.
run_dir() {
  case $1 in
    '' | */* | .*) die "bad run name: $1" ;;
  esac
  printf '%s/%s\n' "$RESEARCH_HOME" "$1"
}

# The folder of one generation of one section.
section_dir() {
  uint "$2" && uint "$3" || die "N and GEN must be whole numbers"
  printf '%s/s%s.%s\n' "$(run_dir "$1")" "$2" "$3"
}
