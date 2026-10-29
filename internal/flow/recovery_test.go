package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Launch resumes the open run of a crashed engine. When that run ends before RunFile reads the item, RunFile must report its result and start no second attempt.
func TestRunFile_ARunThatTheLaunchResumedAndEndedIsNotRunAgain(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"ok"}`, exited)

	file := writeFlow(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt  = \"prompts/a.md\"\n  outputs = [\"verdict\"]\n}\ncommand \"after\" {\n  run = [\"after\", agent.a.verdict]\n}\n")
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("Write to {{ .output_file }}\n"), 0o600))

	rig.crashAfterThePhaseEnded(t, file)

	next := rig.h.engine(t)

	eventually(t, func() bool {
		rows, err := dbos.ListWorkflows(next.ctx, dbos.WithFilterName(itemName), dbos.WithFilterStatus(dbos.WorkflowStatusSuccess))

		return err == nil && len(rows) == 1
	})

	_, err := next.RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"after ok"}, rig.h.commands.ran(), "the resumed run read the file once")
	assert.Len(t, rig.h.agents.seen(), 1, "the agent does not run again")
}
