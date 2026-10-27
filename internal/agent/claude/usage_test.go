package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tobocop2/obee/internal/agent/claude"
	"github.com/tobocop2/obee/internal/model"
)

const (
	scriptMode = 0o700
	fileMode   = 0o600
	// scriptHead logs the arguments, the folder, the environment and the request of the fake CLI.
	scriptHead = "#!/bin/sh\nd=$(dirname \"$0\")\necho \"args=$*\" > \"$d/log\"\npwd >> \"$d/log\"\nenv > \"$d/env\"\nIFS= read -r request\necho \"$request\" >> \"$d/log\"\n"
	answer     = "cat \"$d/reply\"\n"
)

var (
	t1 = time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC)
	t2 = time.Date(2026, 10, 25, 7, 0, 0, 0, time.UTC)
)

func window(percent, resets any) map[string]any {
	return map[string]any{"utilization": percent, "resets_at": resets}
}

func replyOf(id string, five, seven any) string {
	limits := map[string]any{"five_hour": five, "seven_day": seven}
	text, _ := json.Marshal(map[string]any{ //nolint:errcheck // a map of strings and numbers always encodes
		"type": "control_response",
		"response": map[string]any{
			"request_id": id, "subtype": "success", "response": map[string]any{"rate_limits": limits},
		},
	})

	return string(text) + "\n"
}

func reply(five, seven any) string { return replyOf("obee-usage", five, seven) }

func goodReply() string {
	return reply(window(12.5, "2026-10-24T05:00:00Z"), window(40, "2026-10-25T07:00:00Z"))
}

func write(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

// fakeCLI is a claude script on its own PATH folder.
type fakeCLI struct {
	dir  string
	base map[string]string
}

// fake writes a claude script that logs the request and then runs body. The reply file holds goodReply.
func fake(t *testing.T, body string) fakeCLI {
	t.Helper()

	dir := t.TempDir()
	write(t, filepath.Join(dir, "claude"), scriptHead+body, scriptMode)
	write(t, filepath.Join(dir, "reply"), goodReply(), fileMode)

	return fakeCLI{dir: dir, base: map[string]string{"PATH": dir + ":/usr/bin:/bin"}}
}

func (f fakeCLI) set(t *testing.T, name, text string) {
	t.Helper()
	write(t, filepath.Join(f.dir, name), text, fileMode)
}

func (f fakeCLI) lines(t *testing.T, name string) []string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(f.dir, name)) //nolint:gosec // the folder is the test's own temporary folder
	if err != nil {
		t.Fatal(err)
	}

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (f fakeCLI) environment(t *testing.T) map[string]string {
	t.Helper()

	seen := map[string]string{}

	for _, line := range f.lines(t, "env") {
		if name, value, ok := strings.Cut(line, "="); ok {
			seen[name] = value
		}
	}

	return seen
}

func login(t *testing.T) model.Runner {
	t.Helper()

	return model.Runner{Name: "a", Agent: model.AgentClaude, ConfigDir: t.TempDir()}
}

func (f fakeCLI) read(t *testing.T, runner model.Runner) model.Reading {
	t.Helper()

	return claude.UsageReader{Base: f.base}.Read(t.Context(), runner)
}

func usageOf(t *testing.T, reading model.Reading) model.Usage {
	t.Helper()

	usage, ok := reading.Usage()
	if !ok {
		t.Fatalf("unknown: %s", reading.Reason())
	}

	return usage
}

func wantUnknown(t *testing.T, reading model.Reading, part string) {
	t.Helper()

	if _, ok := reading.Usage(); ok {
		t.Fatalf("got usage, want unknown with %q", part)
	}

	if !strings.Contains(reading.Reason(), part) {
		t.Fatalf("reason = %q, want %q", reading.Reason(), part)
	}
}

// --- the parse of the reply ---

