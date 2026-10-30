package pool_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/pool"
)

var (
	caps = config.Pool{FiveHourMax: 90, SevenDayMax: 95}
	t1   = time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC)
	t2   = time.Date(2026, 10, 25, 7, 0, 0, 0, time.UTC)
)

func use(five, seven float64) model.Reading {
	return model.Known(model.Usage{
		FiveHour: model.Window{Percent: five, ResetsAt: t1},
		SevenDay: model.Window{Percent: seven, ResetsAt: t2},
	})
}

func useReset(five, seven float64, fiveReset, sevenReset time.Time) model.Reading {
	return model.Known(model.Usage{
		FiveHour: model.Window{Percent: five, ResetsAt: fiveReset},
		SevenDay: model.Window{Percent: seven, ResetsAt: sevenReset},
	})
}

func runners(names ...string) []model.Runner {
	list := make([]model.Runner, 0, len(names))
	for _, name := range names {
		list = append(list, model.Runner{Name: name, Agent: model.AgentClaude})
	}

	return list
}

func pick(list []model.Runner, readings map[string]model.Reading) string {
	chosen, ok := pool.Choose(list, readings, caps)
	if !ok {
		return ""
	}

	return chosen.Name
}

func readerOf(readings map[string]model.Reading) pool.Reader {
	return func(_ context.Context, runner model.Runner) model.Reading { return readings[runner.Name] }
}

func TestChoose_ARunnerMustBeUnderBothCaps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		five, seven float64
		ok          bool
	}{
		{"under both", 89.9, 94.9, true},
		{"on the five-hour cap", 90, 10, false},
		{"on the seven-day cap", 10, 95, false},
		{"over both", 95, 99, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := pick(runners("a"), map[string]model.Reading{"a": use(tc.five, tc.seven)})
			if (got == "a") != tc.ok {
				t.Fatalf("pick = %q, want ok=%v", got, tc.ok)
			}
		})
	}
}

func TestChoose_TheLowestSevenDayWinsThenFiveHourThenConfigOrder(t *testing.T) {
	t.Parallel()

	list := runners("a", "b", "c", "d")
	tail := map[string]model.Reading{"c": use(1, 60), "d": use(1, 60)}

	cases := []struct {
		name string
		a, b model.Reading
		want string
	}{
		{"lowest seven-day", use(5, 50), use(80, 20), "b"},
		{"then lowest five-hour", use(30, 20), use(10, 20), "b"},
		{"then config order", use(10, 20), use(10, 20), "a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			readings := map[string]model.Reading{"a": tc.a, "b": tc.b, "c": tail["c"], "d": tail["d"]}
			if got := pick(list, readings); got != tc.want {
				t.Fatalf("pick = %q, want %q", got, tc.want)
			}
		})
	}

	if got := pick(list[2:], tail); got != "c" {
		t.Fatalf("a tie goes to the first runner: got %q", got)
	}
}

func TestChoose_TheSevenDayUseOutranksTheFiveHourUse(t *testing.T) {
	t.Parallel()

	got := pick(runners("a", "b"), map[string]model.Reading{"a": use(1, 30), "b": use(80, 10)})
	if got != "b" {
		t.Fatalf("pick = %q", got)
	}
}

func TestChoose_ARunnerWithUnknownUsageIsNeverPicked(t *testing.T) {
	t.Parallel()

	list := runners("a", "b")

	if got := pick(list, map[string]model.Reading{"a": model.Unknown("boom"), "b": use(80, 90)}); got != "b" {
		t.Fatalf("pick = %q", got)
	}

	if got := pick(list, map[string]model.Reading{"a": model.Unknown("boom"), "b": model.Unknown("boom")}); got != "" {
		t.Fatalf("pick = %q, want none", got)
	}
}

func TestOverCap_NamesTheWindowsAtOrOverTheirCap(t *testing.T) {
	t.Parallel()

	usage, _ := use(90, 10).Usage()
	if over := pool.OverCap(usage, caps); len(over) != 1 || over[0].Percent != 90 {
		t.Fatalf("over = %v", over)
	}

	usage, _ = use(95, 99).Usage()
	if over := pool.OverCap(usage, caps); len(over) != 2 {
		t.Fatalf("over = %v", over)
	}
}

