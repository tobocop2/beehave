package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
)

const briefRules = "# Rules\n\n## Maintainer preflight\n\nnone measured yet\n\n## CI checks and their base\n\n- Run: make check\n- Base: origin/main\n\n## Standing rules\n\n- Take: none\n"

// briefState returns a state folder that holds the rules file of the project p.
func briefState(t *testing.T) map[string]string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "rules"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rules", "p.md"), []byte(briefRules), 0o600))

	return map[string]string{"OBEE_STATE_DIR": dir}
}

func TestBriefs_WriteReturnsThePathOfABriefThatNamesTheItem(t *testing.T) {
	t.Parallel()

	env := briefState(t)

	path, err := newBriefs(env).Write(t.Context(), flow.BriefRequest{Run: "r1", Item: "bb-1", Project: "p", Step: model.StepImplement, Findings: "a finding", Answer: "an answer"})

	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(path))
	assert.Equal(t, env["OBEE_STATE_DIR"], filepath.Dir(filepath.Dir(filepath.Dir(path))))

	text, err := os.ReadFile(path) //nolint:gosec // the test reads the path the code returned
	require.NoError(t, err)
	assert.Contains(t, string(text), "# Lane `bb-1-implement-0`")
	assert.Contains(t, string(text), "## Open findings\n\na finding")
	assert.Contains(t, string(text), "## Last answer of the owner\n\nan answer")
}

func TestBriefs_EveryAgentStepHasAKind(t *testing.T) {
	t.Parallel()

	env := briefState(t)

	for _, step := range []model.Step{model.StepPlan, model.StepImplement, model.StepReview} {
		_, err := newBriefs(env).Write(t.Context(), flow.BriefRequest{Run: "r1", Item: "bb-2", Project: "p", Step: step})
		require.NoError(t, err, step)
	}
}

func TestBriefs_ARoundAfterTheFirstIsAReworkOfTheEarlierBrief(t *testing.T) {
	t.Parallel()

	env := briefState(t)
	briefs := newBriefs(env)

	_, err := briefs.Write(t.Context(), flow.BriefRequest{Run: "r1", Item: "bb-3", Project: "p", Step: model.StepImplement})
	require.NoError(t, err)

	path, err := briefs.Write(t.Context(), flow.BriefRequest{Run: "r1", Item: "bb-3", Project: "p", Step: model.StepImplement, Round: 1, Findings: "fix this"})

	require.NoError(t, err)
	assert.FileExists(t, path)
}

func TestBriefs_RefusesAStepThatRunsNoAgent(t *testing.T) {
	t.Parallel()

	_, err := newBriefs(briefState(t)).Write(t.Context(), flow.BriefRequest{Run: "r1", Item: "bb-4", Project: "p", Step: model.StepGate})

	require.ErrorContains(t, err, "gate")
}

func TestBriefs_RefusesAStepItDoesNotKnow(t *testing.T) {
	t.Parallel()

	_, err := newBriefs(briefState(t)).Write(t.Context(), flow.BriefRequest{Run: "r1", Item: "bb-4", Project: "p", Step: model.Step("bogus")})

	require.ErrorContains(t, err, "unknown")
}

func TestBriefs_RefusesAProjectWithNoRulesFile(t *testing.T) {
	t.Parallel()

	_, err := newBriefs(briefState(t)).Write(t.Context(), flow.BriefRequest{Run: "r1", Item: "bb-5", Project: "other", Step: model.StepImplement})

	require.Error(t, err)
}

func TestSlug_KeepsAPathSeparatorOutOfAName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "---a-b--", slug("../a/b.."))
}

func TestBriefs_RefusesARelativeStateFolder(t *testing.T) {
	t.Parallel()

	_, err := newBriefs(map[string]string{"OBEE_STATE_DIR": "rel"}).Write(t.Context(), flow.BriefRequest{Run: "r1", Item: "bb-6", Project: "p", Step: model.StepImplement})

	require.Error(t, err)
}

func TestBriefs_AFolderThatCannotBeMadeOrWrittenIsAnError(t *testing.T) {
	t.Parallel()

	request := flow.BriefRequest{Run: "r1", Item: "bb-4", Project: "p", Step: model.StepImplement}

	t.Run("run folder is a file", func(t *testing.T) {
		t.Parallel()

		env := briefState(t)
		require.NoError(t, os.MkdirAll(filepath.Join(env["OBEE_STATE_DIR"], "briefs"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(env["OBEE_STATE_DIR"], "briefs", "r1"), nil, 0o600))

		_, err := newBriefs(env).Write(t.Context(), request)

		require.ErrorContains(t, err, "make")
	})

	t.Run("slots file is a folder", func(t *testing.T) {
		t.Parallel()

		env := briefState(t)
		require.NoError(t, os.MkdirAll(filepath.Join(env["OBEE_STATE_DIR"], "briefs", "r1", "bb-4-implement-0.slots"), 0o700))

		_, err := newBriefs(env).Write(t.Context(), request)

		require.ErrorContains(t, err, "write")
	})

	t.Run("brief file is a folder", func(t *testing.T) {
		t.Parallel()

		env := briefState(t)
		require.NoError(t, os.MkdirAll(filepath.Join(env["OBEE_STATE_DIR"], "briefs", "r1", "bb-4-implement-0.md"), 0o700))

		_, err := newBriefs(env).Write(t.Context(), request)

		require.ErrorContains(t, err, "write")
	})
}
