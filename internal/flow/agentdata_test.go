package flow

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// readFrom is a prompt that names the data file after the words "Read from ".
const readFrom = "Read from {{ .data_file }}\n"

// dataPath returns the path that a rendered prompt names after the words "Read from ".
func dataPath(prompt string) string {
	_, rest, _ := strings.Cut(prompt, "Read from ")
	path, _, _ := strings.Cut(rest, "\n")

	return path
}

// dataSeen is what the fake agent found at the start of each run: the text of the data file that its prompt names, and the mode of the file.
type dataSeen struct {
	mu    sync.Mutex
	texts []string
	modes []os.FileMode
}

// watch makes the fake agent of the rig read the data file that its prompt names when it starts, then end with result.
func (d *dataSeen) watch(rig *agentRig, result func() agent.Result) {
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		path := dataPath(request.Prompt)
		raw, readErr := os.ReadFile(filepath.Clean(path))
		info, statErr := os.Stat(path)

		d.mu.Lock()
		defer d.mu.Unlock()

		if readErr == nil && statErr == nil {
			d.texts = append(d.texts, string(raw))
			d.modes = append(d.modes, info.Mode().Perm())
		}

		return result()
	}
}

func (d *dataSeen) all() ([]string, []os.FileMode) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.texts...), append([]os.FileMode(nil), d.modes...)
}

const dataValue = `data = {"base_reds": ["lint-a"], "base": "main"}, `

func TestAgent_TheDataKeywordIsWrittenToTheDataFileThatThePromptNames(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	seen := &dataSeen{}
	seen.watch(rig, func() agent.Result { return exited })

	_, err := rig.run(t, agentFlow(dataValue), readFrom)
	require.NoError(t, err)

	requests := rig.h.agents.seen()
	require.Len(t, requests, 1)

	assert.Equal(t, dataFile(rig.h.opts.StateDir, testItem, aStep), dataPath(requests[0].Prompt), "the prompt names the data file of the item and the step")
	assert.Equal(t, filepath.Join(rig.h.opts.StateDir, inputsFolder), filepath.Dir(filepath.Dir(dataPath(requests[0].Prompt))), "the file is in a folder of its own in the inputs folder of the state folder")
	assert.NotEqual(t, outputFile(rig.h.opts.StateDir, testItem, aStep), requests[0].DataFile, "the data file is not the output file")
	assert.Equal(t, dataPath(requests[0].Prompt), requests[0].DataFile, "the request names the file that the prompt names")

	texts, modes := seen.all()
	assert.Equal(t, []string{`{"base":"main","base_reds":["lint-a"]}`}, texts, "the agent finds the value as JSON when it starts")
	assert.Equal(t, []os.FileMode{0o600}, modes)
}

func TestAgent_ACallWithNoDataGetsNoDataFile(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(""), "Read from [{{ .data_file }}]\n")
	require.NoError(t, err)

	requests := rig.h.agents.seen()
	require.Len(t, requests, 1)
	assert.Contains(t, requests[0].Prompt, "Read from []", "the prompt gets an empty data_file")
	assert.Empty(t, requests[0].DataFile)
	assert.NoFileExists(t, dataFile(rig.h.opts.StateDir, testItem, aStep))
}

func TestAgent_DataThatIsNotADictIsRefused(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`data = ["lint-a"], `), readFrom)

	require.ErrorContains(t, err, "data must be a dict")
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgent_ADataFileThatCannotBeWrittenFailsThePhaseBeforeTheAgentStarts(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	engine := rig.h.engine(t)
	require.NoError(t, os.WriteFile(filepath.Join(rig.h.opts.StateDir, inputsFolder), nil, 0o600), "a file where the inputs folder goes")

	_, err := engine.RunFile(testItem, testProject, flowFile(t, agentFlow(dataValue), readFrom), modeOff())

	require.ErrorContains(t, err, "write the data file")
	assert.Empty(t, rig.h.agents.seen())
}

