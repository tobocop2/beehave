package slurmtest

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/model"
)

// call runs the words as a command on the head node of the cluster.
func call(c *Cluster, words ...string) model.CommandResult {
	return c.SSH().Run(context.Background(), append([]string{"-o", "BatchMode=yes", Target}, words...))
}

func up(mode hosttest.Mode) *Cluster {
	c := New(mode)
	c.Up = true

	return c
}

func submit(c *Cluster, command string) string {
	return strings.TrimSpace(call(c, "sbatch", "--parsable", "--job-name=g", "--wrap="+command).Stdout)
}

func TestSSH_AClusterThatIsDownDropsTheLinkAndABrokenOneFailsEveryCommand(t *testing.T) {
	t.Parallel()

	assert.Equal(t, sshFailed, call(New(hosttest.Working), "pwd").ExitCode)
	assert.Equal(t, sshFailed, call(New(hosttest.Broken), "pwd").ExitCode, "a broken cluster that was never up does not answer")
	assert.Equal(t, 1, call(up(hosttest.Broken), "pwd").ExitCode, "the link is up and the command fails, so it is not a dropped link")
}

func TestSSH_ALinkToAMachineSendsTheCommandAfterTheDoubleDash(t *testing.T) {
	t.Parallel()

	c := up(hosttest.Working)
	got := c.SSH().Run(t.Context(), []string{"-o", "BatchMode=yes", "-o", "Port=22", "--", Target, "pwd"})

	assert.Equal(t, 0, got.ExitCode)
	assert.Equal(t, "/home/lane\n", got.Stdout)
}

func TestSSH_PlannedDropsEndAndAreScopedToACommand(t *testing.T) {
	t.Parallel()

	c := up(hosttest.Working)
	c.Drops = 1
	assert.Equal(t, sshFailed, call(c, "pwd").ExitCode)
	assert.Equal(t, 0, call(c, "pwd").ExitCode)

	c.Drops = -1
	assert.Equal(t, sshFailed, call(c, "pwd").ExitCode)

	c.Drops, c.DropOn = 0, "pwd"
	assert.Equal(t, sshFailed, call(c, "pwd").ExitCode)
	assert.Equal(t, 0, call(c, "pwd").ExitCode)

	c.DropAll = "pwd"
	assert.Equal(t, sshFailed, call(c, "pwd").ExitCode)
	assert.Equal(t, sshFailed, call(c, "pwd").ExitCode)
	assert.Equal(t, 0, call(c, "touch", "x").ExitCode)
}

func TestSSH_AnswersTheNodeCommands(t *testing.T) {
	t.Parallel()

	c := up(hosttest.Working)
	assert.Equal(t, DF, call(c, "df", "-P", "-k", ".").Stdout)
	assert.Equal(t, "node1\nnode2\n", call(c, "sinfo", "-h", "-N", "-o", "%N").Stdout)
	assert.Equal(t, "idle\nmixed\n", call(c, "sinfo", "-h", "-N", "-o", "%T").Stdout)
	assert.Equal(t, "/home/lane\n", call(c, "pwd", "-P").Stdout)

	call(c, "scontrol", "update", "NodeName=node1", "State=DRAIN")
	assert.Equal(t, "drained\ndraining*\n", call(c, "sinfo", "-h", "-N", "-o", "%T").Stdout)
	call(c, "scontrol", "update", "NodeName=node1", "State=RESUME")
	assert.Equal(t, "idle\nmixed\n", call(c, "sinfo", "-h", "-N", "-o", "%T").Stdout)

	assert.Equal(t, 1, call(c, "mystery").ExitCode)
	assert.NotContains(t, call(up(hosttest.UnreadableDisk), "df").Stdout, "Filesystem 1024")
}

func TestSSH_TouchedFilesEndTheSlotsThatWaitForThem(t *testing.T) {
	t.Parallel()

	c := up(hosttest.Working)
	c.Slots = []string{"a", "b"}
	call(c, "touch", "a")

	assert.Equal(t, 1, c.WaitingSlots())
}

func TestSSH_JobsGoThroughTheirStates(t *testing.T) {
	t.Parallel()

	c := up(hosttest.Working)
	first := submit(c, hosttest.PassingCommand)
	assert.Equal(t, "RUNNING\n", call(c, "squeue", "-h", "-j", first, "-o", "%T").Stdout)
	assert.Equal(t, 1, call(c, "squeue", "-h", "-j", first, "-o", "%T").ExitCode, "the controller forgets the job")
	assert.Equal(t, "COMPLETED\n", call(c, "sacct", "-X", "-n", "-P", "-j", first).Stdout)
	assert.Equal(t, 1, c.Sbatches)

	failing := submit(c, hosttest.FailingCommand)
	assert.Equal(t, "FAILED\n", call(c, "sacct", "-X", "-n", "-P", "-j", failing).Stdout)

	unknown := submit(c, hosttest.UnknownCommand)
	assert.Equal(t, "MYSTERY\n", call(c, "squeue", "-h", "-j", unknown, "-o", "%T").Stdout)

	c.Jobs[first].Vanished = true
	assert.Equal(t, 1, call(c, "squeue", "-h", "-j", first, "-o", "%T").ExitCode)
	assert.Empty(t, call(c, "sacct", "-X", "-n", "-P", "-j", first).Stdout)
}

func TestSSH_ACanceledJobLingersThenEndsAsCanceled(t *testing.T) {
	t.Parallel()

	c := up(hosttest.Working)
	id := submit(c, hosttest.PassingCommand)
	c.Jobs[id].Lingers, c.Jobs[id].Blank = 1, 1

	require.Equal(t, 0, call(c, "scancel", id).ExitCode)
	assert.Equal(t, 1, c.Scancels)
	assert.Equal(t, "RUNNING\n", call(c, "squeue", "-h", "-j", id, "-o", "%T").Stdout)
	assert.Equal(t, 1, call(c, "squeue", "-h", "-j", id, "-o", "%T").ExitCode)
	assert.Empty(t, call(c, "sacct", "-X", "-n", "-P", "-j", id).Stdout, "the record comes late")
	assert.Contains(t, call(c, "sacct", "-X", "-n", "-P", "-j", id).Stdout, "CANCELLED") //nolint:misspell // Slurm's spelling

	c.NoStop = true
	assert.Equal(t, 1, call(c, "scancel", id).ExitCode)
}

func TestSSH_ACancelOfAJobThatWasNeverSubmittedFailsInsteadOfPanicking(t *testing.T) {
	t.Parallel()

	c := up(hosttest.Working)

	assert.Equal(t, 1, call(c, "scancel", "--me").ExitCode)
	assert.Zero(t, c.Scancels)
}

func TestSSH_AJobIdCanBeReusedAndALogGrowsByThePlan(t *testing.T) {
	t.Parallel()

	c := up(hosttest.Working)
	first := submit(c, "x")
	c.ReuseID = true
	assert.Equal(t, first, submit(c, "y"))

	c.Jobs[first].LogSizes = []int{3, 5}
	log := "slurm-" + first + ".out"
	assert.Equal(t, "3\n", call(c, "stat", "-c", "%s", log).Stdout)
	assert.Equal(t, "5\n", call(c, "stat", "-c", "%s", log).Stdout)
	assert.Equal(t, "5\n", call(c, "stat", "-c", "%s", log).Stdout, "the last size repeats")

	c.Jobs[first].LogSizes = nil
	assert.Equal(t, 1, call(c, "stat", "-c", "%s", log).ExitCode)
	assert.Equal(t, 1, call(c, "stat", "-c", "%s", "slurm-999.out").ExitCode)
}
