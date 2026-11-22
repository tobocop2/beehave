package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// countingLogins counts the picks of a login.
type countingLogins struct {
	fakeLogins
	picks *int
}

func (l countingLogins) Pick(ctx context.Context, project, item string, busy []string, overCap bool) (string, error) {
	*l.picks++

	return l.fakeLogins.Pick(ctx, project, item, busy, overCap)
}

// storedInput reads the input that the store holds for the latest run of the item.
func storedInput(t *testing.T, client *Client, item string) ItemInput {
	t.Helper()

	last, found, err := latestRun(client.flow, item)
	require.NoError(t, err)
	require.True(t, found)

	in, err := last.input()
	require.NoError(t, err)

	return in
}

func TestAgent_ASandboxNoneStepInASandboxedRunIsRefusedBeforeItsLoginIsPicked(t *testing.T) {
	t.Parallel()

	var picks int

	rig := newAgentRig(t)
	rig.sandbox = model.SandboxOn
	rig.h.picker = countingLogins{fakeLogins: rig.h.logins, picks: &picks}
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`sandbox = None, `), "x")

	require.ErrorContains(t, err, "sets sandbox = None")
	require.ErrorContains(t, err, `Set sandbox = "off" in the project`)
	require.ErrorContains(t, err, "beehave run")
	assert.Zero(t, picks, "the refusal comes before the pick of a login")
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgent_TheRefusalOfASandboxNoneStepHoldsForItsPositionItsRepeatAndALookalikeName(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		body    string
		refused bool
		ran     int
	}{
		"a boxed step first, then None": {agentCallText(`sandbox = sandbox(), `) + agentCallText(`sandbox = None, `), true, 1},
		"None twice":                    {agentCallText(`sandbox = None, `) + agentCallText(`sandbox = None, `), true, 0},
		"a variable named none_box":     {"none_box = sandbox(network = [])\n" + agentCallText(`sandbox = none_box, `), false, 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)
			rig.sandbox = model.SandboxOn
			rig.reply("", exited)

			_, err := rig.run(t, bodyFlow(test.body), "x")

			if test.refused {
				require.ErrorContains(t, err, "sets sandbox = None")
			} else {
				require.NoError(t, err)
			}

			assert.Len(t, rig.h.agents.seen(), test.ran)
		})
	}
}

func TestRun_AProjectWithSandboxOffRunsASandboxNoneStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.sandbox = model.SandboxOff
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`sandbox = None, `), "x")

	require.NoError(t, err)
	assert.Nil(t, sandboxSeen(t, rig), "the step runs with no sandbox, as the owner chose with off")
}

func TestRun_ASandboxedRunStillRunsAStepWithItsOwnBox(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.sandbox = model.SandboxOn
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`sandbox = sandbox(network = ["a.example.org"]), `), "x")

	require.NoError(t, err)
	assert.Equal(t, &model.Sandbox{Network: []string{"a.example.org"}}, sandboxSeen(t, rig))
}

func TestRun_TheRecordOfARunHoldsTheSandboxModeItStartedWith(t *testing.T) {
	t.Parallel()

	for name, mode := range map[string]model.SandboxMode{"on": model.SandboxOn, "off": model.SandboxOff} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)
			rig.sandbox = mode
			rig.reply("", exited)

			_, err := rig.run(t, agentFlow(``), "x")
			require.NoError(t, err)

			assert.Equal(t, mode, storedInput(t, rig.h.client(t, testVersion), testItem).Sandbox, "the record holds the mode the run started with")
		})
	}
}

func TestSpawn_AChildRunKeepsTheSandboxModeOfItsParent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	spawner := "def worker(item):\n    command(name = \"w\", run = [\"w\"])\n\ndef main(item):\n    gather(spawn(items = [\"21\"], entry = \"worker\"))\n"

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, spawner), RunSandbox(model.SandboxOn))
	require.NoError(t, err)

	assert.Equal(t, model.SandboxOn, storedInput(t, h.client(t, testVersion), "21").Sandbox)
}

func TestSpawn_AChildOfARunWithNoStoredModeStoresTheModeItsParentRunsWith(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.sandboxDefault = func(string) bool { return true }
	spawner := "def worker(item):\n    command(name = \"w\", run = [\"w\"])\n\ndef main(item):\n    gather(spawn(items = [\"21\"], entry = \"worker\"))\n"

	_, err := runOldRecord(t, rig, spawner)
	require.NoError(t, err)

	assert.Equal(t, model.SandboxOn, storedInput(t, rig.h.client(t, testVersion), "21").Sandbox, "the child stores on or off, never the empty mode of an old parent")
}

func TestResume_ARunWithNoRecordedSandboxModeKeepsRunningItsStepsUnsandboxed(t *testing.T) {
	t.Parallel()

	var old ItemInput

	require.NoError(t, json.Unmarshal([]byte(`{"item":"12","project":"p"}`), &old))
	assert.Empty(t, old.Sandbox, "an input stored before the field has no mode")

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.sandboxDefault = func(string) bool { return false }

	_, err := runOldRecord(t, rig, agentFlow(`sandbox = None, `))

	require.NoError(t, err, "the live config of the project says off, so the None step starts")
	assert.Nil(t, sandboxSeen(t, rig))
}

