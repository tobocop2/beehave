package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheck_ReportsAnUnknownFieldOfTheItemParameter(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, text string }{
		{"main", "def main(item):\n    command(run = [item.parent])\n"},
		{"main with another name", "def main(work):\n    command(run = [work.parent])\n"},
		{"finish", "def main(item):\n    pass\n\ndef finish(item):\n    command(run = [item.parent])\n"},
		{"spawn entry", "def worker(unit):\n    command(run = [unit.parent])\n\ndef main(item):\n    spawn([\"1\"], \"worker\")\n"},
		{"spawn entry keyword", "def worker(unit):\n    command(run = [unit.parent])\n\ndef main(item):\n    spawn(items = [\"1\"], entry = \"worker\")\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, tc.text)

			require.Len(t, got, 1)
			assert.Contains(t, got[0], "parent is not a field of the item. It has: id, title, body.")
		})
	}
}

func TestCheck_AcceptsTheFieldsOfTheItemAndLeavesOtherNamesToTheRun(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def helper(item):\n    return item.parent\n\n"+
		"def reassigned(item):\n    item = struct(parent = 1)\n    return item.parent\n\n"+
		"def second(item, other):\n    return other.parent\n\n"+
		"def main(item):\n    command(run = [item.id, item.title, item.body])\n    spawn([\"1\"], \"second\")\n    return [helper(item), reassigned(item)]\n")

	assert.Empty(t, got)
}

func TestCheck_LeavesAnItemParameterThatTheFunctionBindsAgainToTheRun(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    for item in [struct(parent = 1)]:\n        command(run = [item.parent])\n")

	assert.Empty(t, got)
}
