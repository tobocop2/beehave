package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

func TestLogins_HeadroomIsFalseWithNoReaderAndTakesTheAnswerOfTheReaderOtherwise(t *testing.T) {
	t.Parallel()

	assert.False(t, logins{}.Headroom(t.Context()), "a value built with no reader has no room to report")
	assert.True(t, logins{room: func(context.Context) bool { return true }}.Headroom(t.Context()))
	assert.False(t, logins{room: func(context.Context) bool { return false }}.Headroom(t.Context()))
}

// claudePools holds the default caps of the Claude windows.
func claudePools() map[model.Agent]config.Pool {
	return map[model.Agent]config.Pool{claude.Name: {"five_hour": 90, "seven_day": 95}}
}

// claudeUsage is a reading of the two windows of Claude, the five-hour window first.
func claudeUsage(five, seven model.Window) model.Reading {
	five.Name, seven.Name = "five_hour", "seven_day"

	return model.Known(model.Usage{Windows: []model.Window{five, seven}})
}

func TestPoolRoom_ReadsEveryRunnerOfTheAgentAgainstTheUsageCaps(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Runners: []model.Runner{{Name: "a", Agent: claude.Name}, {Name: "b", Agent: claude.Name}},
		Pools:   claudePools(),
	}

	reading := func(percent map[string]float64) func(context.Context, model.Runner) model.Reading {
		return func(_ context.Context, runner model.Runner) model.Reading {
			return claudeUsage(model.Window{Percent: percent[runner.Name]}, model.Window{Percent: 1})
		}
	}

	assert.True(t, poolRoom(cfg, claude.Name, reading(map[string]float64{"a": 10, "b": 20}))(t.Context()))
	assert.False(t, poolRoom(cfg, claude.Name, reading(map[string]float64{"a": 10, "b": 95}))(t.Context()), "one runner over its cap leaves no room")
}
