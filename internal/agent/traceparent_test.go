package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	stepTrace    = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	ambientTrace = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	lineLogin    = "LINE_CONFIG_DIR"
)

// lineAdapter is an adapter for a CLI named line, with a sandbox. Its launch sets the variables in launchEnv.
type lineAdapter struct {
	agent.Adapter

	launchEnv map[string]string
}

func (lineAdapter) Profile() agent.Profile {
	return agent.Profile{Binary: "line", ConfigVars: []string{lineLogin}, Sandbox: true, MinVersion: "1.0.0"}
}

func (a lineAdapter) Guard(string, string, []string, []string) agent.Launch {
	return agent.Launch{Env: a.launchEnv}
}

func (a lineAdapter) Sandbox(agent.SandboxPlan, string, string, []string, []string) agent.Launch {
	return agent.Launch{Env: a.launchEnv}
}

func (lineAdapter) Line(string) (string, string) { return "", "" }

func (lineAdapter) Calls(string) agent.Seen { return agent.Seen{} }

// traceRun runs the fake CLI line with every trace and telemetry variable of an owner in the engine environment, and returns the NAME=value lines that it saw.
func traceRun(t *testing.T, sandboxed bool, trace string, launchEnv map[string]string) []string {
	t.Helper()

	f := newFixture(t, "line", model.Agent("line"), "")
	f.spec.Adapter = lineAdapter{launchEnv: launchEnv}
	f.spec.Traceparent = trace

	if sandboxed {
		f.spec.Sandbox = &agent.SandboxPlan{Work: f.work}
	}

	for name, value := range map[string]string{
		"TRACEPARENT": ambientTrace, "TRACESTATE": "k=v",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector.example:4318", "OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Bearer s3cret",
		"OTEL_TRACES_EXPORTER": "otlp", "CLAUDE_CODE_ENABLE_TELEMETRY": "1", "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "1",
	} {
		f.spec.Env[name] = value
	}

	got := agent.Runner{Version: seen("1.0.0\n")}.Run(context.Background(), f.spec)
	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)

	lines := strings.Split(record(t, f.records, "env"), "\n")
	require.Equal(t, 1, countPrefix(lines, lineLogin+"="), "the fake CLI printed the environment of the run")

	return lines
}

func countPrefix(lines []string, prefix string) int {
	n := 0

	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}

	return n
}

func assertNoExporter(t *testing.T, lines []string) {
	t.Helper()

	for _, prefix := range []string{"TRACESTATE=", "OTEL_", "CLAUDE_CODE_ENABLE_TELEMETRY=", "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA="} {
		assert.Zero(t, countPrefix(lines, prefix), prefix)
	}
}

func TestRunner_ATraceparentOfTheSpecReachesTheAgentProcessOnceAndAloneOfTheExporterSettings(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	lines := traceRun(t, false, stepTrace, nil)

	assert.Equal(t, 1, countPrefix(lines, "TRACEPARENT="))
	assert.Contains(t, lines, "TRACEPARENT="+stepTrace, "the value of the step replaces the ambient one")
	assertNoExporter(t, lines)
}

func TestRunner_ASandboxedRunGetsTheTraceparentOfTheSpecAndNoExporterSetting(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	lines := traceRun(t, true, stepTrace, nil)

	assert.Equal(t, 1, countPrefix(lines, "TRACEPARENT="))
	assert.Contains(t, lines, "TRACEPARENT="+stepTrace)
	assertNoExporter(t, lines)
}

func TestRunner_ASpecWithNoTraceparentLeavesTheAgentWithNoTraceVariableSandboxedOrNot(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	for name, sandboxed := range map[string]bool{"plain": false, "sandboxed": true} {
		lines := traceRun(t, sandboxed, "", nil)

		assert.Zero(t, countPrefix(lines, "TRACEPARENT="), name)
		assertNoExporter(t, lines)
	}
}
