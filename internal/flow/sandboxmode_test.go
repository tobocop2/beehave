package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	last, found, err := latestRun(client.store(), item)
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
	spawner := "def worker(item):\n    command(name = \"w\", run = [\"w\"])\n\ndef main(item):\n    gather(spawn(items = [\"21\"], entry = \"worker\"))\n"

	_, err := runOldRecord(t, rig, spawner)
	require.NoError(t, err)

	assert.Equal(t, model.SandboxOff, storedInput(t, rig.h.client(t, testVersion), "21").Sandbox, "the child stores off, the mode of an old parent, never the empty mode")
}

func TestResume_ARunWithNoRecordedSandboxModeKeepsRunningItsStepsUnsandboxed(t *testing.T) {
	t.Parallel()

	var old ItemInput

	require.NoError(t, json.Unmarshal([]byte(`{"item":"12","project":"p"}`), &old))
	assert.Empty(t, old.Sandbox, "an input stored before the field has no mode")

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := runOldRecord(t, rig, agentFlow(`sandbox = None, `))

	require.NoError(t, err, "a run with no stored mode runs with the mode off, so the None step starts")
	assert.Nil(t, sandboxSeen(t, rig))
}

func TestResume_ARunWithNoRecordedSandboxModeRunsAStepWithNoKeywordWithNoSandbox(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := runOldRecord(t, rig, agentFlow(``))

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 1)
	assert.Nil(t, sandboxSeen(t, rig), "a run recorded before the mode was stored keeps running with no sandbox")
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

// refusedStartBound bounds the return of a refused start. The refusal comes before any run is stored, so it returns at once.
const refusedStartBound = 30 * time.Second

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
		"an entry run with no option": func(t *testing.T, rig *agentRig, file string) error {
			t.Helper()

			in, err := pinnedInput(testItem, testProject, file)
			if err != nil {
				return err
			}

			in.Entry = "main"
			_, err = rig.h.engine(t).run(in)

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

			file := flowFile(t, agentFlow(``), "x")
			done := make(chan error, 1)

			go func() { done <- start(t, rig, file) }()

			var err error

			select {
			case err = <-done:
			case <-time.After(refusedStartBound):
				require.FailNow(t, "a refused start must return at once and leave nothing to wait for")
			}

			require.ErrorIs(t, err, errNoSandboxMode)
			assert.Empty(t, rig.h.agents.seen(), "no step of the run started")

			_, found, readErr := latestRun(rig.h.client(t, testVersion).store(), testItem)
			require.NoError(t, readErr)
			assert.False(t, found, "the store holds no run with an empty mode")
		})
	}
}

func TestResume_ARunKeepsItsStoredModeAfterTheConfigOfTheProjectChanges(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	file := flowFile(t, bodyFlow("wait(name = \"ask\", question = \"go?\")\n"+agentCallText("sandbox = None, ")), "x")
	interrupted(t, rig.h, file, RunSandbox(model.SandboxOff))

	// The config of the project now says on, and the next run passes that mode.
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

// offLines counts the off lines in the progress text.
func offLines(text string) int { return strings.Count(text, model.SandboxOffLine) }

// launchMode builds and launches an engine of the given mode over the harness. Its progress goes to out. stop ends it as a crash would.
func launchMode(t *testing.T, h *harness, mode string, out *safeBuffer) (engine *Engine, stop func()) {
	t.Helper()

	opts := h.opts
	opts.Mode, opts.Progress = mode, out

	stopCtx, cancel := context.WithCancel(t.Context())
	engine, err := NewEngine(stopCtx, opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	return engine, func() {
		cancel()
		require.NoError(t, engine.Shutdown())
	}
}

// askingFlow is a workflow file that stops at a question, so its run stays open.
func askingFlow(t *testing.T) string {
	t.Helper()

	return writeFlow(t, bodyFlow("wait(name = \"ask\", question = \"go?\")\n"))
}

// interruptedOldRecord starts a run with no stored sandbox mode, as an older build did, until it asks its question, then stops the engine.
func interruptedOldRecord(t *testing.T, h *harness, file string) {
	t.Helper()

	var out safeBuffer

	engine, stop := launchMode(t, h, ModeRun, &out)
	client := h.client(t, testVersion)

	in, err := pinnedInput(testItem, testProject, file)
	require.NoError(t, err)
	require.Empty(t, in.Sandbox)

	_, err = engine.joinOrStart(in)
	require.NoError(t, err)

	waitOn(t, waiting[struct{}]{what: "the run to show a question", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		rows, err := client.Status()

		return struct{}{}, err == nil && len(rows) == 1 && rows[0].Question != "", nil
	}})

	stop()
}

func TestServe_TheEngineSaysTheOffLineAtItsStartForAnOpenRunThatStoredOffAndForNoOtherRun(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		stored model.SandboxMode
		lines  int
	}{
		"a stored off":    {model.SandboxOff, 1},
		"a stored on":     {model.SandboxOn, 0},
		"an empty record": {"", 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			file := askingFlow(t)

			if test.stored == "" {
				interruptedOldRecord(t, h, file)
			} else {
				interrupted(t, h, file, RunSandbox(test.stored))
			}

			var out safeBuffer

			_, stop := launchMode(t, h, ModeServe, &out)
			stop()

			assert.Equal(t, test.lines, offLines(out.String()), out.String())
		})
	}
}

