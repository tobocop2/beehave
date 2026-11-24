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

func TestRunStep_TheBodyAndEachTryOfAHostCommandKeepTheStepContextWithTheirKey(t *testing.T) {
	t.Parallel()

	var (
		flow  dbos.Workflow[int, int]
		steps []bool
		keys  []string
	)

	rig := newRefusalRig(t, func(e *Engine) dbos.Workflow[int, int] {
		flow = func(ctx dbos.Context, _ int) (int, error) {
			return runStep(e, ctx, "keyed", func(c context.Context) (int, error) {
				_, bodyIsStep := c.(dbos.Context) // the readers of the run find it only in a DBOS context
				try := attemptKey(c, 2)
				_, tryIsStep := try.(dbos.Context) // the readers of the run find it only in a DBOS context
				steps = append(steps, bodyIsStep, tryIsStep)
				keys = append(keys, host.JobKey(c), host.JobKey(try))

				return 1, nil
			})
		}

		return flow
	})

	require.NoError(t, rig.run(t, flow, "typed-run"))

	assert.Equal(t, []bool{true, true}, steps, "the body of a step and the try of a host command still hold the step context")
	assert.Equal(t, []string{"typed-run:0", "typed-run:0:2"}, keys)
}

func TestStepKey_TheContextOfTheEngineOutsideARunHasNoKey(t *testing.T) {
	t.Parallel()

	rig := newRefusalRig(t, func(*Engine) dbos.Workflow[int, int] {
		return func(dbos.Context, int) (int, error) { return 0, nil }
	})

	assert.Empty(t, stepKey(rig.ctx), "a DBOS context with no run gives no key, so the host starts a new job and matches no other")
}

func TestStepKey_ACallOutsideARunHasNoKey(t *testing.T) {
	t.Parallel()

	assert.Empty(t, stepKey(t.Context()), "a plain context has no run and no step")
	assert.Empty(t, host.JobKey(t.Context()))
}
