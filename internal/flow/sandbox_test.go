package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/model"
)

func sandboxSeen(t *testing.T, rig *agentRig) *model.Sandbox {
	t.Helper()

	require.Len(t, rig.h.agents.seen(), 1)

	return rig.h.agents.seen()[0].Sandbox
}

func TestAgentStep_ASandboxCallGoesToTheAgentRequestWithItsLists(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`sandbox = sandbox(network = ["api.example.org"], allow_write = ["out"]), `), "x")

	require.NoError(t, err)
	assert.Equal(t, &model.Sandbox{Network: []string{"api.example.org"}, AllowWrite: []string{"out"}}, sandboxSeen(t, rig))
}

func TestAgentStep_ASandboxCallWithNoListsTurnsTheSandboxOnWithNoDomain(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`sandbox = sandbox(), `), "x")

	require.NoError(t, err)
	assert.Equal(t, &model.Sandbox{}, sandboxSeen(t, rig))
}

func TestAgentStep_ANoneOptsOutEvenWhenTheProjectDefaultIsOn(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.sandboxDefault = func(string) bool { return true }
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`sandbox = None, `), "x")

	require.NoError(t, err)
	assert.Nil(t, sandboxSeen(t, rig))
}

func TestAgentStep_ALeftOutKeywordTakesTheDefaultOfTheProject(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		def  func(string) bool
		want *model.Sandbox
	}{
		"default on":        {func(string) bool { return true }, &model.Sandbox{}},
		"default off":       {func(string) bool { return false }, nil},
		"no default at all": {nil, nil},
		"another project":   {func(project string) bool { return project != testProject }, nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)
			rig.h.sandboxDefault = test.def
			rig.reply("", exited)

			_, err := rig.run(t, agentFlow(``), "x")

			require.NoError(t, err)
			assert.Equal(t, test.want, sandboxSeen(t, rig))
		})
	}
}

func TestAgentStep_ASandboxCallOverridesTheDefaultOff(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.sandboxDefault = func(string) bool { return false }
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`sandbox = sandbox(network = ["a.example.org"]), `), "x")

	require.NoError(t, err)
	assert.Equal(t, &model.Sandbox{Network: []string{"a.example.org"}}, sandboxSeen(t, rig))
}

func TestAgentStep_ARefusedSandboxValueStopsTheStepBeforeTheAgentStarts(t *testing.T) {
	t.Parallel()

	for extra, want := range map[string]string{
		`sandbox = "on", `:                                  "sandbox must be sandbox(...) or None",
		`sandbox = True, `:                                  "sandbox must be sandbox(...) or None",
		`sandbox = struct(network = []), `:                  "sandbox must be sandbox(...) or None",
		`sandbox = sandbox(network = "x"), `:                "network must be a list, not a text.",
		`sandbox = sandbox(allow_write = "x"), `:            "allow_write must be a list, not a text.",
		`sandbox = sandbox(network = [3]), `:                "network must hold strings only",
		`sandbox = sandbox(network = ["https://x.org"]), `:  "not a domain name",
		`sandbox = sandbox(allow_write = [""]), `:           "empty path",
		`sandbox = sandbox(allow_write = ["../outside"]), `: `holds ".."`,
		`sandbox = sandbox(read = []), `:                    "sandbox takes no argument read. Use one of: network, allow_write.",
		`sandbox = sandbox(network = [3] + []), `:           "network must hold strings only",
	} {
		t.Run(extra, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)

			_, err := rig.run(t, agentFlow(extra), "x")

			require.ErrorContains(t, err, want)
			assert.Empty(t, rig.h.agents.seen())
		})
	}
}

func TestBoxOf_RefusesAListThatIsNoListOfStrings(t *testing.T) {
	t.Parallel()

	_, err := boxOf(starlark.MakeInt(1), nil)
	require.ErrorContains(t, err, "network must be a list of strings")

	_, err = boxOf(nil, starlark.NewList([]starlark.Value{starlark.MakeInt(1)}))
	require.ErrorContains(t, err, "allow_write must hold strings only")

	_, err = boxOf(starlark.NewList([]starlark.Value{starlark.String("https://x.org")}), nil)
	require.ErrorContains(t, err, "not a domain name")
}

func TestRecalledSandbox_AStepRecordedWithNoSandboxReplaysWithNoSandbox(t *testing.T) {
	t.Parallel()

	box, err := recalledSandbox(nil)
	require.NoError(t, err)
	assert.Nil(t, box, "a record without the field is a step with no sandbox")

	box, err = recalledSandbox(map[string]any{"other": "x"})
	require.NoError(t, err)
	assert.Nil(t, box)
}

func TestRecalledSandbox_ReadsBackWhatRecordSandboxWrote(t *testing.T) {
	t.Parallel()

	for _, box := range []*model.Sandbox{{}, {Network: []string{"a.example.org"}, AllowWrite: []string{"/x"}}} {
		// The record goes through JSON in the step, so a value of the outputs is read back as text.
		raw, err := json.Marshal(recordSandbox(box))
		require.NoError(t, err)

		var round map[string]any
		require.NoError(t, json.Unmarshal(raw, &round))

		got, err := recalledSandbox(round)
		require.NoError(t, err)
		assert.Equal(t, box, got)
	}

	assert.Empty(t, recordSandbox(nil), "no sandbox records nothing")
}

func TestRecalledOutputFile_ARecordWithoutAPinnedPathFallsBackToTheComputedOne(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/computed", recalledOutputFile(nil, "/computed"))
	assert.Equal(t, "/computed", recalledOutputFile(map[string]any{recordedSandbox: "x"}, "/computed"))
	assert.Equal(t, "/pinned", recalledOutputFile(map[string]any{recordedOutputFile: "/pinned"}, "/computed"))
}

func TestRecall_APinnedFileWhoseFolderCannotBeMadeIsAnError(t *testing.T) {
	t.Parallel()

	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))

	a := agentArgs{}
	err := a.recall(map[string]any{recordedOutputFile: filepath.Join(blocker, "x.json")})

	require.ErrorContains(t, err, "make the folder of the output file")
}

func TestRecalledSandbox_AnUnreadableRecordIsAnError(t *testing.T) {
	t.Parallel()

	_, err := recalledSandbox(map[string]any{recordedSandbox: "{not json"})

	require.ErrorContains(t, err, "read the recorded sandbox of the step")
}

func TestPhaseInput_APhaseStoredWithoutTheSandboxFieldReadsAsNoSandbox(t *testing.T) {
	t.Parallel()

	var in PhaseInput

	require.NoError(t, json.Unmarshal([]byte(`{"run":"r","item":"i","project":"p","login":"l","model":"m"}`), &in))
	assert.Nil(t, in.Sandbox)

	raw, err := json.Marshal(PhaseInput{Run: "r"})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "sandbox", "a step with no sandbox keeps the stored form")

	raw, err = json.Marshal(PhaseInput{Run: "r", Sandbox: &model.Sandbox{Network: []string{"a.example.org"}}})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"sandbox":{"network":["a.example.org"]}`)
}