func TestParseUsage_TheReplyGivesBothWindowsWithTheirResetTimes(t *testing.T) {
	t.Parallel()

	other := `{"type":"control_response","response":{"request_id":"other"}}`
	noise := "not json\n[1]\n{\"type\":\"assistant\"}\n" + other + "\n"

	usage := usageOf(t, claude.ParseUsage(noise+reply(window(12.5, "2026-10-24T05:00:00Z"), window(40, "2026-10-25T07:00:00+00:00"))))

	if usage.FiveHour.Percent != 12.5 || !usage.FiveHour.ResetsAt.Equal(t1) || usage.SevenDay.Percent != 40 || !usage.SevenDay.ResetsAt.Equal(t2) {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestParseUsage_ANaiveResetTimeIsUTCAndABadOneIsDropped(t *testing.T) {
	t.Parallel()

	usage := usageOf(t, claude.ParseUsage(reply(window(1, "2026-10-24T05:00:00"), window(1, "garbage"))))
	if !usage.FiveHour.ResetsAt.Equal(t1) || !usage.SevenDay.ResetsAt.IsZero() {
		t.Fatalf("usage = %+v", usage)
	}

	usage = usageOf(t, claude.ParseUsage(reply(window(1, nil), window(1, "2026-10-25T07:00:00Z"))))
	if !usage.FiveHour.ResetsAt.IsZero() {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestParseUsage_AUtilizationOutside0To100IsUnknown(t *testing.T) {
	t.Parallel()

	for name, percent := range map[string]any{
		"negative": -5, "over": 140, "just over": 100.5, "huge": json.RawMessage("1e400"), "tiny negative": -0.1,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			wantUnknown(t, claude.ParseUsage(reply(window(percent, nil), window(1, nil))), "utilization out of range")
			wantUnknown(t, claude.ParseUsage(reply(window(1, nil), window(percent, nil))), "utilization out of range")
		})
	}
}

func TestParseUsage_TheEndsOf0To100AreValid(t *testing.T) {
	t.Parallel()

	usage := usageOf(t, claude.ParseUsage(reply(window(0, nil), window(100, nil))))
	if usage.FiveHour.Percent != 0 || usage.SevenDay.Percent != 100 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestParseUsage_AReplyWithoutBothWindowsIsUnknownWithTheReason(t *testing.T) {
	t.Parallel()

	envelope := func(inner map[string]any) string {
		text, err := json.Marshal(map[string]any{"type": "control_response", "response": inner})
		if err != nil {
			t.Fatal(err)
		}

		return string(text)
	}

	cases := []struct{ name, text, reason string }{
		{"empty output", "", "ended without answering"},
		{"no five-hour window", reply(nil, window(1, nil)), "no five_hour window"},
		{"no seven-day window", reply(window(1, nil), nil), "no seven_day window"},
		{"null utilization", reply(window(nil, nil), window(1, nil)), "five_hour window has no utilization"},
		{"text utilization", reply(window("9", nil), window(1, nil)), "five_hour window has no utilization"},
		{"bool utilization", reply(window(true, nil), window(1, nil)), "five_hour window has no utilization"},
		{"no seven-day utilization", reply(window(1, nil), window(nil, nil)), "seven_day window has no utilization"},
		{"no rate_limits", envelope(map[string]any{"request_id": "obee-usage", "subtype": "success", "response": map[string]any{}}), "no rate_limits"},
		{"an error answer", envelope(map[string]any{"request_id": "obee-usage", "subtype": "error", "error": " nope "}), "refused get_usage: nope"},
		{"an error answer without an error", envelope(map[string]any{"request_id": "obee-usage", "subtype": "error"}), "refused get_usage:"},
		{"a response that is not an object", envelope(map[string]any{"request_id": "obee-usage", "subtype": "success", "response": "x"}), "no rate_limits"},
		{"an envelope without a response object", `{"type":"control_response","response":"x"}`, "ended without answering"},
		{"the mark in a line that is not JSON", "control_response {", "ended without answering"},
		{"the mark in a message of another type", `{"type":"assistant","text":"control_response"}`, "ended without answering"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			wantUnknown(t, claude.ParseUsage(tc.text), tc.reason)
		})
	}
}

// --- the reader, against a fake CLI ---

func TestUsageReader_SendsGetUsageAndReturnsTheWindows(t *testing.T) {
	t.Parallel()

	cli := fake(t, answer)

	usage := usageOf(t, cli.read(t, login(t)))
	if usage.FiveHour.Percent != 12.5 || usage.SevenDay.Percent != 40 {
		t.Fatalf("usage = %+v", usage)
	}

	log := cli.lines(t, "log")
	if log[0] != "args=--output-format stream-json --verbose --input-format stream-json" || log[1] != "/" {
		t.Fatalf("log = %q", log)
	}

	wantUsageRequest(t, log[2])
}

func wantUsageRequest(t *testing.T, line string) {
	t.Helper()

	var request struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype       string `json:"subtype"`
			SkipBehaviors bool   `json:"skip_behaviors"`
		} `json:"request"`
	}

	if err := json.Unmarshal([]byte(line), &request); err != nil {
		t.Fatal(err)
	}

	if request.Type != "control_request" || request.RequestID != "obee-usage" || request.Request.Subtype != "get_usage" || !request.Request.SkipBehaviors {
		t.Fatalf("request = %+v", request)
	}
}

