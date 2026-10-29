package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

const (
	// UsageTimeout is the longest a usage read takes unless a UsageReader says otherwise.
	UsageTimeout = 30 * time.Second

	// exitGrace is how long a CLI that closed its output has to exit before it is killed.
	exitGrace = 500 * time.Millisecond

	streamJSON     = "stream-json"
	usageRequestID = "obee-usage"
	answerMark     = "control_response"
	noAnswer       = "the CLI ended without answering get_usage"
	noPlanLimits   = "the reply has no Claude plan limits (rate_limits_available is false)"
	maxPercent     = 100.0
	maxOutputBytes = 4 << 20
	readChunk      = 64 << 10
	maxStderrBytes = 64 << 10
	maxFloatBits   = 64
	bytesPerMiB    = 1 << 20
	resetLayout    = "2006-01-02T15:04:05"
)

var errReadTimeout = errors.New("read timeout")

func usageArgs() []string {
	return []string{"--output-format", streamJSON, "--verbose", "--input-format", streamJSON}
}

// UsageReader asks the Claude Code CLI for the usage of a runner. Base is the environment of the caller: the CLI gets the allowlisted names of it and the login of the runner.
// A zero Timeout means UsageTimeout.
type UsageReader struct {
	Base    map[string]string
	Timeout time.Duration
}

// Read returns the usage of the runner. Any failure is an unknown reading with the reason.
func (u UsageReader) Read(ctx context.Context, runner model.Runner) model.Reading {
	found, reason := u.locate(runner)
	if reason != "" {
		return model.Unknown(reason)
	}

	timeout := u.Timeout
	if timeout == 0 {
		timeout = UsageTimeout
	}

	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errReadTimeout)
	defer cancel()

	reading := ask(ctx, found, agent.LoginEnv(u.Base, New().Profile(), runner.ConfigDir), timeout)
	if reading.NeedsLogin() {
		return model.UnknownLogin(fmt.Sprintf("runner %s has no Claude plan limits, so it is not logged in or it uses an API key. Log in with `CLAUDE_CONFIG_DIR=%s claude` and run /login",
			runner.Name, runner.ConfigDir))
	}

	return reading
}

// locate returns the path of the CLI, or the reason the runner cannot be read.
func (u UsageReader) locate(runner model.Runner) (string, string) {
	if runner.Agent != model.AgentClaude {
		return "", fmt.Sprintf("usage is read for claude runners only, not %s", runner.Agent)
	}

	if runner.ConfigDir == "" {
		return "", "the runner has no config_dir, so its login is not known"
	}

	// The CLI makes an empty profile in a missing folder, so it is never started for one.
	if info, err := os.Stat(runner.ConfigDir); err != nil || !info.IsDir() {
		return "", fmt.Sprintf("the config_dir %s does not exist, so claude is not started", runner.ConfigDir)
	}

	found, err := proc.LookPath("claude", u.Base["PATH"])
	if err != nil {
		return "", "claude is not installed"
	}

	return found, ""
}

// ask runs the CLI and reads its answer. stdin stays open until the read ends: some CLIs stop reading requests when it closes.
func ask(ctx context.Context, binary string, env []string, timeout time.Duration) model.Reading {
	cmd := proc.GroupCommand(ctx, binary, usageArgs(), "/")
	cmd.Env = env
	stderr := proc.NewTail(maxStderrBytes)
	cmd.Stderr = stderr

	stdin, _ := cmd.StdinPipe()   //nolint:errcheck // a fresh command always has a stdin to open
	stdout, _ := cmd.StdoutPipe() //nolint:errcheck // a fresh command always has a stdout to open

	if err := cmd.Start(); err != nil {
		return model.Unknown("cannot start claude: " + err.Error())
	}

	// A CLI that quit without reading the request breaks this write. Its exit code says why, so the error is not the answer.
	_, _ = io.WriteString(stdin, usageRequest()) //nolint:errcheck // see above

	reading, found := converse(stdout)
	waitErr := reap(cmd, !found)

	if found {
		return reading
	}

	return unanswered(ctx, waitErr, stderr.Text(), timeout)
}

// unanswered returns the reason a CLI gave no answer.
func unanswered(ctx context.Context, waitErr error, stderr string, timeout time.Duration) model.Reading {
	switch {
	case errors.Is(context.Cause(ctx), errReadTimeout):
		return model.Unknown(fmt.Sprintf("no answer within %s seconds", strconv.FormatFloat(timeout.Seconds(), 'g', -1, maxFloatBits)))
	case ctx.Err() != nil:
		return model.Unknown("the read was canceled")
	}

	// A CLI that we killed has no exit code of its own, so only a plain nonzero exit is a reason.
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) && exit.ExitCode() > 0 {
		return model.Unknown(strings.TrimRight(fmt.Sprintf("claude exited %d: %s", exit.ExitCode(), lastLine(stderr)), ": "))
	}

	return model.Unknown(noAnswer)
}

// reap ends the read: it kills the group of the CLI and waits for it. A child that holds no pipe must not outlive the read.
// With grace, a CLI that closed its output gets a moment to exit on its own first, so that its exit code is its own.
func reap(cmd *exec.Cmd, grace bool) error {
	done := make(chan error, 1)

	go func() { done <- cmd.Wait() }()

	if grace {
		select {
		case err := <-done:
			_ = proc.KillGroup(cmd) //nolint:errcheck // ESRCH means nothing was left to kill

			return err
		case <-time.After(exitGrace):
		}
	}

	_ = proc.KillGroup(cmd) //nolint:errcheck // ESRCH means nothing was left to kill

	return <-done
}

func usageRequest() string {
	request, _ := json.Marshal(map[string]any{ //nolint:errcheck // a fixed map of strings and a bool always encodes
		"type":       "control_request",
		"request_id": usageRequestID,
		"request":    map[string]any{"subtype": "get_usage", "skip_behaviors": true},
	})

	return string(request) + "\n"
}

// converse reads stdout until the answer, the size cap or the end of output. found is false when the output ended without an answer.
func converse(stdout io.Reader) (model.Reading, bool) {
	var pending []byte

	total := 0
	chunk := make([]byte, readChunk)

	for {
		n, err := stdout.Read(chunk)
		total += n

		reading, found, rest := scan(append(pending, chunk[:n]...), total)
		pending = rest

		if found {
			return reading, true
		}

		if err != nil {
			return answerIn(pending)
		}
	}
}

// scan looks for the answer in the complete lines of data. It returns the incomplete line after them. Output past the size cap is an answer too: unknown.
func scan(data []byte, total int) (model.Reading, bool, []byte) {
	lines, rest := outputLines(data)
	for _, line := range lines {
		if reading, ok := answerIn(line); ok {
			return reading, true, rest
		}
	}

	if total > maxOutputBytes {
		return model.Unknown(fmt.Sprintf("the CLI printed more than %d MB", maxOutputBytes/bytesPerMiB)), true, rest
	}

	return model.Reading{}, false, rest
}

// outputLines returns the complete lines of data and the incomplete line after them.
func outputLines(data []byte) ([][]byte, []byte) {
	parts := bytes.Split(data, []byte("\n"))

	return parts[:len(parts)-1], parts[len(parts)-1]
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")

	return strings.TrimSpace(lines[len(lines)-1])
}