func TestEarliestReset_IsWhenTheFirstOverCapRunnerGetsRoom(t *testing.T) {
	t.Parallel()

	later := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	readings := map[string]model.Reading{
		"a": useReset(95, 10, t2, t2),
		"b": useReset(95, 99, t1, later), // free only when both windows reset
		"c": model.Unknown("x"),
		"d": use(1, 1),
	}

	got, ok := pool.EarliestReset(readings, caps)
	if !ok || !got.Equal(t2) {
		t.Fatalf("EarliestReset = %v, %v, want %v", got, ok, t2)
	}

	if _, ok := pool.EarliestReset(map[string]model.Reading{"c": model.Unknown("x"), "d": use(1, 1)}, caps); ok {
		t.Fatal("no runner is over a cap, so no reset time")
	}

	if _, ok := pool.EarliestReset(map[string]model.Reading{"a": useReset(95, 10, time.Time{}, t2)}, caps); ok {
		t.Fatal("an over-cap window with no reset time gives no reset time")
	}
}

func TestDescribe_ShowsPercentAndResetOrTheReason(t *testing.T) {
	t.Parallel()

	want := "five-hour 12.5% (resets 2026-10-24T05:00:00+00:00), seven-day 40% (resets 2026-10-25T07:00:00+00:00)"
	if got := pool.Describe(use(12.5, 40)); got != want {
		t.Fatalf("Describe = %q", got)
	}

	if got := pool.Describe(useReset(1, 2, time.Time{}, t2)); !strings.Contains(got, "five-hour 1% (resets no reset time)") {
		t.Fatalf("Describe = %q", got)
	}

	if got := pool.Describe(model.Unknown("boom")); got != "unknown: boom" {
		t.Fatalf("Describe = %q", got)
	}
}

func TestResetTimes_SixDigitsOfFractionOrNoFractionAtAll(t *testing.T) {
	t.Parallel()

	half := t1.Add(500 * time.Millisecond)
	micro := t1.Add(120 * time.Microsecond)

	if got := pool.Describe(useReset(1, 2, half, micro)); !strings.Contains(got, "(resets 2026-10-24T05:00:00.500000+00:00)") ||
		!strings.Contains(got, "(resets 2026-10-24T05:00:00.000120+00:00)") {
		t.Fatalf("Describe = %q", got)
	}

	readings := map[string]model.Reading{"a": useReset(95, 10, half, t2)}

	want := "no claude runner is under both caps (five-hour 90%, seven-day 95%). " +
		"The earliest reset is 2026-10-24T05:00:00.500000+00:00. Wait for it."
	if got := pool.Refusal(runners("a"), readings, caps, model.AgentClaude); got != want {
		t.Fatalf("Refusal = %q", got)
	}
}

func TestRefusal_ListsEachUnknownRunnerWithItsReason(t *testing.T) {
	t.Parallel()

	readings := map[string]model.Reading{"a": model.Unknown("login expired"), "b": model.Unknown("no answer")}

	text := pool.Refusal(runners("a", "b"), readings, caps, model.AgentClaude)
	if !strings.Contains(text, "Unknown usage: a: login expired. Unknown usage: b: no answer.") {
		t.Fatalf("Refusal = %q", text)
	}
}

func TestRefusal_ARunnerThatNeedsALoginGetsNoAdviceToWait(t *testing.T) {
	t.Parallel()

	readings := map[string]model.Reading{"a": model.UnknownLogin("runner a has no Claude plan limits")}

	want := "no claude runner is under both caps (five-hour 90%, seven-day 95%). Unknown usage: a: runner a has no Claude plan limits."
	if got := pool.Refusal(runners("a"), readings, caps, model.AgentClaude); got != want {
		t.Fatalf("Refusal = %q", got)
	}
}

