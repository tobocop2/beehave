package agent

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedact_RemovesSecretsAndKeyShapedTokens(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "a [redacted] b [redacted]", Redactor("s3cret")("a s3cret b sk-ant-abcdefgh12"))
	assert.Equal(t, "plain", Redactor()("plain"))
}

func TestRedactWriter_DropsALineLongerThanTheLimitAndKeepsTheNext(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	writer := &redactWriter{out: &out, redactor: newRedactor("s3cret"), limit: 10}
	_, err := writer.Write([]byte("0123456789ab"))
	require.NoError(t, err)
	_, err = writer.Write([]byte("cdef s3cret\nnext s3cret\n"))
	require.NoError(t, err)

	assert.Equal(t, droppedLine+"next [redacted]\n", out.String())
}

func TestRedactWriter_FlushOfADroppedLineWritesTheMarker(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	writer := &redactWriter{out: &out, redactor: newRedactor(), limit: 4}
	_, err := writer.Write([]byte("0123456789"))
	require.NoError(t, err)
	require.NoError(t, writer.flush())

	assert.Equal(t, droppedLine, out.String())
}

func TestRedactWriter_FlushWithNothingPendingWritesNothing(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	writer := &redactWriter{out: &out, redactor: newRedactor(), limit: 4}
	require.NoError(t, writer.flush())
	assert.Empty(t, out.String())
}

func TestTailBuffer_KeepsTheLastBytesOnly(t *testing.T) {
	t.Parallel()

	tail := &tailBuffer{}
	_, err := tail.Write(bytes.Repeat([]byte("a"), tailBytes))
	require.NoError(t, err)
	_, err = tail.Write([]byte("end"))
	require.NoError(t, err)

	assert.Len(t, tail.data, tailBytes)
	assert.Equal(t, "end", string(tail.data[tailBytes-3:]))
}

func TestBuildEnv_KeepsOnlyTheAllowlistTheLoginAndTheToken(t *testing.T) {
	t.Parallel()

	base := map[string]string{"PATH": "/bin", "LC_CTYPE": "UTF-8", "AWS_SECRET_ACCESS_KEY": "x", "CLAUDE_CONFIG_DIR": "/old"}
	p := Profile{ //nolint:gosec // the token variable name, not a credential
		ConfigVars: []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"},
		TokenVar:   "CLAUDE_CODE_OAUTH_TOKEN",
	}

	got := buildEnv(base, p, "/login", "tok")

	assert.Equal(t, []string{
		"CLAUDE_CODE_OAUTH_TOKEN=tok", "CLAUDE_CONFIG_DIR=/login", "CLAUDE_SECURESTORAGE_CONFIG_DIR=/login",
		"LC_CTYPE=UTF-8", "PATH=/bin",
	}, got)
}

func TestLoginEnv_HoldsTheAllowlistAndTheLoginButNoToken(t *testing.T) {
	t.Parallel()

	base := map[string]string{"PATH": "/bin", "CLAUDE_CODE_OAUTH_TOKEN": "x", "AWS_SECRET_ACCESS_KEY": "x"}
	p := Profile{ConfigVars: []string{"CLAUDE_CONFIG_DIR"}, TokenVar: "CLAUDE_CODE_OAUTH_TOKEN"} //nolint:gosec // the token variable name, not a credential

	assert.Equal(t, []string{"CLAUDE_CONFIG_DIR=/login", "PATH=/bin"}, LoginEnv(base, p, "/login"))
}

func TestLoginEnv_KeepsTheSocketOfTheSSHAgentAndNoGitOrTokenVariable(t *testing.T) {
	t.Parallel()

	base := map[string]string{"SSH_AUTH_SOCK": "/tmp/agent.sock", "GH_TOKEN": "x", "GITHUB_TOKEN": "x", "GIT_SSH_COMMAND": "ssh -i k"}

	assert.Equal(t, []string{"SSH_AUTH_SOCK=/tmp/agent.sock"}, LoginEnv(base, Profile{}, ""))
}

type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestRedactWriter_ASinkErrorOnACompleteLineStopsTheWrite(t *testing.T) {
	t.Parallel()

	writer := &redactWriter{out: refusingWriter{}, redactor: newRedactor(), limit: 100}
	written, err := writer.Write([]byte("line\n"))

	require.Error(t, err)
	assert.Zero(t, written)
}

func TestTailBuffer_NeverStartsInTheMiddleOfACharacter(t *testing.T) {
	t.Parallel()

	tail := &tailBuffer{}
	_, err := tail.Write([]byte(strings.Repeat("\u20ac", 2000))) // 6000 bytes; the cut at 4096 lands inside a character
	require.NoError(t, err)

	assert.True(t, utf8.Valid(tail.data))
	assert.LessOrEqual(t, len(tail.data), tailBytes)
	assert.Greater(t, len(tail.data), tailBytes-3)
	assert.True(t, strings.HasPrefix(string(tail.data), "\u20ac"))
}
