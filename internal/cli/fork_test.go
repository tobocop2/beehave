package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
)

func (s *fakeSteering) Fork(req flow.ForkRequest) (string, error) {
	s.calls = append(s.calls, fmt.Sprintf("fork %s %s %v", req.Run, req.Step, req.Vars))

	if s.forkErr != nil {
		return "", s.forkErr
	}

	return "item-12-r2", s.err
}

func varsFile(t *testing.T, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "vars.json")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func TestForkCommand_AsksTheClientForTheForkAndPrintsTheNewRun(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, stdout, stderr := rig.execute(t.Context(), "fork", "item-12-r1", "--from", "command:two#2", "--vars", varsFile(t, `{"mode": "b"}`))
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, []string{"fork item-12-r1 command:two#2 map[mode:b]"}, rig.steering.calls)
	assert.Contains(t, stdout, "as item-12-r2. Run beehave run for the item to continue it.")
}

func TestForkCommand_AForkWithNoVarsFilePassesNoVars(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "fork", "item-12-r1", "--from", "command:two#2")
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, []string{"fork item-12-r1 command:two#2 map[]"}, rig.steering.calls)
}

func TestForkCommand_RefusesAVarsFileThatIsMissingOrIsNotAnObjectBeforeAskingTheClient(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "fork", "item-12-r1", "--from", "command:two#2", "--vars", filepath.Join(t.TempDir(), "none.json"))
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "read the vars file")

	code, _, stderr = rig.execute(t.Context(), "fork", "item-12-r1", "--from", "command:two#2", "--vars", varsFile(t, `["mode"]`))
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "one JSON object")
	assert.Empty(t, rig.steering.calls)
}

func TestForkCommand_AForkThatTheClientCannotStartIsReportedAndNamesNoNewRun(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.forkErr = errFake

	code, stdout, stderr := rig.execute(t.Context(), "fork", "item-12-r1", "--from", "command:two#2")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, errFake.Error())
	assert.Empty(t, stdout, "no new run is named")
}

func TestForkCommand_NeedsOneRunIdAndTheFlowCommandsOfTheConfig(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "fork", "--from", "command:two#2")
	assert.Equal(t, 1, code)
	assert.NotEmpty(t, stderr)
	assert.Empty(t, rig.steering.calls)

	rig = newRig(t)
	writeConfig(t, rig.dir, "runners.star", "agents(max_concurrent = 0)\n"+validRunners)

	code, _, stderr = rig.execute(t.Context(), "fork", "item-12-r1", "--from", "command:two#2")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "agents.max_concurrent must be 1 or more")
}
