package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
)

func checkText(t *testing.T, text string) []string {
	t.Helper()

	found, _ := workflow.Check(folder(t, map[string]string{"workflow.star": text}, "workflow.star"))

	return problems(found)
}

func TestCheck_AcceptsASpawnAndAGatherThatNameAFunctionOfTheFile(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def worker(item):\n    pass\n\ndef main(item):\n    runs = spawn(items = [\"1\"], entry = \"worker\", name = \"fan\")\n    gather(runs, fail = \"raise\", name = \"fan\")\n    spawn([\"2\"], \"worker\")\n    gather(runs, \"return\")\n    spawn([\"3\"])\n    spawn([\"3\"], entry = \"main\", name = \"x\")\n")

	assert.Empty(t, got)
}

func TestCheck_ASpawnEntryThatTheFileLoadsCounts(t *testing.T) {
	t.Parallel()

	found, _ := workflow.Check(folder(t, map[string]string{
		"workflow.star": "load(\"//lib.star\", \"worker\")\n\ndef main(item):\n    spawn([\"1\"], entry = \"worker\")\n",
		"lib.star":      "def worker(item):\n    pass\n",
	}, "workflow.star"))

	assert.Empty(t, problems(found))
}

func TestCheck_ASpawnEntryThatNamesNoFunctionOfTheFileIsAProblemAtTheValue(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    spawn([\"1\"], entry = \"worker\")\n    spawn([\"1\"], \"other\")\n")

	require.Len(t, got, 2)
	assert.Contains(t, got[0], "2:26 entry is \"worker\", and workflow.star defines no function of that name. Add def worker(item):")
	assert.Contains(t, got[1], "entry is \"other\"")
}

func TestCheck_AFunctionOfALoadedFileThatTheMainFileDoesNotLoadIsNotAnEntry(t *testing.T) {
	t.Parallel()

	found, _ := workflow.Check(folder(t, map[string]string{
		"workflow.star": "load(\"//lib.star\", \"other\")\n\ndef main(item):\n    spawn([\"1\"], entry = \"worker\")\n",
		"lib.star":      "def worker(item):\n    pass\n\ndef other():\n    pass\n",
	}, "workflow.star"))

	require.Len(t, problems(found), 1)
	assert.Contains(t, problems(found)[0], "entry is \"worker\"")
}

func TestCheck_AnEntryThatIsNotALiteralIsLeftToTheRun(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    name = \"x\"\n    spawn([\"1\"], entry = name)\n    spawn([\"1\"], entry = name + \"x\")\n")

	assert.Empty(t, got)
}

func TestCheck_AnUnknownKeywordOfSpawnOrGatherIsAProblemAtTheKeyword(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    runs = spawn([\"1\"], limit = 2)\n    gather(runs, wait = True)\n")

	require.Len(t, got, 2)
	assert.Contains(t, got[0], "2:25 spawn takes no argument limit. Use one of: items, entry, name.")
	assert.Contains(t, got[1], "3:18 gather takes no argument wait. Use one of: runs, fail, until, name.")
}

func TestCheck_ABatchMergeCallWithNoNameIsAProblemAtTheCall(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    batch_merge(prs = [], base = \"main\", repo = \"o/r\")\n    batch_merge([], \"main\", \"o/r\", limit = 2, name = \"b\")\n    batch_merge([], \"main\", \"o/r\", name = \"c\")\n")

	require.Len(t, got, 2)
	assert.Contains(t, got[0], "2:16 batch_merge needs the `name` keyword. Add name = ... to the call. The name makes the step names of the call unique, so use one for each call of the run.")
	assert.Contains(t, got[1], "3:36 batch_merge takes no argument limit. Use one of: prs, base, repo, hold, own, forbid_text, gate_run, name.")
}

func TestCheck_AFailValueThatIsNotListedIsAProblemAtTheValue(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    runs = spawn([\"1\"])\n    gather(runs, fail = \"ignore\")\n    gather(runs, \"stop\")\n    gather(runs, fail = runs)\n    gather(runs)\n")

	require.Len(t, got, 2)
	assert.Contains(t, got[0], "3:25 fail is \"ignore\". Use one of: return, raise.")
	assert.Contains(t, got[1], "fail is \"stop\"")
}

func TestCheck_ACallOfAnotherNameOrAMethodIsNotChecked(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    x = struct(entry = 1)\n    [].append(spawn)\n    command(run = [\"a\"], name = \"n\")\n")

	assert.Empty(t, got)
}
