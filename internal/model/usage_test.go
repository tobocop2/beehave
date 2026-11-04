package model_test

import (
	"testing"

	"github.com/tobocop2/beehave/internal/model"
)

func TestReading_KnownHoldsUsageAndNoReason(t *testing.T) {
	t.Parallel()

	want := model.Usage{FiveHour: model.Window{Percent: 1}, SevenDay: model.Window{Percent: 2}}

	got, ok := model.Known(want).Usage()
	if !ok || got != want {
		t.Fatalf("Usage() = %v, %v", got, ok)
	}

	if reason := model.Known(want).Reason(); reason != "" {
		t.Fatalf("Reason() = %q", reason)
	}
}

func TestReading_UnknownHoldsTheReason(t *testing.T) {
	t.Parallel()

	reading := model.Unknown("boom")

	if _, ok := reading.Usage(); ok {
		t.Fatal("an unknown reading holds usage")
	}

	if reading.Reason() != "boom" {
		t.Fatalf("Reason() = %q", reading.Reason())
	}
}

func TestReading_UnknownLoginHoldsTheReasonAndNeedsALogin(t *testing.T) {
	t.Parallel()

	reading := model.UnknownLogin("log in")

	if _, ok := reading.Usage(); ok || reading.Reason() != "log in" || !reading.NeedsLogin() {
		t.Fatalf("reading = %+v", reading)
	}

	if model.Unknown("x").NeedsLogin() || model.Known(model.Usage{}).NeedsLogin() {
		t.Fatal("only UnknownLogin needs a login")
	}
}

func TestReading_TheZeroReadingIsUnknownNotZeroUsage(t *testing.T) {
	t.Parallel()

	var reading model.Reading

	if _, ok := reading.Usage(); ok {
		t.Fatal("the zero reading holds usage")
	}

	if reading.Reason() != "no reading" {
		t.Fatalf("Reason() = %q", reading.Reason())
	}
}
