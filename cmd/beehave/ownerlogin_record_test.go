package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

func TestReadLogin_ARecordInTheStateFolderIsTheLoginAndGitReadsNoConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownerLoginFile), []byte(`{"settings":["-c","credential.helper=recorded"]}`), reportMode))

	w := &writers{}
	require.NoError(t, w.readLogin(t.Context(), dir))

	assert.Equal(t, []string{"-c", "credential.helper=recorded"}, w.login.Settings())
}

func TestReadLogin_ARecordThatIsNotJsonIsAnErrorThatNamesTheFileAndTheWayOut(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, ownerLoginFile)
	require.NoError(t, os.WriteFile(file, []byte("{"), reportMode))

	err := (&writers{}).readLogin(t.Context(), dir)

	require.ErrorContains(t, err, "read the record of the owner login "+file)
	require.ErrorContains(t, err, "Delete the file to read the git config of the owner again")
}

func TestReadLogin_AStateFolderThatIsAFileIsAnErrorOfTheRecord(t *testing.T) {
	t.Parallel()

	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, reportMode))

	err := (&writers{}).readLogin(t.Context(), blocker)

	require.ErrorContains(t, err, "read the record of the owner login: ")
}

func TestOwnerLogin_AClientCommandReadsTheRecordOfTheStateFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownerLoginFile), []byte("{"), reportMode))
	opts := flow.Options{StateDir: dir, Version: "v1", MaxConcurrent: 1, MaxReviewConcurrent: 1, Env: map[string]string{}}

	_, err := flowFactory().Client(t.Context(), config.Config{}, opts)

	require.ErrorContains(t, err, "read the record of the owner login")
}

func TestRecordLogin_AStateFolderThatCannotHoldTheRecordIsAnError(t *testing.T) {
	t.Parallel()

	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, reportMode))

	err := recordLogin(filepath.Join(blocker, ownerLoginFile), gitexec.OwnerLoginOf(nil))

	require.ErrorContains(t, err, "write the record of the owner login: ")
}

func TestRecordLogin_ARelativeStateFolderIsNotRecorded(t *testing.T) {
	t.Parallel()

	name := filepath.Join("relative-state-folder-of-a-test", ownerLoginFile)

	require.NoError(t, recordLogin(name, gitexec.OwnerLoginOf(nil)))

	_, err := os.Stat(name)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
