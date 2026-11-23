package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

const (
	stepTrace    = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	ambientTrace = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
)

// withAmbientTrace puts every trace and telemetry variable of an owner into the engine environment of the spec.
func withAmbientTrace(f *fixture) {
	for name, value := range map[string]string{
		"TRACEPARENT": ambientTrace, "TRACESTATE": "k=v",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector.example:4318", "OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Bearer s3cret",
		"OTEL_TRACES_EXPORTER": "otlp", "CLAUDE_CODE_ENABLE_TELEMETRY": "1", "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "1",
	} {
		f.spec.Env[name] = value
	}
}

func envLines(t *testing.T, f *fixture) []string {
	t.Helper()

	lines := strings.Split(record(t, f.records, "env"), "\n")
	require.Equal(t, 1, countPrefix(lines, "CLAUDE_CONFIG_DIR="), "the fake CLI printed the environment of the run")

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

func runBoxed(t *testing.T, sandboxed bool, trace string) []string {
	t.Helper()

	f := boxed(t)
	if !sandboxed {
		f.spec.Sandbox = nil
	}

	withAmbientTrace(f)

	f.spec.Traceparent = trace

	got := agent.Runner{Version: seen("2.1.330 (Claude Code)\n")}.Run(context.Background(), f.spec)
	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)

	return envLines(t, f)
}

func TestRunner_ATraceparentOfTheSpecReachesTheAgentProcessOnceAndAloneOfTheExporterSettings(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	lines := runBoxed(t, false, stepTrace)

	assert.Equal(t, 1, countPrefix(lines, "TRACEPARENT="))
	assert.Contains(t, lines, "TRACEPARENT="+stepTrace, "the value of the step replaces the ambient one")
	assertNoExporter(t, lines)
}

func TestRunner_ASandboxedRunGetsTheTraceparentOfTheSpecAndNoExporterSetting(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	lines := runBoxed(t, true, stepTrace)

	assert.Equal(t, 1, countPrefix(lines, "TRACEPARENT="))
	assert.Contains(t, lines, "TRACEPARENT="+stepTrace)
	assertNoExporter(t, lines)
}

func TestRunner_ASpecWithNoTraceparentLeavesTheAgentWithNoTraceVariableSandboxedOrNot(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	for name, sandboxed := range map[string]bool{"plain": false, "sandboxed": true} {
		lines := runBoxed(t, sandboxed, "")

		assert.Zero(t, countPrefix(lines, "TRACEPARENT="), name)
		assertNoExporter(t, lines)
	}
}
