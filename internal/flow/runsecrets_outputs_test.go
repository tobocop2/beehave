package flow

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logText returns the log file of the state folder of the harness.
func logText(t *testing.T, h *harness) string {
	t.Helper()

	text, err := os.ReadFile(filepath.Join(h.opts.StateDir, logFileName))
	require.NoError(t, err)

	return string(text)
}

// storedSteps returns the recorded steps of a run as text: the output and the error of each.
func storedSteps(t *testing.T, engine *Engine, run string) string {
	t.Helper()

	steps, err := dbos.GetWorkflowSteps(engine.ctx, run)
	require.NoError(t, err)

	var texts []string

	for _, step := range steps {
		texts = append(texts, fmt.Sprint(step.StepName, " ", step.Output, " ", step.Error))
	}

	return strings.Join(texts, "\n")
}

func TestRunSecrets_TheStoredOutputOfACommandHoldsNeitherPlantedValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	withRunSecrets(h)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{Stdout: "out " + plantedAWS, Stderr: "err " + plantedForge}, nil
	}
	h.flowIs(t, bodyFlow("command(run = [\"tool\"], name = \"c\")\n"))
	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	stored := storedSteps(t, engine, testRun)

	assert.Contains(t, stored, "out [redacted]", "the control: the step output is in the store")
	assertNoPlanted(t, stored, "the stored steps of the run")
}

func TestRunSecrets_TheQuestionOfAWaitAndTheAnswerOfTheOwnerHoldNeitherPlantedValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	withRunSecrets(h)
	h.facts = notifyFacts(notifyProgram)
	client, done := openedWait(t, h, `def main(item):
    answer = wait(name = "ask", question = "go with `+plantedAWS+`?", timeout = "1h").answer
    command(name = "got", run = ["got", answer])
`)

	assert.Equal(t, "go with [redacted]?", questionOf(t, h, testItem), "status shows the redacted question")

	require.NoError(t, client.Answer(testItem, "use "+plantedForge))
	require.NoError(t, receive(t, done).err)

	assert.Contains(t, h.commands.ranBy(testItem), "got use [redacted]", "the run reads the redacted answer")
}

func TestRunSecrets_TheQuestionAndTheAnswerOfAWaitThatAsksPeopleHoldNeitherPlantedValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	withRunSecrets(h)
	h.facts = notifyFacts(notifyProgram)
	client, done := openedWait(t, h, `def main(item):
    heard = wait(name = "ask", question = "go with `+plantedAWS+`?", timeout = "1h", to = ["alice"])
    command(name = "got", run = ["got", heard.answers["alice"]])
`)

	assert.Equal(t, "go with [redacted]?", questionOf(t, h, testItem), "status shows the redacted question")

	require.NoError(t, client.AnswerAs(testItem, "alice", "use "+plantedForge))
	require.NoError(t, receive(t, done).err)

	assert.Contains(t, h.commands.ranBy(testItem), "got use [redacted]")
}

func TestRunSecrets_TheLogFileOfTheEngineHoldsNeitherPlantedValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	withRunSecrets(h)
	engine := h.engine(t)

	engine.logger.Info("planted "+plantedAWS, "value", plantedForge)

	text := logText(t, h)

	assert.Contains(t, text, "[redacted]", "the control: the record is in the log")
	assertNoPlanted(t, text, "the log file")
}

func TestRunSecrets_TheLogFileOfTheClientHoldsNeitherPlantedValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	withRunSecrets(h)
	client := h.client(t, testVersion)

	client.logger.Info("planted "+plantedAWS, "value", plantedForge)

	text := logText(t, h)

	assert.Contains(t, text, "[redacted]", "the control: the record is in the log")
	assertNoPlanted(t, text, "the log file")
}

func TestRunSecrets_TheTerminalLineOfAFailedStepHoldsNeitherPlantedValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	withRunSecrets(h)

	var terminal safeBuffer

	h.opts.Progress = &terminal
	engine := h.engine(t)

	engine.progress.ended(testRun, "command:c", time.Second, errors.New("failed with "+plantedAWS+" and "+plantedForge))

	assert.Contains(t, terminal.String(), "failed with [redacted] and [redacted]")
	assertNoPlanted(t, terminal.String(), "the terminal")
}

func TestRunSecrets_AnEscapedFormOfASecretIsRemovedFromAStepError(t *testing.T) {
	t.Parallel()

	const sample = `pa"ss<w>ord&ABCDEFGH`

	h := newHarness(t)
	h.opts.Env = map[string]string{"AWS_SECRET_ACCESS_KEY": plantedAWS, "DB_PASSWORD": sample}
	engine := h.engine(t)

	err := engine.scrub(fmt.Errorf("get ?k=%s failed, body {\"p\": \"pa\\\"ss<w>ord&ABCDEFGH\"}", url.QueryEscape(plantedAWS)))

	assert.NotContains(t, err.Error(), url.QueryEscape(plantedAWS), "the URL-escaped form")
	assert.NotContains(t, err.Error(), `pa\"ss`, "the JSON-escaped form")
}

func TestRunSecrets_AWarningNamesEachSecretVariableThatIsTooShortToRemoveAndNeverItsValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"DB_PASSWORD": "hunter2", "AWS_SECRET_ACCESS_KEY": plantedAWS, "PATH": "/usr/bin"}
	h.engine(t)

	text := logText(t, h)

	assert.Contains(t, text, "DB_PASSWORD")
	assert.NotContains(t, text, "hunter2", "the warning never holds the value")
	assert.NotContains(t, text, "AWS_SECRET_ACCESS_KEY", "a value of 8 bytes or more is removed, so it needs no warning")
	assert.Equal(t, 1, strings.Count(text, "level=WARN"))
}
