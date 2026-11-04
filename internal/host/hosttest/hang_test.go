package hosttest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

func TestHangingGit_ListsOneRepositoryHangsOnRefsAndPassesTheRestOn(t *testing.T) {
	t.Parallel()

	inner := &proctest.Scripted{}
	inner.Plan("home\n", "pwd")

	runner := hosttest.HangingGit(inner, "/work/repo")

	found := runner.Run(t.Context(), []string{"-o", "BatchMode=yes", "host", "find", "-H", "/work"})
	assert.Equal(t, "/work/repo/.git\x00", found.Stdout)

	assert.Equal(t, "home\n", runner.Run(t.Context(), []string{"host", "pwd"}).Stdout, "another command goes to the inner runner")

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()

	refs := runner.Run(ctx, []string{"host", "git", "-C", "/work/repo", "for-each-ref"})

	require.Equal(t, model.CommandResult{ExitCode: -1, Stderr: context.DeadlineExceeded.Error()}, refs, "the ref listing waits for the context")
}