func TestRefusal_ALoginProblemAndAnOverCapRunnerStillSayToWaitForTheReset(t *testing.T) {
	t.Parallel()

	readings := map[string]model.Reading{"a": model.UnknownLogin("log in"), "b": useReset(95, 10, t1, t1)}

	want := "no claude runner is under both caps (five-hour 90%, seven-day 95%). " +
		"The earliest reset is 2026-10-24T05:00:00+00:00. Unknown usage: a: log in. Wait for it."
	if got := pool.Refusal(runners("a", "b"), readings, caps, model.AgentClaude); got != want {
		t.Fatalf("Refusal = %q", got)
	}
}

func TestRefusal_SaysSoWhenNoResetTimeIsKnown(t *testing.T) {
	t.Parallel()

	text := pool.Refusal(runners("a"), map[string]model.Reading{"a": model.Unknown("x")}, caps, model.AgentClaude)
	if !strings.Contains(text, "No reset time is known.") {
		t.Fatalf("Refusal = %q", text)
	}
}

func TestRefusal_NamesTheCapsAndTheEarliestReset(t *testing.T) {
	t.Parallel()

	readings := map[string]model.Reading{"a": useReset(95, 10, t2, t2), "b": useReset(10, 99, t1, t1)}

	want := "no claude runner is under both caps (five-hour 90%, seven-day 95%). " +
		"The earliest reset is 2026-10-24T05:00:00+00:00. Wait for it."
	if got := pool.Refusal(runners("a", "b"), readings, caps, model.AgentClaude); got != want {
		t.Fatalf("Refusal = %q", got)
	}
}

func TestReadAll_OneReaderThatPanicsLeavesTheOtherReadings(t *testing.T) {
	t.Parallel()

	read := func(_ context.Context, runner model.Runner) model.Reading {
		if runner.Name == "a" {
			panic("boom") //nolint:forbidigo // the test needs a reader that panics
		}

		return use(1, 2)
	}

	readings := pool.ReadAll(t.Context(), runners("a", "b"), read)

	if reason := readings["a"].Reason(); reason != "the read failed: boom" {
		t.Fatalf("reason = %q", reason)
	}

	if _, ok := readings["b"].Usage(); !ok {
		t.Fatal("the other reading is lost")
	}
}

func TestReadAll_TheRunnersAreReadAtTheSameTime(t *testing.T) {
	t.Parallel()

	var (
		arrived sync.WaitGroup
		release = make(chan struct{})
	)

	arrived.Add(3)

	go func() {
		arrived.Wait()
		close(release)
	}()

	read := func(_ context.Context, _ model.Runner) model.Reading {
		arrived.Done()

		select {
		case <-release:
			return use(1, 2)
		case <-time.After(5 * time.Second):
			return model.Unknown("read one after the other")
		}
	}

	for name, reading := range pool.ReadAll(t.Context(), runners("a", "b", "c"), read) {
		if _, ok := reading.Usage(); !ok {
			t.Fatalf("%s: %s", name, reading.Reason())
		}
	}
}

func TestSelect_IsErrNoRunnerWithoutRunnersOfTheAgent(t *testing.T) {
	t.Parallel()

	only := []model.Runner{{Name: "o", Agent: model.AgentOpenCode}}

	if _, err := pool.Select(t.Context(), only, caps, model.AgentClaude, readerOf(nil)); !errors.Is(err, pool.ErrNoRunner) {
		t.Fatalf("err = %v", err)
	}
}

func TestSelect_PicksTheRunnerWithRoom(t *testing.T) {
	t.Parallel()

	got, err := pool.Select(t.Context(), runners("a", "b"), caps, model.AgentClaude,
		readerOf(map[string]model.Reading{"a": use(5, 50), "b": use(5, 10)}))
	if err != nil || got.Name != "b" {
		t.Fatalf("Select = %v, %v", got, err)
	}
}

