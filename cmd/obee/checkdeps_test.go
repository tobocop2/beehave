package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
)

func TestCheckDeps_EveryCheckCommandIsInTheBuiltTree(t *testing.T) {
	t.Parallel()

	var withDeps, bare bytes.Buffer

	require.Zero(t, cli.Execute(t.Context(), []string{"check", "--help"}, cli.BuildInfo{Check: checkDeps(nil)}, nil, &withDeps, &withDeps))
	require.Zero(t, cli.Execute(t.Context(), []string{"check", "--help"}, cli.BuildInfo{}, nil, &bare, &bare))

	for _, name := range []string{"skills", "papers", "scope", "transcript"} {
		assert.Contains(t, withDeps.String(), "  "+name+" ", name)
		assert.NotContains(t, bare.String(), "  "+name+" ", name)
	}
}

func TestCheckDeps_TheLedgerReadsAnswerFromTheStateFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	deps := checkDeps(map[string]string{"OBEE_STATE_DIR": dir})

	closes, err := deps.Closes("1")
	require.NoError(t, err)
	assert.Empty(t, closes)

	adds, err := deps.OwnerAdds("1")
	require.NoError(t, err)
	assert.Empty(t, adds)
}

func TestCheckDeps_ARelativeStateFolderFailsEachLedgerRead(t *testing.T) {
	t.Parallel()

	deps := checkDeps(map[string]string{"OBEE_STATE_DIR": "rel"})

	_, closesErr := deps.Closes("1")
	_, addsErr := deps.OwnerAdds("1")

	require.Error(t, closesErr)
	require.Error(t, addsErr)
}

func TestCheckDeps_TheForgeNeedsARepo(t *testing.T) {
	t.Parallel()

	_, err := checkDeps(nil).PRBody(t.Context(), "", 1)

	require.Error(t, err)
}

func TestCheckDeps_TheForgeReadsThePullRequestBodyThroughGh(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho 'Closes #7'\n"), 0o700)) //nolint:gosec // a test script must be executable
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")

	body, err := checkDeps(nil).PRBody(t.Context(), "o/r", 1)

	require.NoError(t, err)
	assert.Contains(t, body, "Closes #7")
}

func TestCheckDeps_TheSkillAndBashReadsFailOnAMissingTranscript(t *testing.T) {
	t.Parallel()

	deps := checkDeps(nil)
	missing := filepath.Join(t.TempDir(), "none.jsonl")

	_, skillErr := deps.Skills(missing, "")
	_, bashErr := deps.Bash(missing)

	require.Error(t, skillErr)
	require.Error(t, bashErr)
}
