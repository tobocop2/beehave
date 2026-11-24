package slurmtest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/host/hosttest"
)

func TestCluster_AJobWithAFinalStateIsReportedByNameInAccountingOnly(t *testing.T) {
	t.Parallel()

	c := up(hosttest.Working)
	call(c, "sbatch", "--parsable", "--job-name=gate-a", "--wrap=x")

	c.Jobs["42"].Final = "FAILED"

	assert.Equal(t, "\n", call(c, "squeue", "-h", "--name=gate-a", "-o", "%i|%T").Stdout, "an ended job is not in the queue")
	assert.Equal(t, "42|FAILED\n", call(c, "sacct", "-X", "-n", "-P", "-S", "now-7days", "--name=gate-a", "-o", "JobID,State").Stdout)

	c.Jobs["42"].Canceled = true
	assert.Contains(t, call(c, "sacct", "--name=gate-a").Stdout, "42|CANCELLED", "a cancel wins over the final state") //nolint:misspell // Slurm's spelling
}
