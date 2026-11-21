package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSend_SendsTheTextAsAJSONStringAndPrintsTheID(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.env["USER"] = "ann"

	code, stdout, stderr := rig.execute(t.Context(), "send", "12", "--topic", "go", "--key", "k1", "ship", "it")
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, []string{`send 12 go k1 cli:ann "ship it"`}, rig.steering.calls)
	assert.Equal(t, "id-1\n", stdout)
}

func TestSend_ReadsTheValueFromTheFileOfJSON(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	file := filepath.Join(t.TempDir(), "v.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"n": 1}`), 0o600))

	code, _, stderr := rig.execute(t.Context(), "send", "12", "--topic", "go", "--json", file)
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, []string{`send 12 go  cli: {"n": 1}`}, rig.steering.calls)
}

func TestSend_RefusesBothAValueAndAFileOrNeither(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"both":    {"send", "12", "--topic", "go", "--json", "v.json", "text"},
		"neither": {"send", "12", "--topic", "go"},
	} {
		rig := newRig(t)

		code, _, stderr := rig.execute(t.Context(), args...)

		assert.Equal(t, 1, code, name)
		assert.Contains(t, stderr, "give the value as text after the item, or as a file with --json", name)
		assert.Empty(t, rig.steering.calls, name)
	}
}

func TestSend_ReportsAFileThatCannotBeRead(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "send", "12", "--topic", "go", "--json", filepath.Join(t.TempDir(), "missing.json"))

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "read the value of the message")
	assert.Empty(t, rig.steering.calls)
}

func TestSend_ReportsTheRefusalOfTheRunAndPrintsNoID(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.err = errors.New("item 12 has no open run")

	code, stdout, stderr := rig.execute(t.Context(), "send", "12", "--topic", "go", "x")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "item 12 has no open run")
	assert.Empty(t, stdout)
}