func TestServe_AnEngineOfRunSaysNoOffLineWhenItResumesARunThatStoredOff(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	interrupted(t, h, askingFlow(t), RunSandbox(model.SandboxOff))

	var out safeBuffer

	_, stop := launchMode(t, h, ModeRun, &out)
	stop()

	assert.Zero(t, offLines(out.String()), out.String())
}

func TestServe_EachRestartSaysTheOffLineOnceForTheRunItResumes(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	interrupted(t, h, askingFlow(t), RunSandbox(model.SandboxOff))

	for range 2 {
		var out safeBuffer

		_, stop := launchMode(t, h, ModeServe, &out)
		stop()

		assert.Equal(t, 1, offLines(out.String()), out.String())
	}
}

func TestServe_ARunStartedAfterTheEngineStartedSaysNoOffLineNeitherItsChildNorAJoinedRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var out safeBuffer

	engine, stop := launchMode(t, h, ModeServe, &out)
	spawner := "def worker(item):\n    command(name = \"w\", run = [\"w\"])\n\ndef main(item):\n    gather(spawn(items = [\"21\"], entry = \"worker\"))\n"

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, spawner), RunSandbox(model.SandboxOff))
	require.NoError(t, err)

	// A project with sandbox = "off" runs with the mode off, and a joined run of it stores off too.
	_, err = h.client(t, testVersion).JoinRun(t.Context(), "23", testProject, writeFlow(t, bodyFlow("command(name = \"w\", run = [\"w\"])\n")), RunSandbox(model.SandboxOff))
	require.NoError(t, err)

	assert.Equal(t, model.SandboxOff, storedInput(t, h.client(t, testVersion), "21").Sandbox, "the child stores off")
	assert.Equal(t, model.SandboxOff, storedInput(t, h.client(t, testVersion), "23").Sandbox, "the joined run stores off")

	stop()

	assert.Zero(t, offLines(out.String()), out.String())
}

func TestOpenRun_ReadsTheModeAndFileOfTheOpenRunOnly(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	client := h.client(t, testVersion)

	open, err := client.OpenRun(testItem)
	require.NoError(t, err)
	assert.Equal(t, OpenRun{}, open, "an item with no run has no open run")

	file := askingFlow(t)
	interrupted(t, h, file, RunSandbox(model.SandboxOn))

	open, err = client.OpenRun(testItem)
	require.NoError(t, err)
	assert.Equal(t, OpenRun{Open: true, File: file, Sandbox: model.SandboxOn}, open, "the open run gives its stored mode")

	require.NoError(t, client.Cancel(testItem))

	open, err = client.OpenRun(testItem)
	require.NoError(t, err)
	assert.False(t, open.Open, "a run that ended is not open")
}

func TestOpenRun_AnOldRecordUsesTheModeOff(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	interruptedOldRecord(t, h, askingFlow(t))

	open, err := h.client(t, testVersion).OpenRun(testItem)
	require.NoError(t, err)
	assert.Equal(t, model.SandboxOff, open.Sandbox, "a run recorded before the mode was stored runs with the mode off")
}

func TestServe_AnEngineThatCannotListItsOpenRunsFailsItsLaunch(t *testing.T) {
	t.Parallel()

	var out safeBuffer

	h := newHarness(t)
	opts := h.opts
	opts.Mode, opts.Progress = ModeServe, &out

	engine, err := NewEngine(t.Context(), opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	require.ErrorContains(t, engine.sayResumedOff(), "list the open runs")
	require.ErrorContains(t, engine.Launch(), "list the open runs", "the launch of the engine fails with it")
}
