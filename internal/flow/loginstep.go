package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	// loginStepName is the builtin name of the step that picks the login of a run. Its records are `login#<n>`.
	loginStepName = "login"
	// capWaitFloor is the least time of the sleep that waits for a usage cap: a reset time in the past still waits this long before the next read.
	capWaitFloor = time.Minute
)

// loginChoice is the record of one pick of a login. A pick that found a runner holds its Login.
// OverCap is true when the runner was over a usage cap and the owner allowed that. A pick that found none holds the Reset time to wait for.
type loginChoice struct {
	Login   string    `json:"login,omitempty"`
	OverCap bool      `json:"over_cap,omitempty"`
	Reset   time.Time `json:"reset,omitempty"`
}

// loginFor returns the login of an agent call. A call that names a runner never reads the pool.
// A run that has no login yet picks one at its first agent call. When no runner has room under the usage caps, the run sleeps until the earliest
// reset the pool knows and picks again. When the pool knows no reset time, the call fails with the refusal of the pool.
// Each pick is a recorded step, so a resumed run replays the same picks and the same sleeps, and keeps the login that owns its session.
func (s *script) loginFor(a agentArgs) (string, error) {
	if a.runner != "" {
		return a.runner, nil
	}

	for s.login == "" {
		choice, err := countedStep(s, loginStepName, "", s.pickOnce)
		if err != nil {
			return "", err
		}

		if s.login = choice.Login; s.login == "" {
			err = s.waitForReset(choice.Reset)
		}

		if err != nil {
			return "", err
		}
	}

	return s.login, nil
}

// pickOnce reads the pool once. A refusal with a known reset time is a result to wait on, not an error.
func (s *script) pickOnce(ctx context.Context) (loginChoice, error) {
	login, err := s.e.pickLogin(ctx, s.in, false)

	if refused(err) && s.e.overCap {
		login, err = s.e.pickLogin(ctx, s.in, true)

		return choiceOf(login, true, err)
	}

	return choiceOf(login, false, err)
}

// refused reports whether err is the refusal of a pool with no runner under the usage caps.
func refused(err error) bool {
	var refusal resetter

	return errors.As(err, &refusal)
}

// choiceOf turns the answer of a pick into its record. A refusal with a known reset time is a result to wait on; any other error stays an error.
func choiceOf(login string, overCap bool, err error) (loginChoice, error) {
	if err == nil {
		return loginChoice{Login: login, OverCap: overCap}, nil
	}

	var refusal resetter

	if errors.As(err, &refusal) {
		if reset, known := refusal.ResetAt(); known {
			return loginChoice{Reset: reset}, nil
		}
	}

	return loginChoice{}, err
}

// waitForReset is the durable sleep until the reset time. A sleep survives a restart of the process, and a resume sleeps only the time that is left.
func (s *script) waitForReset(reset time.Time) error {
	clock := s.e.deps.Clock

	wait := reset.Sub(clock.wall())
	if wait <= 0 {
		wait = cmp.Or(clock.CapWait, capWaitFloor)
	}

	if _, err := dbos.Sleep(s.ctx, wait); err != nil {
		return fmt.Errorf("wait for a usage cap of item %s to reset: %w", s.in.Item, err)
	}

	return nil
}
