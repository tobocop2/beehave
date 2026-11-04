package flow

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// valueFile is a parent that spawns three children, gathers them with the given arguments and prints what each result holds in its value.
// The worker body is the text that the child runs after its command.
func valueFile(workerTail, gatherArgs string) string {
	return `def worker(item):
    command(name = "w", run = ["w"])
` + workerTail + `

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    done = gather(runs, ` + gatherArgs + `name = "fan")
    command(name = "after", run = ["after"] + [r.item + ":" + ("ok" if r.ok else "failed") + ":" + str(r.value) for r in done.results])
`
}

func TestGatherValue_AParentReadsTheDictThatTheChildReturned(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := `def worker(item):
    command(name = "w", run = ["w"])
    return {"found": ["a", 7], "ok": True, "note": None}

def main(item):
    runs = spawn(items = ["21", "22"], entry = "worker", name = "fan")
    done = gather(runs, name = "fan")
    command(name = "after", run = ["after"] + [r.item + "=" + r.value["found"][0] + "/" + str(r.value["found"][1]) for r in done.results])
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 21=a/7 22=a/7"}, h.commands.ranBy(testItem))
}

func TestGatherValue_ATupleAndAFloatAreKept(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := valueFile(`    return ("a", 2.5, 12345678901234567890)`, "")
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file))

	require.NoError(t, err)
	assert.Equal(t, []string{`after 21:ok:["a", 2.5, 12345678901234567890] 22:ok:["a", 2.5, 12345678901234567890] 23:ok:["a", 2.5, 12345678901234567890]`}, h.commands.ranBy(testItem))
}

func TestGatherValue_ARecordedValueThatIsNotJSONFailsTheGather(t *testing.T) {
	t.Parallel()

	_, err := outcomesValue([]childOutcome{{Item: "21", OK: true, Value: []byte("{")}})

	require.ErrorContains(t, err, "read the value of the run of item 21")
}

func TestGatherValue_ARunThatReturnsNothingHasNone(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, valueFile("", "")))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 21:ok:None 22:ok:None 23:ok:None"}, h.commands.ranBy(testItem))
}

func TestGatherValue_UntilAnyReturnsTheValueOfTheFirstRunThatEnded(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21", "23")

	defer close(release)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, valueFile(`    return "found"`, `until = "any", `)))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 22:ok:found"}, h.commands.ranBy(testItem))
}

func TestGatherValue_AFailedChildHasNoValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var failing atomic.Bool

	failing.Store(true)
	failItem(h, "22", &failing)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, valueFile(`    return "found"`, `fail = "return", `)))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 21:ok:found 22:failed:None 23:ok:found"}, h.commands.ranBy(testItem))
}

func TestGatherValue_AValueThatIsNotJSONSafeFailsTheChildWithAClearMessage(t *testing.T) {
	t.Parallel()

	for name, tail := range map[string]string{
		"a function":   `    return worker`,
		"a struct":     `    return struct(a = 1)`,
		"a nested one": `    return {"a": [1, {"b": worker}]}`,
		"a number key": `    return {1: "a"}`,
		"a float":      `    return 1e308 * 10`,
		"a cycle":      "    cycle = []\n    cycle.append(cycle)\n    return cycle",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			file := strings.Replace(valueFile(tail, `fail = "return", `), `str(r.value)`, `r.error`, 1)
			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file))

			require.NoError(t, err)

			after := h.commands.ranBy(testItem)
			require.Len(t, after, 1)
			assert.Contains(t, after[0], "21:failed")
			assert.Contains(t, after[0], "returned a value that is not JSON-safe", "the child says what it returned and what is allowed")
		})
	}
}

func TestGatherValue_AValueOverTheLimitFailsTheChild(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := strings.Replace(valueFile(`    return "x" * 70000`, `fail = "return", `), `str(r.value)`, `r.error`, 1)
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file))

	require.NoError(t, err)

	after := h.commands.ranBy(testItem)
	require.Len(t, after, 1)
	assert.Contains(t, after[0], "returned a value that is larger than 65536 bytes")
}
