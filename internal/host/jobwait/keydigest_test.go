package jobwait_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/host/jobwait"
)

func TestKeyDigest_IsTheSameForTheSameKeyAndDiffersForAnotherKey(t *testing.T) {
	t.Parallel()

	first := jobwait.KeyDigest("run-1:4")

	assert.Equal(t, first, jobwait.KeyDigest("run-1:4"))
	assert.NotEqual(t, first, jobwait.KeyDigest("run-1:5"))
	assert.Regexp(t, `^[0-9a-f]{32}$`, first, "hex digits only, so a unit name and a Slurm handle accept it")
}