// The first run of the agent fails, and the data file is gone before the resume. The agent finds the recorded bytes at the recorded path, which is the path
// that the name function gives for the item and the step.
func TestAgent_AReplayWritesTheDataFileFromTheRecordedValue(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	seen := &dataSeen{}
	results := []agent.Result{{Outcome: agent.OutcomeExited, ExitCode: 1, Note: "the first run fails"}, exited}

	var mu sync.Mutex

	seen.watch(rig, func() agent.Result {
		mu.Lock()
		defer mu.Unlock()

		next := results[0]
		results = results[1:]

		return next
	})

	file := flowFile(t, agentFlow(dataValue), readFrom)
	first := rig.h.engine(t)

	_, err := first.RunFile(testItem, testProject, file, modeOff())
	require.Error(t, err, "the first run of the agent fails")

	recorded := dataFile(rig.h.opts.StateDir, testItem, aStep)
	require.NoError(t, os.Remove(recorded))
	require.NoError(t, first.Shutdown())

	next := rig.h.engine(t)

	_, err = resumeAndJoin(t, next, rig.h.client(t, testVersion))
	require.NoError(t, err)

	texts, _ := seen.all()
	assert.Equal(t, []string{`{"base":"main","base_reds":["lint-a"]}`, `{"base":"main","base_reds":["lint-a"]}`}, texts, "each start finds the recorded bytes")

	requests := rig.h.agents.seen()
	require.Len(t, requests, 2)
	assert.Equal(t, recorded, requests[1].DataFile, "the replay writes the path of the record")
	assert.FileExists(t, dataFile(next.dir, testItem, aStep), "the replay writes the file again")
}

// A replay whose phase never started builds the phase input from the recall: the data file is the one that the recorded prompt names, and the text is the recorded one.
func TestAgent_TheRecallTakesTheDataFileAndTheTextThatTheRenderStepRecorded(t *testing.T) {
	t.Parallel()

	recorded := promptData{File: "/state/inputs/7.agent_3Aa_231/data.json", Text: `{"base":"main"}`}

	raw, err := json.Marshal(recordData(recorded))
	require.NoError(t, err)

	var outputs map[string]any
	require.NoError(t, json.Unmarshal(raw, &outputs), "the replay reads the record as the store gives it back")

	a := agentArgs{file: filepath.Join(t.TempDir(), "outputs", "out.json"), dataFile: recorded.File, record: "{}"}
	require.NoError(t, a.recall(outputs))
	assert.Equal(t, recorded.File, a.dataFile)
	assert.Equal(t, recorded.Text, a.record)

	none := agentArgs{file: a.file, dataFile: "/computed.json", record: "{}"}
	require.NoError(t, none.recall(map[string]any{}))
	assert.Empty(t, none.record, "a record with no data is a call with no data")

	require.ErrorContains(t, a.recall(map[string]any{recordedPromptData: "{"}), "read the recorded data of the step")
}

func TestAgent_TheDataFileGetsMode0600AlsoWhenAnOlderFileHasAnotherMode(t *testing.T) {
	t.Parallel()

	engine := &Engine{dir: t.TempDir()}
	file := dataFile(engine.dir, "7", aStep)
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(t, os.WriteFile(file, []byte("old"), 0o600))
	require.NoError(t, os.Chmod(file, 0o200), "an older file that its owner cannot read")

	written, err := engine.writeData(PhaseInput{Item: "7", Call: aStep, Data: `{"a":1}`})
	require.NoError(t, err)
	require.Equal(t, file, written)

	info, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	text, err := os.ReadFile(filepath.Clean(file))
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(text))
}

