package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeScript records its arguments, stdin, working folder and environment, then acts on the mode file beside it. The grandchild pid goes to a temporary
// file first and is renamed, so a reader that sees the "grandchild" file sees a complete pid.
const fakeScript = `#!/bin/bash
here=$(dirname "$0")
echo "$*" > "$here/args"
cat > "$here/stdin"
pwd > "$here/cwd"
env > "$here/env"
mode=$(cat "$here/mode" 2>/dev/null)
case "$mode" in
chatter) for i in 1 2 3 4 5 6; do echo "tick $i"; done ;;
silent) sleep 30 ;;
grand) sleep 30 & echo $! > "$here/grandchild.tmp"; mv "$here/grandchild.tmp" "$here/grandchild"; echo started; exit 0 ;;
holdgrand) sleep 30 & echo $! > "$here/grandchild.tmp"; mv "$here/grandchild.tmp" "$here/grandchild"; sleep 30 ;;
secret) echo "token is $CLAUDE_CODE_OAUTH_TOKEN and sk-ant-abcdef123456 here"; echo "err $CLAUDE_CODE_OAUTH_TOKEN" >&2; exit 3 ;;
fail) echo "boom" >&2; exit 7 ;;
partial) printf 'no newline at the end' ;;
long) head -c 300 /dev/zero | tr '\0' x; echo; echo after ;;
esac
`

// install puts a fake agent first on PATH and returns the folder that holds its records.
func install(t *testing.T, name, mode string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(fakeScript), 0o700)) //nolint:gosec // a test script must be executable
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0o600))
	t.Setenv("PATH", dir+":/bin:/usr/bin")

	return dir
}

func record(t *testing.T, dir, name string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a path inside the test folder
	require.NoError(t, err)

	return strings.TrimSpace(string(raw))
}
