package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

// These tests run the settings function of the example file: it reads the round settings from facts().vars and refuses a missing or wrong one.

// roundSettings is the smallest valid round: the four required keys and a label.
func roundSettings() map[string]any {
	return map[string]any{"base": "integration/s1", "owner": "tobocop2", "lane_cap": json.Number("4"), "fix_rounds": json.Number("5"), "label": "beehest-round"}
}

// with returns the settings with the keys of change set. A nil value removes the key.
func with(settings map[string]any, change map[string]any) map[string]any {
	for key, value := range change {
		if value == nil {
			delete(settings, key)
		} else {
			settings[key] = value
		}
	}

	return settings
}

func roundVars(settings map[string]any) map[string]any {
	return map[string]any{"round": settings}
}

// settingsOf runs settings(facts()) of the example file over the vars and returns the text of the settings, or the error of the run.
// The test replaces main of its copy of the file with a main that prints the settings, so it reaches the function with no round entry.
func settingsOf(t *testing.T, vars map[string]any) (string, error) {
	t.Helper()

	h := newHarness(t)
	h.facts = func(project string) model.Project { return model.Project{Name: project, Vars: vars} }

	text, err := os.ReadFile(filepath.Join("..", "..", "examples", "remediation", "workflow.star"))
	require.NoError(t, err)

	renamed := strings.Replace(string(text), "def main(item):", "def _unused_main(item):", 1)
	require.NotEqual(t, string(text), renamed)

	file := filepath.Join(t.TempDir(), "workflow.star")
	require.NoError(t, os.WriteFile(file, []byte(renamed+"\ndef main(item):\n    fail(\"S=%r\" % settings(facts()))\n"), 0o600)) //nolint:gosec // the copy of the example file goes to a temporary folder of the test

	_, err = h.engine(t).RunFile(testItem, testProject, file, modeOff())
	require.Error(t, err)

	_, shown, found := strings.Cut(err.Error(), "S=")
	if !found {
		return "", err
	}

	return shown, nil
}

// refusalOf returns the refusal that the settings function gives, and fails the test when it gives none.
func refusalOf(t *testing.T, vars map[string]any) string {
	t.Helper()

	shown, err := settingsOf(t, vars)
	require.Error(t, err, "settings returned %s", shown)

	return err.Error()
}

func TestRound_AMissingBaseIsRefusedByTheWorkflow(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "21")
	rig.setRound("base", nil)

	_, err := rig.run(t)

	require.ErrorContains(t, err, "vars.round.base is missing")
	assert.Empty(t, rig.kids.started(), "the round starts no child")
	assert.Empty(t, rig.h.agents.seen(), "the round runs no agent")
}

func TestRound_ASettingThatTheRoundNeedsIsRefusedWhenItIsMissing(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"base", "owner", "lane_cap", "fix_rounds"} {
		got := refusalOf(t, roundVars(with(roundSettings(), map[string]any{key: nil})))

		assert.Contains(t, got, "vars.round."+key+" is missing", key)
	}
}

func TestRound_AProjectWithNoRoundSettingsIsRefused(t *testing.T) {
	t.Parallel()

	assert.Contains(t, refusalOf(t, map[string]any{"mutation": json.Number("1")}), "REFUSE: the project vars hold no round.")
}

