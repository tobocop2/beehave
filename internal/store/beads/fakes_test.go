package beads_test

import (
	"context"
	"slices"
	"testing"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc"
	"github.com/tobocop2/beehest/internal/store/beads/beadstest"
)

const (
	progress = beadstest.Progress
	nowUnix  = beadstest.Now
)

type fields = beadstest.Fields

func newFakeBd(t *testing.T) *beadstest.Bd {
	t.Helper()

	return beadstest.NewBd(t)
}

// failingHeartbeats fails the heartbeat of the named rows and passes every other command to inner.
type failingHeartbeats struct {
	inner proc.Runner
	rows  []string
}

func (f failingHeartbeats) Run(ctx context.Context, args []string) model.CommandResult {
	if args[0] == "heartbeat" && slices.Contains(f.rows, args[1]) {
		return model.CommandResult{ExitCode: 1, Stderr: "boom"}
	}

	return f.inner.Run(ctx, args)
}

// brokenRunner fails every command.
type brokenRunner struct{}

func (brokenRunner) Run(context.Context, []string) model.CommandResult {
	return model.CommandResult{ExitCode: 1, Stderr: "boom"}
}

// readyList serves the ready list of the item that the tracker conformance suite reads, and forwards every other command to inner.
// The item carries the label bug. A list with another label, the held list included, is empty, and no item has a parent or an epic.
type readyList struct {
	inner proc.Runner
}

const readyItem = `[{"id":"5","title":"A finding","priority":2,"issue_type":"task","labels":["bug"]}]`

func (r readyList) Run(ctx context.Context, args []string) model.CommandResult {
	switch {
	case args[0] == "ready" && slices.Contains(args, "--label") && !slices.Contains(args, "bug"):
		return model.CommandResult{Stdout: "[]"}
	case args[0] == "ready":
		return model.CommandResult{Stdout: readyItem}
	case args[0] == "children" || (args[0] == "list" && slices.Contains(args, "epic")):
		return model.CommandResult{Stdout: "[]"}
	}

	return r.inner.Run(ctx, args)
}
