#!/bin/sh
# Run one agent step with the real claude CLI and check that the command hook blocks a refused command.
# This costs a real agent run, so `make check` never runs it. Run it with `make smoke-hook`.
#
# The step lists forbid_commands = ["bulk-staging"]. The task tells the agent to run `git add -A` once in its work folder.
# The smoke passes only when the transcript of the run holds the refusal of the hook and the index of the work folder is empty.
#
# SMOKE_CLAUDE_CONFIG_DIR is the Claude profile that the run uses. The default is ~/.claude.
# The profile must be logged in to a Claude plan.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
profile=${SMOKE_CLAUDE_CONFIG_DIR:-$HOME/.claude}

command -v claude >/dev/null 2>&1 || { echo "smoke-hook: claude is not on the path"; exit 1; }
[ -d "$profile" ] || { echo "smoke-hook: $profile is not a folder. Set SMOKE_CLAUDE_CONFIG_DIR."; exit 1; }
[ -x "$root/bin/obee" ] || { echo "smoke-hook: run make build first"; exit 1; }

work=$(mktemp -d)
mkdir -p "$work/config/projects" "$work/state" "$work/repo" "$work/flow/prompts"
chmod 700 "$work/state"

git -C "$work/repo" init -q -b main
printf 'hello\n' > "$work/repo/notes.txt"
git -C "$work/repo" add notes.txt
git -C "$work/repo" -c user.name=smoke -c user.email=smoke@example.invalid commit -q -m "start"

cat > "$work/flow/workflow.star" <<'STAR'
def main(item):
    agent(name = "stage", prompt = "prompts/stage.md", model = "sonnet", forbid_commands = ["bulk-staging"], inputs = {"item": item})
STAR

cat > "$work/flow/prompts/stage.md" <<'PROMPT'
This is a test of a command guard. Do exactly this:

1. Create a file named extra.txt in the current folder with the word extra in it.
2. Run the one command `git add -A` in the current folder, once.
3. Stop. Do not run any other command. If the command is refused, do not try another way to stage the file.

Task: {{ .item.id }} {{ .item.body }}
PROMPT

printf 'runner(name = "smoke", agent = "claude", config_dir = "%s")\n' "$profile" > "$work/config/runners.star"
printf 'project(source = "%s", base_branch = "main", gate = "true", test_report = "reports/*.xml")\n' "$work/repo" > "$work/config/projects/demo.star"
token=smoke-hook-$(basename "$work")
printf 'Run the test of the command guard. Run id: %s\n' "$token" > "$work/task.md"

export OBEE_CONFIG="$work/config" OBEE_STATE_DIR="$work/state"

echo "smoke-hook: the files of this run are in $work"

"$root/bin/obee" item add demo stage-test --title "Test the command guard" --body-file "$work/task.md"
"$root/bin/obee" run stage-test demo --workflow "$work/flow/workflow.star" || true

# The work folder of the agent is a worktree of the throwaway repository. Its index is its own.
fail=0
seen=0
trees=$(git -C "$work/repo" worktree list --porcelain | sed -n 's/^worktree //p')
for tree in $trees; do
	if ! git -C "$tree" diff --cached --quiet; then
		echo "smoke-hook: FAIL the index of $tree holds a staged change, so the hook did not block git add -A"
		fail=1
	fi
done

# The refusal is in the transcript of the agent: the result of the call starts with PreToolUse.
for file in $(find "$profile/projects" "$work/state" -type f -newer "$work/task.md" 2>/dev/null); do
	# The run id is in the task text, so a transcript of another session cannot match.
	if grep -q "$token" "$file" && grep -q 'PreToolUse' "$file" && grep -q 'git add -A' "$file"; then
		seen=1
	fi
done
if [ "$seen" -eq 0 ]; then
	echo "smoke-hook: FAIL no record of the run holds a refusal of the hook"
	fail=1
fi

[ "$fail" -eq 0 ] || exit 1
echo "smoke-hook: the hook refused git add -A and the index is empty"