func TestDataFile_HasAFolderOfItsOwnSoThatOneFolderHoldsOneFile(t *testing.T) {
	t.Parallel()

	file := dataFile("/state", "7", "agent:plan")

	assert.Equal(t, "data.json", filepath.Base(file))
	assert.NotEqual(t, filepath.Join("/state", inputsFolder), filepath.Dir(file), "the folder is not the shared inputs folder")
	assert.NotEqual(t, filepath.Dir(dataFile("/state", "7", "agent:implement")), filepath.Dir(file), "two steps never share a folder")
	assert.NotEqual(t, filepath.Dir(dataFile("/state", "8", "agent:plan")), filepath.Dir(file), "two items never share a folder")
}

// otherDataFiles are data paths that are not the data file of item 12 and step agent:a#1 under the state folder state, by the case they stand for.
func otherDataFiles(state string) map[string]string {
	return map[string]string{
		"the file of the shared inputs folder that an older build wrote": filepath.Join(state, inputsFolder, "12.agent_3Aa_231.json"),
		"a data.json in the shared inputs folder":                        filepath.Join(state, inputsFolder, "data.json"),
		"a data.json in a shared folder of two files":                    filepath.Join(state, inputsFolder, "shared", "data.json"),
		"the folder of another item":                                     filepath.Join(state, inputsFolder, "13.agent_3Aa_231", "data.json"),
		"the folder of another step":                                     filepath.Join(state, inputsFolder, "12.agent_3Ab_231", "data.json"),
		"the file of the item and the step under another state folder":   filepath.Join(filepath.Dir(state), "moved", inputsFolder, "12.agent_3Aa_231", "data.json"),
	}
}

// renameInRender replaces from with to in the recorded render step of the agent call of the run, as an edited or older record would hold it.
func renameInRender(t *testing.T, h *harness, from, to string) {
	t.Helper()

	db, render := sqlOf(t, h), aStep+"/render"

	var stored string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT output FROM operation_outputs WHERE workflow_uuid = ? AND function_name = ?", testRun, render).Scan(&stored))

	raw, err := base64.StdEncoding.DecodeString(stored)
	require.NoError(t, err)
	require.Contains(t, string(raw), from, "the record names the data file")

	renamed := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(string(raw), from, to)))
	_, err = db.ExecContext(t.Context(), "UPDATE operation_outputs SET output = ? WHERE workflow_uuid = ? AND function_name = ?", renamed, testRun, render)
	require.NoError(t, err)
}

// failedDataRun runs the data flow of the rig once with an agent that fails, so the run stays open for a resume, and returns the data file that the run used.
func failedDataRun(t *testing.T, rig *agentRig) string {
	t.Helper()

	rig.h.agents.result = func(AgentRequest) agent.Result {
		return agent.Result{Outcome: agent.OutcomeExited, ExitCode: 1, Note: "the first run fails"}
	}

	first := rig.h.engine(t)
	_, err := first.RunFile(testItem, testProject, flowFile(t, agentFlow(dataValue), readFrom), modeOff())
	require.Error(t, err, "the first run of the agent fails")
	require.NoError(t, first.Shutdown())

	requests := rig.h.agents.seen()
	require.Len(t, requests, 1)
	require.Equal(t, dataFile(rig.h.opts.StateDir, testItem, aStep), requests[0].DataFile, "a fresh call gets the data file of the item and the step")

	rig.h.agents.result = func(AgentRequest) agent.Result { return exited }

	return requests[0].DataFile
}

func TestAgent_AReplayWhoseRecordNamesAnotherDataFileStopsTheCall(t *testing.T) {
	t.Parallel()

	for name := range otherDataFiles("/state") {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)
			derived := failedDataRun(t, rig)
			other := otherDataFiles(rig.h.opts.StateDir)[name]
			renameInRender(t, rig.h, derived, other)

			_, err := resumeAndJoin(t, rig.h.engine(t), rig.h.client(t, testVersion))

			require.ErrorContains(t, err, "the recorded prompt names the data file "+other+", and the data file of this item and step is "+derived)
			require.ErrorContains(t, err, "Start a new run")
			assertOnlyDataFile(t, rig.h.agents.seen()[1:], derived)
			assert.NoFileExists(t, other)
		})
	}
}

