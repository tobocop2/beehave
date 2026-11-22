package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// chunkEntry replaces the main function of the backfill with one that files the item of one chunk of two days.
// The plan, the budget and the child run need the warehouse scripts, and the filing of the item reads none of them.
const chunkEntry = `
def main(item):
    request = struct(first = "2026-01-01", last = "2026-01-02", budget = 500)
    chunk_item(item, request, ["2026-01-01", "2026-01-02"], "r0-0")
`

// pipelineFlow is the text of examples/ticket-pipeline/backfill.star with the main function of chunkEntry in place of its own.
// The tests write it in the folder of the example copy, because the file loads its siblings by path.
func pipelineFlow(t *testing.T) string {
	t.Helper()

	folder := t.TempDir()
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", "ticket-pipeline"))))

	path := filepath.Join(folder, "backfill.star")
	text, err := os.ReadFile(path) //nolint:gosec // the path is the example copy that this test made in its own folder
	require.NoError(t, err)

	renamed := strings.Replace(string(text), "def main(item):", "def full_main(item):", 1)
	require.NotEqual(t, string(text), renamed, "the example has a main function")
	require.NoError(t, os.WriteFile(path, []byte(renamed+chunkEntry), 0o600)) //nolint:gosec // the example copy of this test

	return path
}

// pipelineHarness is a harness whose project has the vars, and whose tracker records what the file creates.
func pipelineHarness(t *testing.T, vars map[string]any) (*harness, *fakeWriter) {
	t.Helper()

	h := newHarness(t)
	h.opts.Env = map[string]string{"HOME": "/home/me", "PIPE_DIR": "/pipe", "PIPE_HOME": "/pipe-home"}
	writer := newFakeWriter()
	h.writer = writer
	h.facts = func(string) model.Project { return model.Project{Name: testProject, Vars: vars} }

	return h, writer
}

func TestExamplePipeline_AProjectWithAChunkAssigneeFilesEachChunkWithThatPerson(t *testing.T) {
	t.Parallel()

	h, writer := pipelineHarness(t, map[string]any{"chunk_assignee": "dana"})

	_, err := h.engine(t).RunFile(testItem, testProject, pipelineFlow(t), modeOff())

	require.NoError(t, err)
	require.Len(t, writer.assignees, 1)
	assert.Contains(t, writer.assignees[0], ":chunk-2026-01-01-2026-01-02-v")
	assert.True(t, strings.HasSuffix(writer.assignees[0], "-r0-0|dana"))
}

func TestExamplePipeline_AProjectWithNoChunkAssigneeFilesTheChunkWithNoAssignee(t *testing.T) {
	t.Parallel()

	h, writer := pipelineHarness(t, map[string]any{})

	_, err := h.engine(t).RunFile(testItem, testProject, pipelineFlow(t), modeOff())

	require.NoError(t, err)
	require.Len(t, writer.assignees, 1)
	assert.True(t, strings.HasSuffix(writer.assignees[0], "-r0-0|"))
}
