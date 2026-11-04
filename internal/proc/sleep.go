package proc

import (
	"context"
	"errors"
	"time"
)

// Sleep waits for d. When ctx ends first, it returns the error of ctx joined with its cause.
func Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return errors.Join(ctx.Err(), context.Cause(ctx))
	case <-timer.C:
		return nil
	}
}
