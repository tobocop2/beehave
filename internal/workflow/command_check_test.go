package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commandWith is a file with one command step that holds the given lines after `run`. The first line is line 4.
func commandWith(lines string) string {
	return "workflow \"w\" {}\ncommand \"r\" {\n  run = [\"x\"]\n" + lines + "}\n"
}

func TestCheckRefusesAStopOnRCThatIsNotAListOfCodesFromOneTo255(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		setting string
		col     int
	}{
		"not a list":     {`  stop_on_rc = 2`, 16},
		"a string":       {`  stop_on_rc = "2"`, 16},
		"a text item":    {`  stop_on_rc = [2, "x"]`, 20},
		"zero":           {`  stop_on_rc = [2, 0]`, 20},
		"above 255":      {`  stop_on_rc = [256]`, 17},
		"a fraction":     {`  stop_on_rc = [1.5]`, 17},
		"a negative":     {`  stop_on_rc = [-1]`, 17},
		"a list in list": {`  stop_on_rc = [[2]]`, 17},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := oneProblem(t, commandWith(test.setting+"\n"))

			assert.Contains(t, d.Text, "stop_on_rc")
			assert.Equal(t, 4, d.Line)
			assert.Equal(t, test.col, d.Col, "the problem sits on the value")
		})
	}
}

func TestCheckRefusesAStdinThatIsNotAString(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]string{"a number": "7", "a list": `["a"]`, "a bool": "true"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := oneProblem(t, commandWith("  stdin = "+value+"\n"))

			assert.Contains(t, d.Text, "stdin")
			assert.Contains(t, d.Text, "string")
			assert.Equal(t, 4, d.Line)
			assert.Equal(t, 11, d.Col)
		})
	}
}

func TestCheckRefusesAnEnvThatIsNotAMapOfStrings(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, commandWith("  env = \"A=1\"\n"))
	assert.Contains(t, d.Text, "env")
	assert.Contains(t, d.Text, "map")
	assert.Equal(t, 4, d.Line)
	assert.Equal(t, 9, d.Col)

	d = oneProblem(t, commandWith("  env = { A = 7 }\n"))
	assert.Contains(t, d.Text, "env")
	assert.Contains(t, d.Text, "string")
	assert.Equal(t, 15, d.Col, "the problem sits on the value")
}

func TestCheckRefusesAnEnvNameThatBeginsWithDBOSOrOBEE(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"DBOS__APPLICATION_VERSION", "OBEE_STATE_DIR"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := oneProblem(t, commandWith("  env = { "+name+" = \"1\" }\n"))

			assert.Contains(t, d.Text, name)
			assert.Equal(t, 4, d.Line)
			assert.Equal(t, 11, d.Col, "the problem sits on the name")
		})
	}
}

func TestCheckRefusesStdinAndEnvOnACommandThatRunsOnAHost(t *testing.T) {
	t.Parallel()

	for _, setting := range []string{"stdin = \"x\"", "env = { A = \"1\" }"} {
		d := oneProblem(t, commandWith("  host = \"box\"\n  "+setting+"\n"))

		assert.Contains(t, d.Text, "host")
		assert.Equal(t, 5, d.Line, "the problem sits on the setting")
	}
}

func TestCheckPassesTheSettingsOfACommandAndReadsWhatAnExpressionComputes(t *testing.T) {
	t.Parallel()

	found, unparsed := check(t, twoSteps(commandWith(`  stdin      = agent.implement.text
  env        = { MODE = "strict", (env.KEY) = "1" }
  stop_on_rc = [2, 255]
`)[len("workflow \"w\" {}\n"):]))

	require.False(t, unparsed)
	assert.Empty(t, found)

	found, _ = check(t, commandWith("  stop_on_rc = env.CODES\n  env = env.PAIRS\n  stdin = env.TEXT\n"))
	assert.Empty(t, found)

	found, _ = check(t, commandWith("  stop_on_rc = [env.CODE]\n"))
	assert.Empty(t, found)
}

func TestCheckRefusesAStdinThatNamesAStepThatComesLater(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, "workflow \"w\" {}\ncommand \"r\" {\n  run   = [\"x\"]\n  stdin = agent.implement.text\n}\nagent \"implement\" {\n  prompt = \"prompts/fix.md\"\n}\n")

	assert.Contains(t, d.Text, "implement")
	assert.Equal(t, 4, d.Line)
}
