package workflow_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

func TestCheckEventKey_AcceptsTheShapeAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, key, want string
	}{
		{"word", "progress", ""},
		{"digit first", "1st", ""},
		{"dots dashes and underscores", "a.b-c_d", ""},
		{"longest", strings.Repeat("a", 64), ""},
		{"empty", "", "is not valid"},
		{"upper case", "Bad Key", "is not valid"},
		{"leading dash", "-a", "is not valid"},
		{"too long", strings.Repeat("a", 65), "is not valid"},
		{"reserved", "beehave.keys", "is reserved"},
		{"reserved prefix alone", "beehave.", "is reserved"},
		{"no dot is no prefix", "beehave", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := workflow.CheckEventKey(tc.key)

			if tc.want == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestCheck_AKeyOfPublishAndProgressThatTheRuleRefusesIsAProblemAtTheKey(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    runs = spawn([\"1\"])\n    publish(\"Bad Key\", 1)\n    publish(\"beehave.keys\", 1)\n    progress(runs, \"Bad Key\")\n    progress(runs, \"beehave.x\")\n")

	require.Len(t, got, 4)
	assert.Contains(t, got[0], "publish: the key \"Bad Key\" is not valid")
	assert.Contains(t, got[1], "publish: the key \"beehave.keys\" is reserved")
	assert.Contains(t, got[2], "progress: the key \"Bad Key\" is not valid")
	assert.Contains(t, got[3], "progress: the key \"beehave.x\" is reserved")
}

func TestCheck_AcceptsPublishAndProgressWithAnyValueAndAComputedKey(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    runs = spawn([\"1\"])\n    k = \"a\"\n    publish(k, None)\n    publish(\"b\", {\"n\": [1]}, name = \"x\")\n    progress(runs, k)\n    progress(runs, \"b\", name = \"y\")\n")

	assert.Empty(t, got)
}

func TestCheck_PublishAndProgressNeedTheirKeywords(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    publish(\"a\")\n    progress([])\n")

	require.Len(t, got, 2)
	assert.Contains(t, got[0], "value")
	assert.Contains(t, got[1], "key")
}

func TestCheck_AKeyThatIsNotTextIsAProblem(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    publish(1, 1)\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "key must be a text")
}
