// Package pool picks the runner of an agent that has room under the usage caps. It reads no usage itself: the caller gives a Reader.
package pool

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	resetLayout         = "2006-01-02T15:04:05-07:00"
	resetLayoutFraction = "2006-01-02T15:04:05.000000-07:00"
)

// formatReset writes a reset time with its offset spelled out, as +00:00 for UTC. The seconds carry six digits of fraction, or none when it is zero.
func formatReset(when time.Time) string {
	if when.Nanosecond()/int(time.Microsecond) == 0 {
		return when.Format(resetLayout)
	}

	return when.Format(resetLayoutFraction)
}

// ErrNoRunner is returned when the config has no runner of the agent.
var ErrNoRunner = errors.New("no runner of that agent")

// NoneQualifiesError is returned when runners exist and none has room under both caps. The message says why and when to retry.
// Reset is the earliest time a runner gets room, or the zero time when the pool knows none.
type NoneQualifiesError struct {
	Message string
	Reset   time.Time
}

func (e *NoneQualifiesError) Error() string { return e.Message }

// ResetAt returns the earliest time a runner gets room, and false when the pool knows no reset time.
func (e *NoneQualifiesError) ResetAt() (time.Time, bool) { return e.Reset, !e.Reset.IsZero() }

// Reader reads the usage of one runner. A failure is a reading with a reason, never a panic.
type Reader func(ctx context.Context, runner model.Runner) model.Reading

// OverCap returns the windows at or over their cap.
func OverCap(usage model.Usage, caps config.Pool) []model.Window {
	var over []model.Window

	if usage.FiveHour.Percent >= caps.FiveHourMax {
		over = append(over, usage.FiveHour)
	}

	if usage.SevenDay.Percent >= caps.SevenDayMax {
		over = append(over, usage.SevenDay)
	}

	return over
}

// Choose returns the runner with room: the lowest seven-day use, then the lowest five-hour use, then the first in config order.
// A runner with unknown usage is never chosen. A runner that busy names is chosen only when no other runner has room.
func Choose(runners []model.Runner, readings map[string]model.Reading, caps config.Pool, busy ...string) (model.Runner, bool) {
	free := make([]model.Runner, 0, len(runners))

	for _, runner := range runners {
		if !slices.Contains(busy, runner.Name) {
			free = append(free, runner)
		}
	}

	if chosen, found := lowest(free, readings, caps); found {
		return chosen, true
	}

	return lowest(runners, readings, caps)
}

// lowest returns the runner under both caps with the lowest use.
func lowest(runners []model.Runner, readings map[string]model.Reading, caps config.Pool) (model.Runner, bool) {
	var (
		chosen model.Runner
		found  bool
		best   model.Usage
	)

	for _, runner := range runners {
		usage, known := readings[runner.Name].Usage()
		if !known || len(OverCap(usage, caps)) > 0 {
			continue
		}

		if !found || usage.SevenDay.Percent < best.SevenDay.Percent ||
			(usage.SevenDay.Percent == best.SevenDay.Percent && usage.FiveHour.Percent < best.FiveHour.Percent) {
			chosen, best, found = runner, usage, true
		}
	}

	return chosen, found
}

// EarliestReset returns the first time a runner over a cap gets room: when its last over-cap window resets.
// A runner with an over-cap window that has no reset time is skipped.
func EarliestReset(readings map[string]model.Reading, caps config.Pool) (time.Time, bool) {
	var earliest time.Time

	for _, reading := range readings {
		usage, known := reading.Usage()
		if !known {
			continue
		}

		free, ok := freeAt(OverCap(usage, caps))
		if ok && (earliest.IsZero() || free.Before(earliest)) {
			earliest = free
		}
	}

	return earliest, !earliest.IsZero()
}

// freeAt returns the latest reset of the windows, and false when there is no window or one has no reset time.
func freeAt(windows []model.Window) (time.Time, bool) {
	var latest time.Time

	for _, window := range windows {
		if window.ResetsAt.IsZero() {
			return time.Time{}, false
		}

		if window.ResetsAt.After(latest) {
			latest = window.ResetsAt
		}
	}

	return latest, len(windows) > 0
}

// Describe returns the usage of one runner as text: both windows with their reset times, or the reason.
func Describe(reading model.Reading) string {
	usage, known := reading.Usage()
	if !known {
		return "unknown: " + reading.Reason()
	}

	return describeWindow("five-hour", usage.FiveHour) + ", " + describeWindow("seven-day", usage.SevenDay)
}

func describeWindow(label string, window model.Window) string {
	reset := "no reset time"
	if !window.ResetsAt.IsZero() {
		reset = formatReset(window.ResetsAt)
	}

	return fmt.Sprintf("%s %g%% (resets %s)", label, window.Percent, reset)
}

// ReadAll returns one reading for each runner, by name. The runners are read at the same time, so the whole read takes one timeout, not one for each runner.
// A reader that panics gives an unknown reading and leaves the others.
func ReadAll(ctx context.Context, runners []model.Runner, read Reader) map[string]model.Reading {
	results := make([]model.Reading, len(runners))

	var group sync.WaitGroup

	for index, runner := range runners {
		group.Go(func() { results[index] = guarded(ctx, runner, read) })
	}

	group.Wait()

	readings := make(map[string]model.Reading, len(runners))
	for index, runner := range runners {
		readings[runner.Name] = results[index]
	}

	return readings
}

