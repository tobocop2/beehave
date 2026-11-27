package slurmtest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/host/hosttest"
)

func TestCluster_ListsTheJobsOfANameAndMarksTheCanceledOnes(t *testing.T) {
	t.Parallel()

	c := up(hosttest.Working)
	call(c, "sbatch", "--parsable", "--job-name=gate-a", "--wrap=x")
	call(c, "sbatch", "--parsable", "--job-name=gate-b", "--wrap=x")

	assert.Equal(t, "42|RUNNING\n", call(c, "squeue", "-h", "--name=gate-a", "-o", "%i|%T").Stdout)
	assert.Equal(t, "\n", call(c, "squeue", "-h", "--name=none", "-o", "%i|%T").Stdout)

	c.Jobs["42"].Forgot = true
	assert.Equal(t, "\n", call(c, "squeue", "-h", "--name=gate-a", "-o", "%i|%T").Stdout, "the controller forgot the job")
	assert.Equal(t, "42|RUNNING\n", call(c, "sacct", "-X", "-n", "-P", "-S", "now-7days", "--name=gate-a", "-o", "JobID,State").Stdout)

	c.Jobs["42"].Canceled = true
	assert.Contains(t, call(c, "sacct", "--name=gate-a").Stdout, "42|CANCELLED") //nolint:misspell // Slurm's spelling

	c.Jobs["42"].Vanished = true
	assert.Equal(t, "\n", call(c, "sacct", "--name=gate-a").Stdout)

	c.BadLookup = "squeue"
	assert.Equal(t, 1, call(c, "squeue", "--name=gate-a").ExitCode)
}
