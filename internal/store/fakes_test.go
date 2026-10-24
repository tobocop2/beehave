package store_test

import (
	"context"
	"testing"

	"github.com/tobocop2/obee/internal/model"
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

// brokenRunner fails every command.
type brokenRunner struct{}

func (brokenRunner) Run(context.Context, []string) model.CommandResult {
	return model.CommandResult{ExitCode: 1, Stderr: "boom"}
}
