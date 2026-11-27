package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

func TestAgentStep_TheOutputFileOfACallIsTheOneFileOfAFolderOfItsOwn(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"approve"}`, exited)

	_, err := rig.run(t, outputsFlow("1"), "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	requests := rig.h.agents.seen()
	require.Len(t, requests, 1)

	folder := filepath.Join(rig.h.opts.StateDir, outputsFolder, "12.agent_3Aa_231")
	assert.Equal(t, filepath.Join(folder, "output.json"), requests[0].OutputFile, "the sandbox can name the folder of the call as a working directory")

	entries, err := os.ReadDir(folder)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the folder holds the output file alone")
	assert.Equal(t, "output.json", entries[0].Name())
}

func TestRefusedWrites_NamesEachRefusedCallByWhatItDoesToTheOutputFile(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "output.json")
	refused := ": the permission check of the agent CLI refused the "

	for name, tc := range map[string]struct {
		denial agent.Denial
		want   string
	}{
		"a write of the file":                   {agent.Denial{Tool: "Write", Target: file, Edit: true}, refused + "Write call that writes it"},
		"a read of the file":                    {agent.Denial{Tool: "Read", Target: file}, refused + "Read call that reads it"},
		"a shell command that names it":         {agent.Denial{Tool: "Bash", Target: "printf x > " + file + " && cat " + file}, refused + "Bash call whose command names it"},
		"a read of another file":                {agent.Denial{Tool: "Read", Target: file + ".old"}, ""},
		"a command that names other file":       {agent.Denial{Tool: "Bash", Target: "cat /etc/hosts"}, ""},
		"a command on a longer name":            {agent.Denial{Tool: "Bash", Target: "cat " + file + ".tmp"}, ""},
		"a command on a path that ends with it": {agent.Denial{Tool: "Bash", Target: "cat /mnt" + file}, ""},
		"a command that names it after a longer name": {
			agent.Denial{Tool: "Bash", Target: "cp " + file + ".bak '" + file + "'"}, refused + "Bash call whose command names it",
		},
		"a command that gives it to a flag with =":  {agent.Denial{Tool: "Bash", Target: "tool --out=" + file}, refused + "Bash call whose command names it"},
		"a command that holds it in backticks":      {agent.Denial{Tool: "Bash", Target: "echo `cat " + file + "`"}, refused + "Bash call whose command names it"},
		"a command that holds it in a substitution": {agent.Denial{Tool: "Bash", Target: "echo $(cat " + file + ")"}, refused + "Bash call whose command names it"},
		"a write of a longer name":                  {agent.Denial{Tool: "Write", Target: file + ".tmp", Edit: true}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, refusedWrites(file, []agent.Denial{tc.denial}))
		})
	}
}

func TestRefusedWrites_TwoRefusedCallsAreNamedOnceEachInNameOrder(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "output.json")
	denials := []agent.Denial{{Tool: "Write", Target: file, Edit: true}, {Tool: "Read", Target: file}, {Tool: "Write", Target: file, Edit: true}}

	assert.Equal(t, ": the permission check of the agent CLI refused the Read call that reads it and the Write call that writes it", refusedWrites(file, denials))
}

func TestPhase_AQueuedPhaseWhoseOutputFileIsNotTheFileOfItsCallStartsNoAgent(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := launchedWith(t, harness, func(*Deps) {})
	flat := filepath.Join(harness.opts.StateDir, outputsFolder, "12.agent_3Aa_231.json")

	stored, err := json.Marshal(map[string]any{
		"run": "item-12-r1", "call": aStep, "item": testItem, "project": testProject, "login": testLogin, "prompt": "Write the verdict",
		"output_file": flat, "outputs": []string{"verdict"},
	})
	require.NoError(t, err)

	var in PhaseInput
	require.NoError(t, json.Unmarshal(stored, &in))

	err = queuePhase(t, engine, in)

	require.ErrorContains(t, err, "the agent phase of item 12 names the output file "+flat)
	require.ErrorContains(t, err, "Run beehest cancel 12, then start a new run")
	assert.Empty(t, harness.agents.seen(), "the agent never gets the outputs folder as a working directory")
}

func TestPhase_AQueuedPhaseThatDeclaresNoOutputsStartsItsAgent(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := launchedWith(t, harness, func(*Deps) {})

	stored, err := json.Marshal(map[string]any{"run": "item-12-r1", "call": aStep, "item": testItem, "project": testProject, "login": testLogin, "prompt": "Look"})
	require.NoError(t, err)

	var in PhaseInput
	require.NoError(t, json.Unmarshal(stored, &in))

	require.NoError(t, queuePhase(t, engine, in))
	assert.Len(t, harness.agents.seen(), 1, "a call that declares no outputs starts")
}