func TestUsageReader_UsesTheLoginOfTheRunnerOnly(t *testing.T) {
	t.Parallel()

	cli := fake(t, answer)
	runner := login(t)
	cli.base["CLAUDE_CONFIG_DIR"] = "/parent/real"

	cli.read(t, runner)

	seen := cli.environment(t)
	if seen["CLAUDE_CONFIG_DIR"] != runner.ConfigDir || seen["CLAUDE_SECURESTORAGE_CONFIG_DIR"] != runner.ConfigDir {
		t.Fatalf("env = %v", seen)
	}
}

func TestUsageReader_NoCredentialAndNoUnknownVariableReachesTheCLI(t *testing.T) {
	t.Parallel()

	cli := fake(t, answer)
	leaks := []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_FOUNDRY_API_KEY", "ANTHROPIC_IDENTITY_TOKEN",
		"ANTHROPIC_CUSTOM_HEADERS", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
		"CLAUDE_CODE_USE_GATEWAY", "A_VARIABLE_NOBODY_HEARD_OF",
	}

	for _, name := range leaks {
		cli.base[name] = "LEAK"
	}

	cli.base["LC_ALL"] = "C"

	usageOf(t, cli.read(t, login(t)))

	seen := cli.environment(t)
	for _, name := range leaks {
		if _, found := seen[name]; found {
			t.Errorf("%s reached the CLI", name)
		}
	}

	if seen["LC_ALL"] != "C" {
		t.Fatalf("LC_ALL = %q", seen["LC_ALL"])
	}
}

func TestUsageReader_NetworkSettingsReachTheCLIAndACredentialDoesNot(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"HTTPS_PROXY", "https_proxy", "NO_PROXY", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "HOME"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cli := fake(t, answer)
			cli.base[name] = "seen-value"

			usageOf(t, cli.read(t, login(t)))

			if got := cli.environment(t)[name]; got != "seen-value" {
				t.Fatalf("%s = %q", name, got)
			}
		})
	}
}

func TestUsageReader_ASlowCLIIsStoppedAtTheTimeoutAndIsUnknown(t *testing.T) {
	t.Parallel()

	cli := fake(t, "sleep 5\n"+answer)

	reading := claude.UsageReader{Base: cli.base, Timeout: 300 * time.Millisecond}.Read(t.Context(), login(t))

	if reading.Reason() != "no answer within 0.3 seconds" {
		t.Fatalf("reason = %q", reading.Reason())
	}
}

func TestUsageReader_ACanceledContextIsUnknown(t *testing.T) {
	t.Parallel()

	cli := fake(t, "sleep 5\n"+answer)
	ctx, cancel := context.WithCancel(t.Context())

	time.AfterFunc(200*time.Millisecond, cancel)

	wantUnknown(t, claude.UsageReader{Base: cli.base}.Read(ctx, login(t)), "the read was canceled")
}

