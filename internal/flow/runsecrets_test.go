package flow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// The planted values are fakes. The AWS value has the shape of a secret access key, and the forge value has the shape of a GitHub token.
const (
	plantedAWS   = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	plantedForge = "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
)

// withRunSecrets sets the two planted values as variables of the run that the config marks as secret by name.
func withRunSecrets(h *harness) {
	h.opts.Env = map[string]string{"AWS_SECRET_ACCESS_KEY": plantedAWS, "GH_TOKEN": plantedForge}
}

func assertNoPlanted(t *testing.T, text, where string) {
	t.Helper()

	assert.NotContains(t, text, plantedAWS, where)
	assert.NotContains(t, text, plantedForge, where)
}

func TestRunSecrets_TheAgentStreamHoldsNeitherPlantedValue(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	withRunSecrets(harness)
	harness.agents.said = []agent.Said{{Text: "key " + plantedAWS}, {Call: true, Text: "Bash: curl -H " + plantedForge + " https://example.test"}}

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	got := &collected{}
	require.NoError(t, harness.client(t, testVersion).Follow(t.Context(), testItem, got.add))

	assert.Equal(t, []string{"key [redacted]", "Bash: curl -H [redacted] https://example.test"}, got.texts())
}

func TestRunSecrets_PublishAndProgressHoldNeitherPlantedValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	withRunSecrets(h)

	flow := `def worker(item):
    publish("note", {"text": "keys ` + plantedAWS + ` ` + plantedForge + `"}, name = "note")

def main(item):
    runs = spawn(items = ["21"], entry = "worker", name = "fan")
    gather(runs, name = "fan")
    p = progress(runs, "note", name = "peek")
    command(name = "log", run = ["log", p[0].value["text"]])
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow))
	require.NoError(t, err)

	ran := h.commands.ranBy(testItem)
	require.Len(t, ran, 1)
	assert.Equal(t, "log keys [redacted] [redacted]", ran[0])

	got, err := h.client(t, testVersion).Published("item-21-r1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assertNoPlanted(t, string(got[0].Value), "the published value in the store")
}

func TestRunSecrets_ASendFromAWorkflowHoldsNeitherPlantedValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	withRunSecrets(h)

	flow := `def child(item):
    send(to = "parent", topic = "t", value = "keys ` + plantedAWS + ` ` + plantedForge + `", name = "tell")

def main(item):
    k = spawn(items = ["21"], entry = "child", name = "fan")
    m = recv("t", timeout = "1m", name = "got")
    gather(k, name = "all")
    command(name = "got", run = ["got", m.value])
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow))
	require.NoError(t, err)

	require.Len(t, h.commands.ranBy(testItem), 1)
	assert.Equal(t, "got keys [redacted] [redacted]", h.commands.ranBy(testItem)[0])
}

func TestRunSecrets_ASendOfTheClientHoldsNeitherPlantedValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	withRunSecrets(h)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	done := startRun(t, engine, writeFlow(t, bodyFlow(`a = recv("go", timeout = "1m", name = "one")
command(name = "got", run = ["got", a.value])`)))
	stepRecorded(t, engine, "recv:one#1.deadline")

	_, err := client.Send(testItem, Outgoing{Topic: "go", Value: text(t, "keys "+plantedAWS+" "+plantedForge)})
	require.NoError(t, err)

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"got keys [redacted] [redacted]"}, h.commands.ranBy(testItem))
}

func TestRunSecrets_AStepErrorHoldsNeitherPlantedValueInTheStoreTheRunErrorOrTheLog(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	withRunSecrets(harness)
	harness.life.failing["claim"] = true
	harness.life.failErr = errors.New("the tracker refused " + plantedAWS + " and " + plantedForge)
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "[redacted]")
	assertNoPlanted(t, err.Error(), "the error of the run")

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	var texts []string

	for _, step := range steps {
		if step.Error != nil {
			texts = append(texts, step.Error.Error())
		}
	}

	assertNoPlanted(t, strings.Join(texts, "\n"), "the stored step error")

	log, err := os.ReadFile(filepath.Join(harness.opts.StateDir, logFileName))
	require.NoError(t, err)
	assertNoPlanted(t, string(log), "the log file")
}

func TestRunSecrets_TheRequestOfAnAgentPhaseCarriesTheRedactorOfTheEngine(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	withRunSecrets(harness)
	engine := harness.engine(t)

	request := engine.agentRequest(t.Context(), &attempt{}, PhaseInput{}, "", nil, nil, nil)

	require.NotNil(t, request.Redact)
	assert.Equal(t, "key [redacted]", request.Redact("key "+plantedAWS))
}

func TestRunSecrets_AValueShorterThanTheFloorStaysInTheText(t *testing.T) {
	t.Parallel()

	redact := runRedactor(map[string]string{"AWS_SECRET_ACCESS_KEY": "short", "PLAIN": plantedAWS, "GH_TOKEN": plantedForge})

	assert.Equal(t, "short "+plantedAWS+" [redacted]", redact("short "+plantedAWS+" "+plantedForge), "only a long value of a secret name is removed")
}