func TestRound_TheSettingsGiveEveryKeyAndTheDefaultsOfTheKeysTheFileLeavesOut(t *testing.T) {
	t.Parallel()

	shown, err := settingsOf(t, roundVars(roundSettings()))

	require.NoError(t, err)
	for _, want := range []string{
		`"base": "integration/s1"`, `"owner": "tobocop2"`, `"lane_cap": 4`, `"fix_rounds": 5`, `"label": "beehest-round"`,
		`"lane_kind": "impl"`, `"base_reds_file": "../base-reds-integration_2Fs1.md"`, `"wait_hours": 24`, `"integration_pr": 0`, `"merge": False`, `"hand_off": False`, `"items": []`,
	} {
		assert.Contains(t, shown, want)
	}

	all := with(roundSettings(), map[string]any{
		"next_base": "integration/s2", "owner_email": "o@example.com", "source_checkout": "/src", "integration_pr": json.Number("7"), "merge": true, "acceptance": "a.sh",
		"base_result": "b.md", "base_reds": []any{"TestX"}, "base_reds_file": "reds.md", "lane_kind": "papers", "allow_paths": []any{"docs"}, "wait_hours": json.Number("12"),
	})

	shown, err = settingsOf(t, roundVars(all))

	require.NoError(t, err)
	for _, want := range []string{
		`"next_base": "integration/s2"`, `"owner_email": "o@example.com"`, `"source_checkout": "/src"`, `"integration_pr": 7`, `"merge": True`, `"acceptance": "a.sh"`,
		`"base_result": "b.md"`, `"base_reds": ["TestX"]`, `"base_reds_file": "reds.md"`, `"lane_kind": "papers"`, `"allow_paths": ["docs"]`, `"wait_hours": 12`,
	} {
		assert.Contains(t, shown, want)
	}
}

func TestRound_AnEmptyNextBaseAndAnEmptyAcceptanceAreValuesAndLeaveTheRoundWithoutACutOrAnAcceptance(t *testing.T) {
	t.Parallel()

	shown, err := settingsOf(t, roundVars(with(roundSettings(), map[string]any{"next_base": "", "acceptance": ""})))

	require.NoError(t, err)
	assert.Contains(t, shown, `"next_base": ""`)
	assert.Contains(t, shown, `"acceptance": ""`)
}

func TestRound_AnItemListSelectsTheListedItems(t *testing.T) {
	t.Parallel()

	shown, err := settingsOf(t, roundVars(with(roundSettings(), map[string]any{"label": nil, "items": []any{"12", "bd-3"}})))

	require.NoError(t, err)
	assert.Contains(t, shown, `"items": ["12", "bd-3"]`)
	assert.Contains(t, shown, `"label": ""`)
}

func TestRound_AnExplicitImplLaneKindNeedsNoAllowedPaths(t *testing.T) {
	t.Parallel()

	shown, err := settingsOf(t, roundVars(with(roundSettings(), map[string]any{"lane_kind": "impl"})))

	require.NoError(t, err)
	assert.Contains(t, shown, `"lane_kind": "impl"`)
}

func TestRound_BadValuesAreRefusedWithTheKeyAndTheFix(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		change map[string]any
		want   string
	}{
		"no selector":             {map[string]any{"label": nil}, "a round needs an item selector: set vars.round.label or vars.round.items"},
		"both selectors":          {map[string]any{"items": []any{"1"}}, "a round takes one item selector: set vars.round.label or vars.round.items, not both"},
		"an empty items list":     {map[string]any{"label": nil, "items": []any{}}, "a round needs an item selector"},
		"a blank label":           {map[string]any{"label": " "}, "vars.round.label is blank"},
		"an item that is no id":   {map[string]any{"label": nil, "items": []any{"not an id"}}, `vars.round.items lists "not an id"`},
		"a round id as an item":   {map[string]any{"label": nil, "items": []any{"round/x"}}, `vars.round.items lists "round/x"`},
		"wait_hours zero":         {map[string]any{"wait_hours": json.Number("0")}, "vars.round.wait_hours is 0"},
		"integration_pr negative": {map[string]any{"integration_pr": json.Number("-1")}, "vars.round.integration_pr is -1"},
		"an unknown lane kind":    {map[string]any{"lane_kind": "code"}, `vars.round.lane_kind is "code". Write impl, papers or design`},
		"papers with no paths":    {map[string]any{"lane_kind": "papers"}, "vars.round.allow_paths is empty"},
		"design with no paths":    {map[string]any{"lane_kind": "design"}, "vars.round.allow_paths is empty"},
		"a blank base_reds_file":  {map[string]any{"base_reds_file": " "}, "vars.round.base_reds_file is blank"},
		"a blank base reds entry": {map[string]any{"base_reds": []any{" "}}, "vars.round.base_reds lists a blank string"},
		"a blank allow_paths":     {map[string]any{"lane_kind": "papers", "allow_paths": []any{""}}, "vars.round.allow_paths lists a blank string"},
		"a text merge":            {map[string]any{"merge": "yes"}, "vars.round.merge is"},
		"a number base":           {map[string]any{"base": json.Number("3")}, "vars.round.base is"},
		"an unknown key":          {map[string]any{"color": "red"}, "vars.round.color is not a round setting"},
		"a blank base":            {map[string]any{"base": " "}, "vars.round.base is blank"},
		"a blank owner":           {map[string]any{"owner": ""}, "vars.round.owner is blank"},
	} {
		assert.Contains(t, refusalOf(t, roundVars(with(roundSettings(), test.change))), test.want, name)
	}
}

