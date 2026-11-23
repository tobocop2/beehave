package flow

import (
	"context"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
)

func TestRunStep_AStepThatRunsAgainGetsTheJobKeyOfItsFirstRunAndEachStepGetsItsOwn(t *testing.T) {
	t.Parallel()

	var (
		flow dbos.Workflow[int, int]
		keys []string
		runs int
	)

	rig := newRefusalRig(t, func(e *Engine) dbos.Workflow[int, int] {
		flow = func(ctx dbos.Context, _ int) (int, error) {
			_, err := runStep(e, ctx, "first", func(c context.Context) (int, error) {
				keys = append(keys, host.JobKey(c))
				runs++

				if runs == 1 {
					return 0, errStepFails
				}

				return 1, nil
			}, dbos.WithStepMaxRetries(1), dbos.WithStepBaseInterval(time.Millisecond), dbos.WithStepMaxInterval(time.Millisecond))
			if err != nil {
				return 0, err
			}

			return runStep(e, ctx, "after", func(c context.Context) (int, error) {
				keys = append(keys, host.JobKey(c))

				return 1, nil
			})
		}

		return flow
	})

	require.NoError(t, rig.run(t, flow, "keyed-run"))

	assert.Equal(t, []string{"keyed-run:0", "keyed-run:0", "keyed-run:1"}, keys, "a replay of a step keeps its key, and the next step has another")
}

func TestStepKey_ACallOutsideARunHasNoKey(t *testing.T) {
	t.Parallel()

	assert.Empty(t, stepKey(t.Context()), "a plain context has no run and no step")
	assert.Empty(t, host.JobKey(t.Context()))
}
