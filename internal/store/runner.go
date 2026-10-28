// Package store reads and writes work items through the bd and gh command lines.
package store

import (
	"context"

	"github.com/tobocop2/obee/internal/model"
)

// Runner runs one outside command with the arguments and returns what it printed.
type Runner interface {
	Run(ctx context.Context, args []string) model.CommandResult
}