func TestSelect_NoneQualifiesIsATypedErrorWithTheRefusal(t *testing.T) {
	t.Parallel()

	_, err := pool.Select(t.Context(), runners("a", "b"), caps, model.AgentClaude,
		readerOf(map[string]model.Reading{"a": model.Unknown("x"), "b": model.Unknown("y")}))

	var none *pool.NoneQualifiesError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v", err)
	}

	if !strings.HasPrefix(none.Error(), "no claude runner is under both caps") {
		t.Fatalf("Error = %q", none.Error())
	}
}

func TestSelect_LeavesOutRunnersOfOtherAgents(t *testing.T) {
	t.Parallel()

	list := append(runners("a"), model.Runner{Name: "o", Agent: model.AgentOpenCode})

	got, err := pool.Select(t.Context(), list, caps, model.AgentClaude, readerOf(map[string]model.Reading{"a": use(1, 1)}))
	if err != nil || got.Name != "a" {
		t.Fatalf("Select = %v, %v", got, err)
	}
}

func TestCommand_PrintsALineForEachRunnerAndThePick(t *testing.T) {
	t.Parallel()

	list := append(runners("a", "b", "c"), model.Runner{Name: "o", Agent: model.AgentOpenCode})
	readings := map[string]model.Reading{"a": use(95, 10), "b": use(10, 20), "c": model.Unknown("boom")}

	lines, err := pool.Command(t.Context(), list, caps, model.AgentClaude, readerOf(readings))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"a: " + pool.Describe(readings["a"]), "b: " + pool.Describe(readings["b"]), "c: unknown: boom", "pick: b"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %q", lines)
	}
}

func TestCommand_NoneQualifiesKeepsTheLinesAndReturnsTheRefusal(t *testing.T) {
	t.Parallel()

	readings := map[string]model.Reading{"a": useReset(95, 10, t2, t2), "b": useReset(10, 99, t1, t1)}

	lines, err := pool.Command(t.Context(), runners("a", "b"), caps, model.AgentClaude, readerOf(readings))

	var none *pool.NoneQualifiesError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v", err)
	}

	if lines[len(lines)-1] != "pick: none" {
		t.Fatalf("lines = %q", lines)
	}

	want := "no claude runner is under both caps (five-hour 90%, seven-day 95%). " +
		"The earliest reset is 2026-10-24T05:00:00+00:00. Wait for it."
	if none.Error() != want {
		t.Fatalf("Error = %q", none.Error())
	}
}

func TestChoose_PrefersARunnerThatIsNotBusy(t *testing.T) {
	t.Parallel()

	readings := map[string]model.Reading{"a": use(1, 1), "b": use(80, 80)}

	chosen, ok := pool.Choose(runners("a", "b"), readings, caps, "a")
	if !ok || chosen.Name != "b" {
		t.Fatalf("Choose = %v, %v: a free runner beats a busy one with lower use", chosen, ok)
	}
}

func TestChoose_EveryRunnerBusyFallsBackToTheLowestUse(t *testing.T) {
	t.Parallel()

	readings := map[string]model.Reading{"a": use(1, 30), "b": use(80, 10)}

	chosen, ok := pool.Choose(runners("a", "b"), readings, caps, "a", "b")
	if !ok || chosen.Name != "b" {
		t.Fatalf("Choose = %v, %v: with all busy the lowest seven-day use wins", chosen, ok)
	}
}

func TestChoose_AFreeRunnerOverACapStaysOut(t *testing.T) {
	t.Parallel()

	readings := map[string]model.Reading{"a": use(1, 1), "b": use(99, 99)}

	chosen, ok := pool.Choose(runners("a", "b"), readings, caps, "a")
	if !ok || chosen.Name != "a" {
		t.Fatalf("Choose = %v, %v: a free runner over a cap is not chosen", chosen, ok)
	}
}

func TestSelect_PrefersARunnerThatIsNotBusy(t *testing.T) {
	t.Parallel()

	got, err := pool.Select(t.Context(), runners("a", "b"), caps, model.AgentClaude,
		readerOf(map[string]model.Reading{"a": use(1, 1), "b": use(5, 5)}), "a")
	if err != nil || got.Name != "b" {
		t.Fatalf("Select = %v, %v", got, err)
	}
}
