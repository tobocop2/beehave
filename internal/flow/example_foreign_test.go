package flow

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests call foreign_followup of the example file directly. No entry of the file reaches it while the engine refuses every foreign pull request,
// so a test adds the entry `followup` to its own copy. The entry passes the value that a foreign review recorded for the pull request 41.
const followupEntry = `

def followup(item):
    return foreign_followup(json.decode(item.recorded), facts())
`

// foreignPull is the pull request 41 of another author. Its number is all that the follow-up reads.
const foreignPull = 41

// foreignFindings are the two findings that the foreign review recorded as open.
const foreignFindings = `[{"class":"blocking","text":"the retry never stops"},{"class":"should-fix","text":"the log names the token"}]`

// foreignIssue is the issue that the follow-up files for the pull request 41 of the repository o/r with foreignFindings: project|key|title|body|parent.
const foreignIssue = testProject + "|foreign:o/r:41|Open review findings of the merged pull request #41|" +
	"Pull request #41 merged with these review findings open:\n\n- blocking: the retry never stops\n- should-fix: the log names the token\n|"

// foreignFakes are the fakes of a follow-up test: the forge, the tracker and the preflight reader.
type foreignFakes struct {
	queue     *fakeQueue
	writer    *fakeWriter
	preflight *fakePreflight
}

// foreignRig is the example rig with the entry followup, a forge that holds the pull request 41 in the state `state`, a tracker that files items, and the round base integration.
func foreignRig(t *testing.T, state string) (*agentRig, foreignFakes, string) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	text, err := os.ReadFile(file) //nolint:gosec // the path is the example copy that this test made in its own folder
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, append(text, followupEntry...), 0o600)) //nolint:gosec // the example copy of this test

	fakes := foreignFakes{queue: &fakeQueue{states: map[int]string{foreignPull: state}}, writer: newFakeWriter(), preflight: &fakePreflight{}}
	rig.h.queue, rig.h.writer, rig.h.preflight = fakes.queue, fakes.writer, fakes.preflight
	rig.setRound("base", "integration")

	return rig, fakes, file
}

// runFollowup runs the entry followup once with the recorded value `recorded` as the item field recorded.
func runFollowup(t *testing.T, rig *agentRig, file, recorded string) (ItemResult, error) {
	t.Helper()

	rig.h.items.fields = map[string]string{"recorded": recorded}

	return rig.h.engine(t).runEntry(testItem, testProject, file, "followup", modeOff())
}

// recordedValue is the value that a foreign review of the pull request 41 records, with the findings `findings` (a JSON list).
func recordedValue(findings string) string { return recordedOf(foreignPull, findings) }

// recordedOf is the value that a foreign review of the pull request pr records, with the findings `findings`.
func recordedOf(pr int, findings string) string {
	return `{"item":"` + testItem + `","head":"","approved":false,"reason":"foreign","pr":` + strconv.Itoa(pr) + `,"findings":` + findings + `}`
}

// createdKeys is the key of each item that the writer filed, in order.
func createdKeys(w *fakeWriter) []string {
	keys := make([]string, 0, len(w.created))
	for _, line := range w.created {
		keys = append(keys, strings.Split(line, "|")[1])
	}

	return keys
}

func TestExampleForeign_TwoPullRequestsOfOneRepositoryFileTwoIssues(t *testing.T) {
	t.Parallel()

	const other = 412

	rig, fakes, file := foreignRig(t, "MERGED")
	fakes.queue.states[other] = "MERGED"
	engine := rig.h.engine(t)

	for _, pr := range []int{foreignPull, other} {
		rig.h.items.fields = map[string]string{"recorded": recordedOf(pr, foreignFindings)}
		_, err := engine.runEntry(testItem, testProject, file, "followup", modeOff())
		require.NoError(t, err)
	}

	assert.Equal(t, []string{"foreign:o/r:41", "foreign:o/r:412"}, createdKeys(fakes.writer), "the number is in the key, so 412 does not find the issue of 41")
}

