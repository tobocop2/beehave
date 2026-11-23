package host_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

const jobFolder = "/home/ci/beehave-gate-one-abc"

func TestBankFolder_ScansOnlyTheFolderOfTheJobWhenItExists(t *testing.T) {
	t.Parallel()

	probe := proctest.Always(model.CommandResult{})
	find := proctest.Always(model.CommandResult{})
	git := proctest.Always(model.CommandResult{})

	err := host.BankFolder(t.Context(), git, find, probe, jobFolder, "git@box:org/repo.git")

	require.ErrorIs(t, err, host.ErrNoRepos, "a folder with no repository holds no work")
	assert.Equal(t, [][]string{{"-d", jobFolder}}, probe.Calls)
	require.Len(t, find.Calls, 1)
	assert.Equal(t, []string{"-H", jobFolder}, find.Calls[0][:2], "the scan root is the folder of the job, not the work root")
	assert.Empty(t, git.Calls)
}

func TestBankFolder_AFolderThatIsGoneHoldsNoWorkAndIsNotScanned(t *testing.T) {
	t.Parallel()

	probe := proctest.Always(model.CommandResult{ExitCode: 1})
	find := proctest.Always(model.CommandResult{})

	err := host.BankFolder(t.Context(), proctest.Always(model.CommandResult{}), find, probe, jobFolder, "git@box:org/repo.git")

	require.ErrorIs(t, err, host.ErrNoRepos)
	assert.Contains(t, err.Error(), jobFolder)
	assert.Empty(t, find.Calls)
}

func TestBankFolder_AProbeThatFailsIsARefusalAndNotAScan(t *testing.T) {
	t.Parallel()

	probe := proctest.Always(model.CommandResult{ExitCode: 255, Stderr: "ssh: connection refused\n"})
	find := proctest.Always(model.CommandResult{})

	err := host.BankFolder(t.Context(), proctest.Always(model.CommandResult{}), find, probe, jobFolder, "git@box:org/repo.git")

	require.ErrorContains(t, err, "check the folder "+jobFolder+": exit 255: ssh: connection refused")
	require.NotErrorIs(t, err, host.ErrNoRepos)
	assert.Empty(t, find.Calls)
}
