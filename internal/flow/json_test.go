package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSON_DecodesTextAndReadsAField(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(r CommandRequest) (CommandOutput, error) {
		if r.Run[0] == "emit" {
			return CommandOutput{Stdout: `{"cmd": "lint", "args": ["-x"]}`}, nil
		}

		return CommandOutput{}, nil
	}

	_, err := walkBody(t, h, "plan = json.decode(command(run = [\"emit\"]).stdout)\ncommand(run = [plan[\"cmd\"], plan[\"args\"][0]])\ncommand(run = [json.encode([1])])\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"emit", "lint -x", "[1]"}, h.commands.ran())
}

func TestJSON_MalformedTextFailsWithThePositionOfTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "def main(item):\n    json.decode(\"{\\\"a\\\": }\")\n")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow.star:2:16")
	assert.Contains(t, err.Error(), "at offset")
}
