package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/workflow"
)

func TestOpenPolicy_ReadsTheWordOfAStartAndDefaultsToJoin(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		text string
		want workflow.OnOpen
		err  string
	}{
		"empty is join": {text: "", want: workflow.OnOpenJoin},
		"join":          {text: "join", want: workflow.OnOpenJoin},
		"replace":       {text: "replace", want: workflow.OnOpenReplace},
		"another word":  {text: "bogus", err: `on_open is "bogus". Use "join" or "replace"`},
		"capital":       {text: "Replace", err: `on_open is "Replace". Use "join" or "replace"`},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := workflow.OpenPolicy(c.text)
			if c.err != "" {
				require.EqualError(t, err, c.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestOnOpenModes_ListsJoinAndReplace(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"join", "replace"}, workflow.OnOpenModes())
}

func TestCheck_ASpawnTakesOnOpenFromItsSetAndRefusesAnotherWord(t *testing.T) {
	t.Parallel()

	assert.Empty(t, checkText(t, "def main(item):\n    spawn([\"1\"], on_open = \"replace\", name = \"a\")\n    spawn([\"1\"], on_open = \"join\", name = \"b\")\n"))

	got := checkText(t, "def main(item):\n    spawn([\"1\"], on_open = \"swap\")\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "on_open is \"swap\". Use one of: join, replace.")
}
