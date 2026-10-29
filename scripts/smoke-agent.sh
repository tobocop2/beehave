#!/bin/sh
# Run examples/local/workflow.hcl once with the real claude CLI and check that the run finishes.
# This costs a real agent run, so `make check` never runs it. Run it with `make smoke-agent`.
#
# SMOKE_CLAUDE_CONFIG_DIR is the Claude profile that the run uses. The default is ~/.claude.
# The profile must be logged in to a Claude plan. The run proves that a logged-in profile can start and finish a workflow.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
profile=${SMOKE_CLAUDE_CONFIG_DIR:-$HOME/.claude}

command -v claude >/dev/null 2>&1 || { echo "smoke-agent: claude is not on the path"; exit 1; }
[ -d "$profile" ] || { echo "smoke-agent: $profile is not a folder. Set SMOKE_CLAUDE_CONFIG_DIR."; exit 1; }
[ -x "$root/bin/obee" ] || { echo "smoke-agent: run make build first"; exit 1; }

work=$(mktemp -d)
mkdir -p "$work/config/projects" "$work/state" "$work/repo"

git -C "$work/repo" init -q -b main
printf 'check:\n\ttrue\n' > "$work/repo/Makefile"
git -C "$work/repo" add Makefile
git -C "$work/repo" -c user.name=smoke -c user.email=smoke@example.invalid commit -q -m "start"

printf 'runner {\n  name       = "smoke"\n  agent      = "claude"\n  config_dir = "%s"\n}\n' "$profile" > "$work/config/runners.hcl"
printf 'source      = "%s"\nbase_branch = "main"\ngate        = "make check"\n' "$work/repo" > "$work/config/projects/demo.hcl"
printf 'Add a file named hello.txt that holds the word hello.\n' > "$work/task.md"

export OBEE_CONFIG="$work/config" OBEE_STATE_DIR="$work/state"

echo "smoke-agent: the files of this run are in $work"

"$root/bin/obee" item add demo fix-login --title "Add hello.txt" --body-file "$work/task.md"
"$root/bin/obee" run fix-login demo --workflow "$root/examples/local/workflow.hcl"

echo "smoke-agent: the run finished"
