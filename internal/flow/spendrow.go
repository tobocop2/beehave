package flow

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/ledger"
)

// startSpend writes the spend row of an agent execution that starts now, with an unknown cost, and returns the id of the execution.
// The id is random, so the run id and the step number of a rewind or of a reused run give no row its identity. A failed write stops the phase before the agent starts.
func (e *Engine) startSpend(ctx context.Context, in PhaseInput) (string, error) {
	number := 0

	if step, isStep := ctx.(dbos.Context); isStep {
		id, err := dbos.GetStepID(step)
		if err != nil {
			return "", fmt.Errorf("read the step number of the agent step %s of item %s: %w", in.Call, in.Item, err)
		}

		number = id
	}

	name := in.Call
	if in.Rerun {
		name += ledger.RerunMark
	}

	call := ledger.Call{ID: rand.Text(), Tree: in.Tree, Run: in.Run, Step: number, Item: in.Item, Name: name, At: e.deps.Clock.wall()}
	if err := e.ledger.StartCall(ctx, call); err != nil {
		return "", fmt.Errorf("the agent step %s of item %s does not start: %w", in.Call, in.Item, err)
	}

	return call.ID, nil
}

// endSpend sets the cost and the session of the execution when the agent exits. A cost that is unknown stays NULL. A failed write is logged and the row keeps its unknown cost,
// so the budget checks refuse the next step until the owner repairs the row. The write does not end with the step: a stopped step still spent its money.
func (e *Engine) endSpend(ctx context.Context, execution string, in PhaseInput, out PhaseOutput) {
	cost := sql.NullFloat64{Float64: out.Cost, Valid: !out.Unknown}

	if err := e.ledger.EndCall(context.WithoutCancel(ctx), execution, out.Session, cost); err != nil {
		e.logger.WarnContext(ctx, "cannot set the cost of the agent call; the ledger holds it as unknown", "item", in.Item, "step", in.Call, "error", err)
	}
}

// ledgerSpend turns a sum of the ledger into the spend of a cap check.
func ledgerSpend(sum ledger.Sum) agent.Spend {
	return agent.Spend{Cost: sum.Dollars, Unknown: sum.Unknown}
}