func TestResume_ARunWithNoRecordedSandboxModeInAProjectWithTheSandboxOnRefusesASandboxNoneStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	asked := ""
	rig.h.sandboxDefault = func(project string) bool { asked = project; return true }

	_, err := runOldRecord(t, rig, agentFlow(`sandbox = None, `))

	require.ErrorContains(t, err, "sets sandbox = None", "an old run reads the live config, which says on")
	assert.Equal(t, testProject, asked)
	assert.Empty(t, rig.h.agents.seen())
}

func TestRun_ARunWithAStoredModeNeverReadsTheLiveConfigOfTheProject(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.sandbox = model.SandboxOff
	rig.reply("", exited)
	reads := 0
	rig.h.sandboxDefault = func(string) bool { reads++; return true }

	_, err := rig.run(t, agentFlow(`sandbox = None, `), "x")

	require.NoError(t, err)
	assert.Zero(t, reads, "the engine reads the live config only for a run with no stored mode")
	assert.Nil(t, sandboxSeen(t, rig))
}

func TestRun_TheRefusalOfASandboxNoneStepTellsTheOwnerHowToStartAgain(t *testing.T) {
	t.Parallel()

	for _, want := range []string{`sandbox = "off"`, "beehave cancel <item>", "beehave run <item>", "the run failed"} {
		assert.Contains(t, errUnsandboxed.Error(), want)
	}

	assert.NotContains(t, errUnsandboxed.Error(), "run again", "a failed run refuses beehave run until it is canceled")
	assert.NotContains(t, errUnsandboxed.Error(), "<run>", "beehave cancel takes an item, and a run id only with --run")
}

// runOldRecord stores and runs a run with no sandbox mode, as a record written before the mode was stored. It passes the run around the start check.
func runOldRecord(t *testing.T, rig *agentRig, flow string) (ItemResult, error) {
	t.Helper()

	in, err := pinnedInput(testItem, testProject, flowFile(t, flow, "x"))
	require.NoError(t, err)
	require.Empty(t, in.Sandbox)

	engine := rig.h.engine(t)
	handle, err := engine.joinOrStart(in)
	require.NoError(t, err)

	return engine.result(handle, testItem)
}

func TestRun_TheStartOfARunRefusesAnEmptySandboxMode(t *testing.T) {
	t.Parallel()

	for name, start := range map[string]func(*testing.T, *agentRig, string) error{
		"run with no option": func(t *testing.T, rig *agentRig, file string) error {
			t.Helper()

			_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

			return err
		},
		"run with an empty mode": func(t *testing.T, rig *agentRig, file string) error {
			t.Helper()

			_, err := rig.h.engine(t).RunFile(testItem, testProject, file, RunSandbox(""))

			return err
		},
		"join with no option": func(t *testing.T, rig *agentRig, file string) error {
			t.Helper()
			rig.h.engine(t)
			_, err := rig.h.client(t, testVersion).JoinRun(t.Context(), testItem, testProject, file)

			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)
			rig.reply("", exited)

			err := start(t, rig, flowFile(t, agentFlow(``), "x"))

			require.ErrorIs(t, err, errNoSandboxMode)
			assert.Empty(t, rig.h.agents.seen(), "no step of the run started")

			_, found, readErr := latestRun(rig.h.client(t, testVersion).flow, testItem)
			require.NoError(t, readErr)
			assert.False(t, found, "the store holds no run with an empty mode")
		})
	}
}

func TestJoinRun_AJoinFromAClientWithTheKeyUnsetStoresOffInAnEngineWhoseConfigIsOn(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	reads := 0
	rig.h.sandboxDefault = func(string) bool { reads++; return true }
	rig.h.engine(t)
	client := rig.h.client(t, testVersion)

	_, err := client.JoinRun(t.Context(), testItem, testProject, flowFile(t, agentFlow(`sandbox = None, `), "x"), RunSandbox(model.Project{}.RunSandboxMode()))

	require.NoError(t, err, "the client stores off, so the engine config that says on does not refuse the step")
	assert.Equal(t, model.SandboxOff, storedInput(t, client, testItem).Sandbox)
	assert.Zero(t, reads, "the engine never reads its own config for the run")
	assert.Len(t, rig.h.agents.seen(), 1)
}

func TestResume_ARunKeepsItsStoredModeAfterTheConfigOfTheProjectChanges(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	file := flowFile(t, bodyFlow("wait(name = \"ask\", question = \"go?\")\n"+agentCallText("sandbox = None, ")), "x")
	interrupted(t, rig.h, file, RunSandbox(model.Project{}.RunSandboxMode()))

	// The config of the project now says on, and the restarted engine and the next run both see it.
	rig.h.sandboxDefault = func(string) bool { return true }
	next := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file, RunSandbox(model.SandboxOn))
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the open run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	require.NoError(t, receive(t, done).err, "the run stored off when it started, so the None step runs")
	assert.Len(t, rig.h.agents.seen(), 1)
	assert.Equal(t, model.SandboxOff, storedInput(t, client, testItem).Sandbox)
}