func TestRound_TheDefaultRecordOfTheBaseRedsIsNamedByTheBaseOfTheRound(t *testing.T) {
	t.Parallel()

	one, err := settingsOf(t, roundVars(with(roundSettings(), map[string]any{"base": "integration/r1"})))
	require.NoError(t, err)

	two, err := settingsOf(t, roundVars(with(roundSettings(), map[string]any{"base": "integration/r2"})))
	require.NoError(t, err)

	assert.Contains(t, one, `"base_reds_file": "../base-reds-integration_2Fr1.md"`)
	assert.Contains(t, two, `"base_reds_file": "../base-reds-integration_2Fr2.md"`)
}

// The file is a path from the folder of the round, and the project folder is its parent: the clean form of an allowed path has at most one leading "..".
func TestRound_ABaseRedsFileThatLeavesTheProjectFolderIsRefused(t *testing.T) {
	t.Parallel()

	for name, file := range map[string]string{
		"two levels up":                     "../../reds.md",
		"down, then up past the project":    "a/../../../reds.md",
		"up past the project and back down": "../../project/reds.md",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			refusal := refusalOf(t, roundVars(with(roundSettings(), map[string]any{"base_reds_file": file})))
			assert.Contains(t, refusal, "vars.round.base_reds_file is "+strconv.Quote(file)+", which leaves the folder of the project")
		})
	}

	for name, file := range map[string]string{
		"the project folder":             "../reds.md",
		"the round folder":               "reds.md",
		"up and down inside the project": "../a/../reds.md",
		"a dot part":                     "./x/../../reds.md",
		"an absolute path":               "/srv/reds.md",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			shown, err := settingsOf(t, roundVars(with(roundSettings(), map[string]any{"base_reds_file": file})))
			require.NoError(t, err)
			assert.Contains(t, shown, `"base_reds_file": `+strconv.Quote(file))
		})
	}
}

func TestRound_ARoundThatIsNotADictIsRefused(t *testing.T) {
	t.Parallel()

	assert.Contains(t, refusalOf(t, map[string]any{"round": "integration/s1"}), "vars.round is")
}

func TestRound_ACountBelowItsLimitIsRefusedWithTheKey(t *testing.T) {
	t.Parallel()

	for key, limit := range map[string]int{"lane_cap": 1, "fix_rounds": 1, "wait_hours": 1, "integration_pr": 0} {
		atLimit, err := settingsOf(t, roundVars(with(roundSettings(), map[string]any{key: json.Number(strconv.Itoa(limit))})))

		require.NoError(t, err, key)
		assert.Contains(t, atLimit, `"`+key+`": `+strconv.Itoa(limit), key)

		below := refusalOf(t, roundVars(with(roundSettings(), map[string]any{key: json.Number(strconv.Itoa(limit - 1))})))

		assert.Contains(t, below, "vars.round."+key+" is "+strconv.Itoa(limit-1), key)
	}
}

func TestRound_ABaseThatCannotMakeARoundIdIsRefused(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"a b", "/x", "x/", "a//b", "..", "a/../b"} {
		assert.Contains(t, refusalOf(t, roundVars(with(roundSettings(), map[string]any{"base": base}))), "vars.round.base is", base)
	}

	for _, base := range []string{"b", "integration/r2", "a.b_c-d/e1"} {
		_, err := settingsOf(t, roundVars(with(roundSettings(), map[string]any{"base": base})))

		require.NoError(t, err, base)
	}
}
