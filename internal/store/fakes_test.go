package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/storetest"
)

const (
	progress = storetest.Progress
	nowUnix  = storetest.Now
)

type fields = storetest.Fields

func newFakeBd(t *testing.T) *storetest.Bd {
	t.Helper()

	return storetest.NewBd(t)
}

// failingHeartbeats fails the heartbeat of the named rows and passes every other command to inner.
type failingHeartbeats struct {
	inner store.Runner
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
