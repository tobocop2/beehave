// Package proctest holds a scripted proc.Runner for tests: it answers each command from a list of plans and records the calls.
package proctest

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

const noAnswer = 99

type answer struct {
	words    []string
	contains string
	result   model.CommandResult
}

// Scripted is a proc.Runner that answers by the words in the arguments. The latest plan that fits wins.
type Scripted struct {
	answers []answer
	Calls   [][]string
}

// Always returns a Scripted whose every call gets the same result.
func Always(result model.CommandResult) *Scripted {
	return &Scripted{answers: []answer{{result: result}}}
}

// Run records the call and returns the latest plan that fits it, or exit code 99 when no plan fits.
func (s *Scripted) Run(_ context.Context, args []string) model.CommandResult {
	s.Calls = append(s.Calls, slices.Clone(args))

	for i := len(s.answers) - 1; i >= 0; i-- {
		if s.answers[i].fits(args) {
			return s.answers[i].result
		}
	}

	return model.CommandResult{ExitCode: noAnswer, Stderr: fmt.Sprintf("scripted: no answer planned for %v", args)}
}

func (a answer) fits(args []string) bool {
	if a.contains != "" {
		return slices.ContainsFunc(args, func(arg string) bool { return strings.Contains(arg, a.contains) })
	}

	for start := 0; start+len(a.words) <= len(args); start++ {
		if slices.Equal(args[start:start+len(a.words)], a.words) {
			return true
		}
	}

	return false
}

// Plan answers a call whose arguments hold the words in a row with the stdout.
func (s *Scripted) Plan(stdout string, words ...string) {
	s.answers = append(s.answers, answer{words: words, result: model.CommandResult{Stdout: stdout}})
}

// PlanResult answers a call whose arguments hold the words in a row with the result.
func (s *Scripted) PlanResult(result model.CommandResult, words ...string) {
	s.answers = append(s.answers, answer{words: words, result: result})
}

// PlanFail answers a call whose arguments hold the words in a row with exit code 1 and the stderr.
func (s *Scripted) PlanFail(stderr string, words ...string) {
	s.answers = append(s.answers, answer{words: words, result: model.CommandResult{ExitCode: 1, Stderr: stderr}})
}

// PlanContaining answers a call with an argument that holds the text with the stdout.
func (s *Scripted) PlanContaining(stdout, text string) {
	s.answers = append(s.answers, answer{contains: text, result: model.CommandResult{Stdout: stdout}})
}

// PlanContainingFail answers a call with an argument that holds the text with exit code 1.
func (s *Scripted) PlanContainingFail(text string) {
	s.answers = append(s.answers, answer{contains: text, result: model.CommandResult{ExitCode: 1}})
}

// Called reports whether a call held the words in a row in its arguments.
func (s *Scripted) Called(words ...string) bool {
	return slices.ContainsFunc(s.Calls, func(call []string) bool { return answer{words: words}.fits(call) })
}

// CalledContaining reports whether a call had an argument that holds the text.
func (s *Scripted) CalledContaining(text string) bool {
	return slices.ContainsFunc(s.Calls, func(call []string) bool { return answer{contains: text}.fits(call) })
}