func TestRun_ASandboxKeywordOfTheWrongTypeFailsInTheAgentStepAfterItsStartLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	rig := newAgentRig(t)
	rig.sandbox = model.SandboxOn
	rig.h.opts.Progress = &out
	rig.reply("", exited)

	_, err := rig.run(t, bodyFlow("bad = \"on\"\n"+agentCallText(`sandbox = bad, `)), "x")

	require.ErrorContains(t, err, "sandbox must be sandbox(...) or None", "the pre-check leaves the wrong type to the agent step, which names it")
	assert.Contains(t, out.String(), "step agent:a started", "the pre-check refuses only a None step")
	assert.Empty(t, rig.h.agents.seen())
}

func TestRun_TheRunDoesNotPrintAStartLineForAStepThatTheRefusalStopped(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		mode    model.SandboxMode
		refused bool
	}{"mode on": {model.SandboxOn, true}, "mode off": {model.SandboxOff, false}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			rig := newAgentRig(t)
			rig.sandbox = test.mode
			rig.h.opts.Progress = &out
			rig.reply("", exited)

			_, err := rig.run(t, agentFlow(`sandbox = None, `), "x")

			if test.refused {
				require.Error(t, err)
				assert.NotContains(t, out.String(), "step agent:a started")
				assert.Contains(t, out.String(), "step agent:a failed")
			} else {
				require.NoError(t, err)
				assert.Contains(t, out.String(), "step agent:a started")
			}
		})
	}
}

func TestServe_ARunThatStartedWithTheSandboxOnRefusesASandboxNoneStepAfterARestart(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	file := flowFile(t, bodyFlow("wait(name = \"ask\", question = \"go?\")\n"+agentCallText("sandbox = None, ")), "x")
	interrupted(t, rig.h, file, RunSandbox(model.SandboxOn))

	// The restarted engine reads a config that now says off. The run keeps the mode in its record.
	next := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file, RunSandbox(model.SandboxOff))
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the open run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	require.ErrorContains(t, receive(t, done).err, "sets sandbox = None")
	assert.Empty(t, rig.h.agents.seen())
	assert.Equal(t, model.SandboxOn, storedInput(t, client, testItem).Sandbox)
}

func TestResume_AStepRefusedForItsSandboxIsRefusedAgainAndTheMessageNamesBeehaveRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.sandbox = model.SandboxOn
	rig.reply("", exited)
	engine := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	file := flowFile(t, agentFlow(`sandbox = None, `), "x")

	_, err := engine.RunFile(testItem, testProject, file, RunSandbox(model.SandboxOn))
	require.ErrorContains(t, err, "beehave run")

	_, err = resumeAndJoin(t, engine, client)

	require.ErrorContains(t, err, "sets sandbox = None")
	require.ErrorContains(t, err, "beehave run")
	assert.Empty(t, rig.h.agents.seen())
}

func TestJoinRun_ARunThatJoinsTheEngineKeepsTheModeOfTheClient(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.engine(t)
	client := rig.h.client(t, testVersion)

	// The engine holds no mode of its own: the client says on, and the None step is refused.
	_, err := client.JoinRun(t.Context(), testItem, testProject, flowFile(t, agentFlow(`sandbox = None, `), "x"), RunSandbox(model.SandboxOn))

	require.ErrorContains(t, err, "sets sandbox = None")
	assert.Equal(t, model.SandboxOn, storedInput(t, client, testItem).Sandbox)
	assert.Empty(t, rig.h.agents.seen())
}

func TestDryRun_AProjectWithTheSandboxOnRefusesASandboxNoneStep(t *testing.T) {
	t.Parallel()

	noneStep := bodyFlow("off = None\nagent(model = \"m\", prompt = \"a.md\", name = \"a\", sandbox = off)\n")

	for name, test := range map[string]struct {
		options []DryOption
		refused string
	}{
		"mode on":    {[]DryOption{WithSandbox(model.SandboxOn)}, "sets sandbox = None"},
		"mode off":   {[]DryOption{WithSandbox(model.SandboxOff)}, ""},
		"no option":  {nil, ""},
		"empty mode": {[]DryOption{WithSandbox("")}, errNoSandboxMode.Error()},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			file := dryFile(t, noneStep)
			require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "a.md"), []byte("hello\n"), 0o600))

			var out bytes.Buffer

			err := DryRun(t.Context(), file, "12", map[string]string{}, &out, test.options...)

			if test.refused != "" {
				require.Error(t, err)
				assert.Contains(t, out.String()+err.Error(), test.refused)
			} else {
				require.NoError(t, err, out.String())
			}
		})
	}
}