func TestExampleForeign_OnePullRequestNumberInTwoRepositoriesFilesTwoIssues(t *testing.T) {
	t.Parallel()

	rig, fakes, file := foreignRig(t, "MERGED")
	rig.h.items.fields = map[string]string{"recorded": recordedValue(foreignFindings)}
	engine := rig.h.engine(t)

	for _, repo := range []string{"o/r", "o2/r"} {
		rig.project.Repo = repo
		_, err := engine.runEntry(testItem, testProject, file, "followup", modeOff())
		require.NoError(t, err)
	}

	assert.Equal(t, []string{"foreign:o/r:41", "foreign:o2/r:41"}, createdKeys(fakes.writer), "the repository is in the key")
}

func TestExampleForeign_TheFollowUpReadsTheRecordedPullRequestAndFilesOneIssue(t *testing.T) {
	t.Parallel()

	rig, fakes, file := foreignRig(t, "MERGED")

	result, err := runFollowup(t, rig, file, recordedValue(foreignFindings))
	require.NoError(t, err)

	assert.Equal(t, []int{foreignPull}, fakes.queue.stateAsked, "the step reads the state of the recorded number, once")
	assert.Equal(t, []string{foreignIssue}, fakes.writer.created, "one issue, keyed by the repository and the number, lists each finding by its class")
	assert.Empty(t, rig.h.forge.opened, "the follow-up opens no pull request of its own")
	assert.Empty(t, fakes.preflight.asked, "the follow-up does not look the pull request up again")

	var id string

	require.NoError(t, json.Unmarshal(result.Value, &id))
	assert.Equal(t, "n-1", id, "the follow-up returns the id of the issue it filed")
}

func TestExampleForeign_ASecondRunForTheSamePullRequestFilesNoSecondIssue(t *testing.T) {
	t.Parallel()

	rig, fakes, file := foreignRig(t, "MERGED")

	rig.h.items.fields = map[string]string{"recorded": recordedValue(foreignFindings)}
	engine := rig.h.engine(t)

	first, err := engine.runEntry(testItem, testProject, file, "followup", modeOff())
	require.NoError(t, err)

	second, err := engine.runEntry(testItem, testProject, file, "followup", modeOff())
	require.NoError(t, err)

	assert.Equal(t, []string{foreignIssue}, fakes.writer.created, "two runs file one issue")
	assert.JSONEq(t, string(first.Value), string(second.Value), "the second run returns the id of the first issue")
}

func TestExampleForeign_AFindingWithAnAttributionLineIsRefusedBeforeThePost(t *testing.T) {
	t.Parallel()

	rig, fakes, file := foreignRig(t, "MERGED")

	_, err := runFollowup(t, rig, file, recordedValue(`[{"class":"nit","text":"Co-Authored-By: Claude"}]`))

	require.Error(t, err)
	assert.Empty(t, fakes.writer.created, "the follow-up posts no attribution line")
}

func TestExampleForeign_AReviewWithNoOpenFindingsFilesNothing(t *testing.T) {
	t.Parallel()

	rig, fakes, file := foreignRig(t, "MERGED")

	result, err := runFollowup(t, rig, file, recordedValue("[]"))
	require.NoError(t, err)

	assert.Empty(t, fakes.writer.created, "a review with no open findings files nothing")
	assert.Empty(t, fakes.queue.stateAsked, "a review with no open findings reads no state")
	assert.JSONEq(t, `""`, string(result.Value))
}

func TestExampleForeign_APullRequestThatHasNotMergedFilesNothing(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"OPEN", "CLOSED"} {
		t.Run(strings.ToLower(state), func(t *testing.T) {
			t.Parallel()

			rig, fakes, file := foreignRig(t, state)

			_, err := runFollowup(t, rig, file, recordedValue(foreignFindings))
			require.NoError(t, err)

			assert.Equal(t, []int{foreignPull}, fakes.queue.stateAsked)
			assert.Empty(t, fakes.writer.created, "only a merge leaves the findings with no pull request to hold them")
		})
	}
}
