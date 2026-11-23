package cli_test

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
)

// agentsLine is the line of the status text that names the agents limit. It is empty when the text has none.
func agentsLine(stdout string) string {
	for line := range strings.SplitSeq(stdout, "\n") {
		if strings.HasPrefix(line, "agents: ") {
			return line
		}
	}

	return ""
}

// TestAdaptiveStatus_Trace prints one STATUS line for each scenario of the file in ADAPTIVE_STATUS (id|mode|current|ceiling).
// The acceptance script of the change compares the lines with its own expectation. The test skips when the variable is empty.
func TestAdaptiveStatus_Trace(t *testing.T) { //nolint:paralleltest // reads a variable of the process
	path := os.Getenv("ADAPTIVE_STATUS") //nolint:forbidigo // the acceptance script names the scenario file in this variable
	if path == "" {
		t.Skip("ADAPTIVE_STATUS names no scenario file")
	}

	file, err := os.Open(path) //nolint:gosec // the acceptance script names the file
	require.NoError(t, err)

	defer func() { require.NoError(t, file.Close()) }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if scanner.Text() == "" {
			continue
		}

		fields := strings.Split(scanner.Text(), "|")
		require.Len(t, fields, 4, scanner.Text())

		current, err := strconv.Atoi(fields[2])
		require.NoError(t, err)

		ceiling, err := strconv.Atoi(fields[3])
		require.NoError(t, err)

		rig := newRig(t)
		mode := config.Concurrency(fields[1])
		rig.steering.agentsCap = flow.AgentsCap{Mode: mode, Current: current, Ceiling: ceiling}
		rig.steering.agentsCapOn = mode == config.ConcurrencyAdaptive

		code, stdout, stderr := rig.execute(t.Context(), "status")
		require.Equal(t, 0, code, stderr)

		_, _ = fmt.Fprintf(os.Stdout, "STATUS %s|%s\n", fields[0], agentsLine(stdout))
	}

	require.NoError(t, scanner.Err())
}

func TestStatus_AnAdaptiveConfigShowsTheLimitInTextAndJSONAndAStaticOneShowsNone(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.agentsCap = flow.AgentsCap{Mode: config.ConcurrencyAdaptive, Current: 3, Ceiling: 6}
	rig.steering.agentsCapOn = true

	code, stdout, stderr := rig.execute(t.Context(), "status")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "agents: 3 of 6 at once (adaptive)", agentsLine(stdout))

	code, stdout, stderr = rig.execute(t.Context(), "status", "--json")
	require.Equal(t, 0, code, stderr)
	assert.JSONEq(t, `{"runs":[],"agents_concurrency":{"mode":"adaptive","current":3,"ceiling":6}}`, stdout)

	rig = newRig(t)
	rig.steering.agentsCap = flow.AgentsCap{Mode: config.ConcurrencyStatic, Current: 6, Ceiling: 6}

	code, stdout, stderr = rig.execute(t.Context(), "status")
	require.Equal(t, 0, code, stderr)
	assert.Empty(t, agentsLine(stdout))

	code, stdout, stderr = rig.execute(t.Context(), "status", "--json")
	require.Equal(t, 0, code, stderr)
	assert.JSONEq(t, `{"runs":[]}`, stdout)
}

func TestStatus_AnAgentsLimitThatCannotBeReadFailsTheList(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.agentsCapErr = errFake

	code, _, stderr := rig.execute(t.Context(), "status")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")
}