// guarded returns the reading of read, or an unknown reading when read panics.
func guarded(ctx context.Context, runner model.Runner, read Reader) (reading model.Reading) {
	defer func() {
		if failure := recover(); failure != nil {
			reading = model.Unknown(fmt.Sprintf("the read failed: %v", failure))
		}
	}()

	return read(ctx, runner)
}

// Refusal says why no runner qualifies, with the earliest reset time when one is known. The runners give the order of the unknown readings.
func Refusal(runners []model.Runner, readings map[string]model.Reading, caps config.Pool, agent model.Agent) string {
	when := " No reset time is known."
	reset, resetKnown := EarliestReset(readings, caps)

	if resetKnown {
		when = " The earliest reset is " + formatReset(reset) + "."
	}

	unknown, needsLogin := unknownUsage(runners, readings)

	// A wait helps only when a reset is known or no runner lacks a login. A runner with no login stays unusable until someone logs in.
	wait := " Wait for it."
	if needsLogin && !resetKnown {
		when, wait = "", ""
	}

	return fmt.Sprintf("no %s runner is under both caps (five-hour %g%%, seven-day %g%%).%s%s%s",
		agent, caps.FiveHourMax, caps.SevenDayMax, when, unknown, wait)
}

// unknownUsage returns one sentence for each runner with unknown usage, and whether one of them needs a login.
func unknownUsage(runners []model.Runner, readings map[string]model.Reading) (string, bool) {
	var (
		text       strings.Builder
		needsLogin bool
	)

	for _, runner := range runners {
		reading := readings[runner.Name]
		if _, known := reading.Usage(); !known {
			fmt.Fprintf(&text, " Unknown usage: %s: %s.", runner.Name, reading.Reason())

			needsLogin = needsLogin || reading.NeedsLogin()
		}
	}

	return text.String(), needsLogin
}

// Select returns the runner of the agent to use. It returns ErrNoRunner when the config has none of that agent.
// When runners exist and none has room, it returns a NoneQualifiesError: the run stops and never moves to another agent.
// busy names the runners that have work now. Select prefers a runner that is not in it, as Choose does.
func Select(ctx context.Context, runners []model.Runner, caps config.Pool, agent model.Agent, read Reader, busy ...string) (model.Runner, error) {
	mine := ofAgent(runners, agent)
	if len(mine) == 0 {
		return model.Runner{}, ErrNoRunner
	}

	readings := ReadAll(ctx, mine, read)

	picked, ok := Choose(mine, readings, caps, busy...)
	if !ok {
		return model.Runner{}, none(mine, readings, caps, agent)
	}

	return picked, nil
}

// SelectOverCap returns the runner of the agent with the lowest use, whatever the caps say. It is the pick of a run that the owner lets go over a cap.
// A runner with unknown usage is never picked, so it returns a NoneQualifiesError when no runner of the agent has a known usage.
func SelectOverCap(ctx context.Context, runners []model.Runner, caps config.Pool, agent model.Agent, read Reader, busy ...string) (model.Runner, error) {
	mine := ofAgent(runners, agent)
	if len(mine) == 0 {
		return model.Runner{}, ErrNoRunner
	}

	readings := ReadAll(ctx, mine, read)

	picked, ok := Choose(mine, readings, config.Pool{FiveHourMax: math.Inf(1), SevenDayMax: math.Inf(1)}, busy...)
	if !ok {
		return model.Runner{}, none(mine, readings, caps, agent)
	}

	return picked, nil
}

// none builds the error of a pick that finds no runner with room.
func none(runners []model.Runner, readings map[string]model.Reading, caps config.Pool, agent model.Agent) *NoneQualifiesError {
	reset, _ := EarliestReset(readings, caps)

	return &NoneQualifiesError{Message: Refusal(runners, readings, caps, agent), Reset: reset}
}

// Command returns one line for each runner of the agent, then the pick. When none qualifies, the last line is "pick: none" and the error is a NoneQualifiesError.
func Command(ctx context.Context, runners []model.Runner, caps config.Pool, agent model.Agent, read Reader) ([]string, error) {
	mine := ofAgent(runners, agent)
	readings := ReadAll(ctx, mine, read)

	lines := make([]string, 0, len(mine)+1)
	for _, runner := range mine {
		lines = append(lines, runner.Name+": "+Describe(readings[runner.Name]))
	}

	picked, ok := Choose(mine, readings, caps)
	if !ok {
		return append(lines, "pick: none"), none(mine, readings, caps, agent)
	}

	return append(lines, "pick: "+picked.Name), nil
}

func ofAgent(runners []model.Runner, agent model.Agent) []model.Runner {
	var mine []model.Runner

	for _, runner := range runners {
		if runner.Agent == agent {
			mine = append(mine, runner)
		}
	}

	return mine
}