func TestUsageReader_TheDefaultTimeoutIs30Seconds(t *testing.T) {
	t.Parallel()

	if claude.UsageTimeout != 30*time.Second {
		t.Fatalf("UsageTimeout = %v", claude.UsageTimeout)
	}
}

func TestUsageReader_BytesThatAreNotTextAreReplacedNotFatal(t *testing.T) {
	t.Parallel()

	cli := fake(t, answer)
	good := goodReply()

	cli.set(t, "reply", "\xff\xfe junk\n"+good)
	usageOf(t, cli.read(t, login(t)))

	cli.set(t, "reply", "{\"type\": \"control_response\", \"x\": \"\xff\xfe\"}\n"+good)
	usageOf(t, cli.read(t, login(t)))

	wantUnknown(t, fake(t, "printf 'bad \\377\\376' >&2\nexit 2\n").read(t, login(t)), "claude exited 2: bad")
}

func TestUsageReader_EndlessOutputStopsAtTheSizeCap(t *testing.T) {
	t.Parallel()

	for name, flood := range map[string]string{
		"lines":    "yes '{\"type\":\"assistant\"}'\n",
		"one line": "yes x | tr -d '\\n'\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cli := fake(t, flood)
			start := time.Now()

			reading := claude.UsageReader{Base: cli.base, Timeout: 10 * time.Second}.Read(t.Context(), login(t))

			if reading.Reason() != "the CLI printed more than 4 MB" || time.Since(start) > 8*time.Second {
				t.Fatalf("reason = %q after %v", reading.Reason(), time.Since(start))
			}
		})
	}
}

func TestUsageReader_StopsAtTheAnswerAndDoesNotWaitForTheCLI(t *testing.T) {
	t.Parallel()

	cli := fake(t, answer+"sleep 30\n")
	start := time.Now()

	usageOf(t, claude.UsageReader{Base: cli.base, Timeout: 10 * time.Second}.Read(t.Context(), login(t)))

	if time.Since(start) > 5*time.Second {
		t.Fatalf("the read took %v", time.Since(start))
	}
}

func TestUsageReader_AChildThatOutlivesTheAnswerIsKilled(t *testing.T) {
	t.Parallel()

	cli := fake(t, "sleep 30 &\necho $! > \"$d/child\"\n"+answer+"sleep 30\n")

	usageOf(t, cli.read(t, login(t)))

	var pid int

	if err := json.Unmarshal([]byte(cli.lines(t, "child")[0]), &pid); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("the child %d is still running", pid)
	}
}

func TestUsageReader_AChildOfACLIThatClosedOutputAndExitedInsideTheGraceTimeIsKilled(t *testing.T) {
	t.Parallel()

	cli := fake(t, "sleep 30 >/dev/null 2>&1 &\necho $! > \"$d/child\"\nexec 1>&-\nexit 0\n")

	wantUnknown(t, cli.read(t, login(t)), "ended without answering")

	var pid int

	if err := json.Unmarshal([]byte(cli.lines(t, "child")[0]), &pid); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("the child %d is still running", pid)
	}
}

func TestUsageReader_AReplyLineWithoutANewlineAtTheEndOfOutputIsRead(t *testing.T) {
	t.Parallel()

	cli := fake(t, "printf '%s' \"$(cat \"$d/reply\")\"\n")

	usageOf(t, cli.read(t, login(t)))
}

func TestUsageReader_ACLIThatClosesOutputAndLingersIsKilledAtOnce(t *testing.T) {
	t.Parallel()

	cli := fake(t, "exec 1>&-\nsleep 30\n")
	start := time.Now()

	wantUnknown(t, claude.UsageReader{Base: cli.base, Timeout: 10 * time.Second}.Read(t.Context(), login(t)), "ended without answering")

	if time.Since(start) > 5*time.Second {
		t.Fatalf("the read took %v", time.Since(start))
	}
}

