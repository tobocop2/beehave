package pool_test

import (
	"context"
	"strings"
	"testing"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/pool"
)

// threeWindows is a reading of an agent with the windows hourly, daily and monthly, from the shortest to the longest.
func threeWindows(hourly, daily, monthly float64) model.Reading {
	return model.Known(model.Usage{Windows: []model.Window{
		{Name: "hourly", Percent: hourly},
		{Name: "daily", Percent: daily},
		{Name: "monthly", Percent: monthly},
	}})
}

func TestWindows_TheRefusalListsTheCapsOfTheAgentInSortedNameOrder(t *testing.T) {
	t.Parallel()

	runners := []model.Runner{{Name: "x", Agent: "other"}}
	readings := map[string]model.Reading{"x": threeWindows(99, 99, 99)}
	text := pool.Refusal(runners, readings, config.Pool{"monthly": 50, "hourly": 20.5, "daily": 30}, "other")

	if !strings.HasPrefix(text, "no other runner is under both caps (daily 30%, hourly 20.5%, monthly 50%).") {
		t.Fatalf("refusal = %q", text)
	}
}

func TestWindows_TheRefusalOfAnAgentWithNoCapsNamesTheAgentAndSaysSo(t *testing.T) {
	t.Parallel()

	runners := []model.Runner{{Name: "x", Agent: "bare"}}
	readings := map[string]model.Reading{"x": model.Unknown("down")}
	text := pool.Refusal(runners, readings, config.Pool{}, "bare")

	want := "no bare runner can be picked: the agent has no usage caps. No reset time is known. Unknown usage: x: down. Wait for it."
	if text != want {
		t.Fatalf("refusal = %q, want %q", text, want)
	}
}

func TestWindows_ACapOnAWindowTheReadingLacksDoesNotApplyToTheAgent(t *testing.T) {
	t.Parallel()

	runners := []model.Runner{{Name: "x", Agent: "claude"}}
	read := func(context.Context, model.Runner) model.Reading { return use(99, 99) }

	picked, err := pool.Select(context.Background(), runners, config.Pool{"monthly": 1}, "claude", read)
	if err != nil || picked.Name != "x" {
		t.Fatalf("a cap on monthly must not apply to a reading of five_hour and seven_day: got %v, %v", picked, err)
	}

	if !pool.HasRoom(map[string]model.Reading{"x": use(99, 99)}, config.Pool{"monthly": 1}) {
		t.Fatal("a cap on a window the reading lacks leaves room")
	}
}

func TestWindows_DescribeWritesEachWindowWithItsNameAndADashForAnUnderscore(t *testing.T) {
	t.Parallel()

	reading := model.Known(model.Usage{Windows: []model.Window{{Name: "one_hour", Percent: 1}, {Name: "day", Percent: 2, ResetsAt: t1}}})

	want := "one-hour 1% (resets no reset time), day 2% (resets 2026-10-24T05:00:00+00:00)"
	if got := pool.Describe(reading); got != want {
		t.Fatalf("Describe = %q, want %q", got, want)
	}
}

func TestWindows_TheWindowsAreComparedFromTheLastToTheFirst(t *testing.T) {
	t.Parallel()

	runners := []model.Runner{{Name: "x", Agent: "other"}, {Name: "y", Agent: "other"}, {Name: "z", Agent: "other"}}
	data := map[string]model.Reading{
		"x": threeWindows(1, 50, 10),
		"y": threeWindows(9, 20, 10),
		"z": threeWindows(0, 20, 11),
	}
	read := func(_ context.Context, r model.Runner) model.Reading { return data[r.Name] }

	picked, err := pool.Select(context.Background(), runners, config.Pool{}, "other", read)
	if err != nil || picked.Name != "y" {
		t.Fatalf("monthly ties between x and y, then daily decides for y; z is last on monthly: got %v, %v", picked, err)
	}
}

func TestWindows_AnOverCapRunnerOfAnAgentWithOtherWindowsIsPickedWhenTheCapsAreLifted(t *testing.T) {
	t.Parallel()

	runners := []model.Runner{{Name: "x", Agent: "other"}}
	read := func(context.Context, model.Runner) model.Reading { return threeWindows(99, 99, 99) }

	if _, err := pool.Select(context.Background(), runners, config.Pool{"daily": 50}, "other", read); err == nil {
		t.Fatal("a cap on daily at 50 leaves no runner at 99")
	}

	picked, err := pool.SelectOverCap(context.Background(), runners, config.Pool{"daily": 50}, "other", read)
	if err != nil || picked.Name != "x" {
		t.Fatalf("SelectOverCap ignores the caps: got %v, %v", picked, err)
	}
}
