package hosttest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

func TestSSHConfig_AnswersSshGAndCountsItAndPassesTheRestOn(t *testing.T) {
	t.Parallel()

	next := proctest.Always(model.CommandResult{Stdout: "from the host\n"})
	runner := &hosttest.SSHConfig{Answer: hosttest.CleanSSHConfig, Next: next}

	assert.Equal(t, hosttest.CleanSSHConfig, runner.Run(t.Context(), []string{"-G", "box"}).Stdout)
	assert.Equal(t, "from the host\n", runner.Run(t.Context(), []string{"box", "true"}).Stdout)
	assert.Equal(t, 1, runner.Asked)
	assert.Equal(t, [][]string{{"box", "true"}}, next.Calls, "only the call that is not ssh -G reaches the host")
}