func TestUsageReader_AnEnvironmentWithANullByteIsUnknownNotAnError(t *testing.T) {
	t.Parallel()

	cli := fake(t, answer)
	cli.base["LANG"] = "a\x00b"

	wantUnknown(t, cli.read(t, login(t)), "cannot start claude")
}

func TestUsageReader_ARunnerThatCannotBeReadIsUnknownAndClaudeIsNotStarted(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	write(t, file, "x", fileMode)

	cases := []struct {
		name   string
		runner model.Runner
		reason string
	}{
		{"another agent", model.Runner{Name: "o", Agent: model.AgentOpenCode, ConfigDir: t.TempDir()}, "usage is read for claude runners only, not opencode"},
		{"no config_dir", model.Runner{Name: "a", Agent: model.AgentClaude}, "no config_dir"},
		{"a missing config_dir", model.Runner{Name: "a", Agent: model.AgentClaude, ConfigDir: filepath.Join(t.TempDir(), "none")}, "does not exist, so claude is not started"},
		{"a config_dir that is a file", model.Runner{Name: "a", Agent: model.AgentClaude, ConfigDir: file}, "does not exist, so claude is not started"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cli := fake(t, answer)

			wantUnknown(t, cli.read(t, tc.runner), tc.reason)

			if _, err := os.Stat(filepath.Join(cli.dir, "log")); err == nil {
				t.Fatal("claude was started")
			}
		})
	}
}

func TestUsageReader_AnAnswerToAnotherRequestIsSkipped(t *testing.T) {
	t.Parallel()

	cli := fake(t, answer)
	cli.set(t, "reply", replyOf("other", window(99, nil), window(99, nil))+goodReply())

	if usage := usageOf(t, cli.read(t, login(t))); usage.FiveHour.Percent != 12.5 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestUsageReader_ACLIThatQuitsWithoutReadingTheRequestIsUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, "claude"), "#!/bin/sh\necho 'no login' >&2\nexit 3\n", scriptMode)

	reading := claude.UsageReader{Base: map[string]string{"PATH": dir + ":/usr/bin:/bin"}}.Read(t.Context(), login(t))

	if reading.Reason() != "claude exited 3: no login" {
		t.Fatalf("reason = %q", reading.Reason())
	}
}

func TestUsageReader_ANonzeroExitWithoutAnAnswerIsUnknownWithTheExitCode(t *testing.T) {
	t.Parallel()

	cli := fake(t, "echo first >&2\necho last >&2\nexit 4\n")

	if reading := cli.read(t, login(t)); reading.Reason() != "claude exited 4: last" {
		t.Fatalf("reason = %q", reading.Reason())
	}

	bare := fake(t, "exit 5\n")
	if reading := bare.read(t, login(t)); reading.Reason() != "claude exited 5" {
		t.Fatalf("reason = %q", reading.Reason())
	}
}

func TestUsageReader_AnAnswerWinsOverANonzeroExit(t *testing.T) {
	t.Parallel()

	usageOf(t, fake(t, answer+"exit 4\n").read(t, login(t)))
}

func TestUsageReader_ACleanExitWithoutAnAnswerIsUnknown(t *testing.T) {
	t.Parallel()

	wantUnknown(t, fake(t, "exit 0\n").read(t, login(t)), "the CLI ended without answering get_usage")
}

func TestUsageReader_AMissingClaudeIsUnknown(t *testing.T) {
	t.Parallel()

	reading := claude.UsageReader{Base: map[string]string{"PATH": t.TempDir()}}.Read(t.Context(), login(t))

	wantUnknown(t, reading, "claude is not installed")
}

func TestUsageReader_ACLIThatCannotStartIsUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, "claude"), "#!/nonexistent/interpreter\n", scriptMode)

	wantUnknown(t, claude.UsageReader{Base: map[string]string{"PATH": dir}}.Read(t.Context(), login(t)), "cannot start claude")
}
