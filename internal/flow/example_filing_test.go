package flow

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
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
	rig.setRound("fix_rounds", oneFixRound)
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

	done := pipelineInBackground(rig.h.engine(t), file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))

	return rig, writer, receive(t, done)
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

func TestExampleFiling_AnItemWhoseIDIsAGitHubNumberAddsNoPatternToTheForbidTextOfTheRound(t *testing.T) {
	t.Parallel()

	// The pattern match itself is the merge package's. The round passes the attribution patterns for every item, and adds a pattern only for an id that is not a GitHub number.
	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	require.NotEmpty(t, rig.merger.given)

	for _, given := range rig.merger.given {
		assert.Len(t, given.ForbidText, 4, "an id of only digits is a GitHub number, so it adds no pattern to the four attribution patterns")
	}
}

func TestExampleChangelog_TheMergeRefusesAChangeThatLeavesTheDeclaredChangelogAlone(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		changelog, changed string
		landed             []string
	}{
		"no changelog declared":                  {"", "a.go\n", []string{"1"}},
		"the declared changelog is in the diff":  {"CHANGELOG.md", "a.go\nCHANGELOG.md\n", []string{"1"}},
		"the declared changelog is not in diff":  {"CHANGELOG.md", "a.go\ndocs/CHANGELOG.md\n", nil},
		"an empty diff leaves the changelog out": {"CHANGELOG.md", "", nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t, "1")
			if tc.changelog != "" {
				rig.project.Vars["changelog"] = tc.changelog
			}

			previous := rig.h.commands.reply
			rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
				if request.Run[0] == "git" && (request.Run[1] == "fetch" || request.Run[1] == "diff") {
					return CommandOutput{Stdout: tc.changed}, nil
				}

				return previous(request)
			}

			_, err := rig.run(t)

			require.NoError(t, err)
			assert.Equal(t, tc.landed, rig.merger.landed())

			if tc.landed == nil {
				// The child of the item reads the changed paths at the end of its run and fails with the refusal, so the item never reaches the land phase and gets no note.
				assert.Contains(t, gitLines(rig), "git diff --name-only origin/main..."+rig.heads["1"])
				assert.Empty(t, notesWithKey(rig.writer, "rejected"))
			}
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

func TestExampleFiling_ParkedTextThatIsNotJSONIsRefusedWithTheSettleOutputAndTheItem(t *testing.T) {
	t.Parallel()

	_, writer, outcome := parkedRig(t, "not json at all")

	require.ErrorContains(t, outcome.err, `REFUSE: the output parked of the settle step of item `+testItem+` is not valid JSON: "not json at all".`)
	assert.NotContains(t, outcome.err.Error(), "json.decode", "the raw decoder error is not what the item ends with")
	assert.Empty(t, writer.created)
}

func TestExampleFiling_ParkedTextThatIsNotJSONIsQuotedUpTo200CharactersAndTheCutIsMarked(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ parked, quoted string }{
		"an empty text":                  {"", `""`},
		"200 characters, not cut":        {strings.Repeat("x", 200), `"` + strings.Repeat("x", 200) + `"`},
		"201 characters, cut and marked": {strings.Repeat("x", 201), `"` + strings.Repeat("x", 200) + `..."`},
		"300 multibyte characters":       {strings.Repeat("€", 300), `"` + strings.Repeat("€", 200) + `..."`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, _, outcome := parkedRig(t, tc.parked)

			require.Error(t, outcome.err)
			assert.Contains(t, outcome.err.Error(), "is not valid JSON: "+tc.quoted+". Write [] when there is nothing to report")
		})
	}
}
