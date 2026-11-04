package flow

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
)

// settleMarker is in example_lane_test.go. parkedRig runs the example to the ruling of the owner, who parks every finding, and makes the settle agent write parked.
func parkedRig(t *testing.T, parked string) (*agentRig, *fakeWriter, runOutcome) {
	t.Helper()

	return parkedRigSeeded(t, parked, nil)
}

// parkedRigSeeded is parkedRig with a writer that already holds the keys of an earlier attempt of the same run.
func parkedRigSeeded(t *testing.T, parked string, seed map[string]string) (*agentRig, *fakeWriter, runOutcome) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	writer := newFakeWriter()
	maps.Copy(writer.keys, seed)
	rig.h.writer = writer

	plain := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		done := plain(request)

		if strings.Contains(request.Prompt, settleMarker) {
			text, err := json.Marshal(map[string]string{"parked": parked})
			require.NoError(t, err)
			rig.put(request.OutputFile, string(text))
		}

		return done
	}

	done := runInBackground2(rig.h.engine(t), file)

	waitForTheOwner(t, rig.h)
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "park all"))

	return rig, writer, <-done
}

const parkedOne = `[{"id":"retry-loop","title":"The retry loop never stops","body":"## Problem\nIt spins.","found":"Run go test ./retry at commit abc: it hangs.","severity":"P1","operator":true}]`

func TestExampleFiling_AParkedFindingIsFiledOnceWithTheKeyOfItsIdTheTemplateHowItWasFoundAndNoAssignee(t *testing.T) {
	t.Parallel()

	_, writer, outcome := parkedRig(t, parkedOne)

	require.NoError(t, outcome.err)
	assert.Equal(t, []string{testProject + "|" + testRun + ":finding-retry-loop|The retry loop never stops|## Problem\nIt spins.\n\n## How it was found\n\nRun go test ./retry at commit abc: it hangs.|"}, writer.created)
	assert.Equal(t, []string{testRun + ":finding-retry-loop|"}, writer.assignees, "no assignee")
	assert.Equal(t, []string{testRun + ":finding-retry-loop|P1"}, writer.severities, "one severity label")
	assert.Equal(t, []string{testRun + ":finding-retry-loop|true"}, writer.operators, "the operator flag of the finding becomes the operator label")
}

func TestExampleFiling_AReplayWithTheSameRunIDFilesNothingNew(t *testing.T) {
	t.Parallel()

	_, writer, outcome := parkedRigSeeded(t, parkedOne, map[string]string{testRun + ":finding-retry-loop": "n-1"})

	require.NoError(t, outcome.err)
	assert.Empty(t, writer.created, "the key of the first attempt is found, so no second issue is filed")
}

func TestExampleFiling_TheMergeRefusesATextThatNamesTheTrackerIDOfTheItemAndAllowsAGitHubNumber(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		item, body, refusal string
	}{
		"a tracker id in the body":        {"bb-12", "Fixes the loop.\nSee bb-12 for more.", "2:See bb-12 for more."},
		"the id in another case":          {"bb-12", "see BB-12", "1:see BB-12"},
		"a longer id is another id":       {"bb-12", "see bb-123", ""},
		"a GitHub number is allowed":      {"12", "Addresses #12", ""},
		"a body without the id is merged": {"bb-12", "Fixes the loop.", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, file, world := mergingExample(t, func(head string) *shipWorld {
				return &shipWorld{pull: shipPull(t, head, map[string]any{"body": tc.body})}
			})

			rig.h.items.fields["id"] = tc.item

			_, err := rig.h.engine(t).RunFile(tc.item, testProject, file)

			if tc.refusal == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, errMergeRefused)
			require.ErrorContains(t, err, tc.refusal)
			assert.Empty(t, world.writes)
		})
	}
}

func TestExampleChangelog_TheMergeRefusesAChangeThatLeavesTheDeclaredChangelogAlone(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		changelog, changed, refusal string
	}{
		"no changelog declared":                  {"", "a.go\n", ""},
		"the declared changelog is in the diff":  {"CHANGELOG.md", "a.go\nCHANGELOG.md\n", ""},
		"the declared changelog is not in diff":  {"CHANGELOG.md", "a.go\ndocs/CHANGELOG.md\n", "CHANGELOG.md"},
		"an empty diff leaves the changelog out": {"CHANGELOG.md", "", "CHANGELOG.md"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, file, world := mergingExample(t, func(head string) *shipWorld {
				return &shipWorld{pull: shipPull(t, head, nil)}
			})

			rig.h.facts = func(project string) model.Project {
				return model.Project{Name: project, Changelog: tc.changelog}
			}
			rig.h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{Stdout: tc.changed}, nil }

			_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

			if tc.refusal == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, "the project declares the changelog "+tc.changelog)
			assert.Contains(t, rig.h.commands.ran(), "git diff --name-only origin/"+shipBase+"..."+rig.head, "the read is from the base to the head")
			assert.Empty(t, world.writes, "nothing is merged")
		})
	}
}

func TestExampleFiling_NoParkedFindingFilesNothing(t *testing.T) {
	t.Parallel()

	_, writer, outcome := parkedRig(t, "[]")

	require.NoError(t, outcome.err)
	assert.Empty(t, writer.created)
}

func TestExampleFiling_ABadFindingFilesNothingAndTheRunFailsWithTheReason(t *testing.T) {
	t.Parallel()

	good := parkedOne[1 : len(parkedOne)-1]

	for name, tc := range map[string]struct{ parked, reason string }{
		"an operator issue below P2":     {strings.Replace("["+good+"]", `"P1"`, `"P3"`, 1), "never below P2"},
		"another issue above P2":         {strings.Replace("["+good+"]", `"operator":true`, `"operator":false`, 1), "never above P2"},
		"a severity that is no P label":  {strings.Replace("["+good+"]", `"P1"`, `"P4"`, 1), "not one label of P0"},
		"two severities in one":          {strings.Replace("["+good+"]", `"P1"`, `"P1 P2"`, 1), "not one label of P0"},
		"the same id twice":              {"[" + good + "," + good + "]", "repeated"},
		"an id with capitals":            {strings.Replace("["+good+"]", "retry-loop", "Retry-Loop", 1), "lower case"},
		"no how it was found":            {strings.Replace("["+good+"]", `"found":"Run go test ./retry at commit abc: it hangs."`, `"found":" "`, 1), "empty found"},
		"a missing field":                {strings.Replace("["+good+"]", `"operator":true`, `"x":1`, 1), "has no \"operator\""},
		"text that is not a list":        {`{"id":"a"}`, "not a JSON list"},
		"a bad entry after a good entry": {"[" + good + "," + strings.NewReplacer("retry-loop", "second", `"P1"`, `"P9"`).Replace(good) + "]", "second"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, writer, outcome := parkedRig(t, tc.parked)

			require.ErrorContains(t, outcome.err, tc.reason)
			assert.Empty(t, writer.created, "every finding is checked before the first is filed")
		})
	}
}