// assertOnlyDataFile asserts that each agent start of a replay names the data file derived. The recovered phase of the first run can start before
// the stop of the replay ends its claim, and it derives its own path.
func assertOnlyDataFile(t *testing.T, requests []AgentRequest, derived string) {
	t.Helper()

	for _, request := range requests {
		assert.Equal(t, derived, request.DataFile, "an agent start of the replay opens only the data file that the engine derives")
	}
}

func TestAgent_AReplayUnderAnotherStateFolderStops(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	recorded := failedDataRun(t, rig)
	moved := t.TempDir()
	require.NoError(t, os.Remove(recorded))

	_, err := resumeAndJoin(t, rig.h.engineWith(t, func(e *Engine) { e.dir = moved }), rig.h.client(t, testVersion))

	require.ErrorContains(t, err, "the recorded prompt names the data file "+recorded+", and the data file of this item and step is "+dataFile(moved, testItem, aStep))
	require.ErrorContains(t, err, "Start a new run")
	assertOnlyDataFile(t, rig.h.agents.seen()[1:], dataFile(moved, testItem, aStep))
	assert.NoFileExists(t, recorded, "the file of the first run was removed, and the replay does not write it again")
}

// queuePhase queues a phase on the agents queue of the engine, as DBOS recovers a stored one, and waits for its end.
func queuePhase(t *testing.T, engine *Engine, in PhaseInput) error {
	t.Helper()

	holdClaim(engine, in.Run, claimHold{renewed: time.Now()})

	handle, err := dbos.RunWorkflow(engine.ctx, engine.phase, in, dbos.WithQueue(engine.queue), dbos.WithQueuePartitionKey(in.Login))
	require.NoError(t, err)

	_, err = handle.GetResult()

	return err
}

// storedPhase is the stored input of a phase of item 12 with data, as an older build wrote it: it holds the data file that build recorded.
func storedPhase(t *testing.T, call, recorded string) PhaseInput {
	t.Helper()

	stored, err := json.Marshal(map[string]string{
		"run": "item-12-r1", "call": call, "item": testItem, "project": testProject, "login": testLogin, "prompt": "Read the data", "data": `{"k":1}`, "data_file": recorded,
	})
	require.NoError(t, err)

	var in PhaseInput
	require.NoError(t, json.Unmarshal(stored, &in), "a stored input that holds data_file decodes")

	return in
}

func TestPhase_AQueuedPhaseWritesAndOpensOnlyTheDataFileOfItsItemAndStep(t *testing.T) {
	t.Parallel()

	for name := range otherDataFiles("/state") {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			engine := launchedWith(t, harness, func(*Deps) {})
			state := harness.opts.StateDir
			recorded := otherDataFiles(state)[name]

			require.NoError(t, queuePhase(t, engine, storedPhase(t, aStep, recorded)))

			derived := filepath.Join(state, inputsFolder, "12.agent_3Aa_231", "data.json")
			requests := harness.agents.seen()
			require.Len(t, requests, 1)
			assert.Equal(t, derived, requests[0].DataFile, "the agent opens the data file that the engine derives")
			assert.NoFileExists(t, recorded, "the phase writes nothing at the recorded path")

			text, err := os.ReadFile(filepath.Clean(derived))
			require.NoError(t, err)
			assert.JSONEq(t, `{"k":1}`, string(text))
		})
	}
}

func TestPhase_AQueuedPhaseWithDataAndNoStepNameStartsNoAgent(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := launchedWith(t, harness, func(*Deps) {})
	recorded := filepath.Join(harness.opts.StateDir, inputsFolder, "12.agent_3Aa_231.json")

	err := queuePhase(t, engine, storedPhase(t, "", recorded))

	require.ErrorContains(t, err, "the agent phase of item 12 has data and no step name")
	require.ErrorContains(t, err, "Start a new run")
	assert.Empty(t, harness.agents.seen())
	assert.NoFileExists(t, recorded)
}
