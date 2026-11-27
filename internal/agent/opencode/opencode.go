package opencode

import (
	"context"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/model"
)

// Name is the name the OpenCode agent is chosen by.
const Name model.Agent = "opencode"

// ReadUsage returns an unknown reading, because OpenCode has no usage report. The pool never picks a runner whose usage is unknown.
func (Adapter) ReadUsage(context.Context, model.Runner) model.Reading {
	return agent.NoUsage(Name)
}
