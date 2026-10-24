package forge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
)

const (
	repo     = "owner/name"
	noAnswer = 99
)

type answer struct {
	words    []string
	contains string
	result   model.CommandResult
}

// scripted is a Runner that answers by the words in the arguments. The latest plan that fits wins.
type scripted struct {
	answers []answer
	calls   [][]string
}

func (s *scripted) Run(_ context.Context, args []string) model.CommandResult {
	s.calls = append(s.calls, slices.Clone(args))

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

func (s *scripted) plan(stdout string, words ...string) {
	s.answers = append(s.answers, answer{words: words, result: model.CommandResult{Stdout: stdout}})
}

func (s *scripted) planFail(stderr string, words ...string) {
	s.answers = append(s.answers, answer{words: words, result: model.CommandResult{ExitCode: 1, Stderr: stderr}})
}

func (s *scripted) planContaining(stdout, text string) {
	s.answers = append(s.answers, answer{contains: text, result: model.CommandResult{Stdout: stdout}})
}

func (s *scripted) planContainingFail(text string) {
	s.answers = append(s.answers, answer{contains: text, result: model.CommandResult{ExitCode: 1}})
}

func (s *scripted) called(text string) bool {
	return slices.ContainsFunc(s.calls, func(call []string) bool {
		return slices.ContainsFunc(call, func(arg string) bool { return strings.Contains(arg, text) })
	})
}

// fixed builds a forge whose every call gets the same answer.
func fixed(t *testing.T, stdout string) (*forge.Forge, *scripted) {
	t.Helper()

	run := &scripted{answers: []answer{{result: model.CommandResult{Stdout: stdout}}}}

	return newForge(t, repo, run), run
}

// failing builds a forge whose every call exits 1 with the stderr.
func failing(t *testing.T, stderr string) (*forge.Forge, *scripted) {
	t.Helper()

	run := &scripted{answers: []answer{{result: model.CommandResult{ExitCode: 1, Stderr: stderr}}}}

	return newForge(t, repo, run), run
}

func toJSON(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return string(data)
}

type fields = map[string]any

// graph wraps pull request fields as a GraphQL answer.
func graph(t *testing.T, pull fields) string {
	t.Helper()

	return toJSON(t, fields{"data": fields{"repository": fields{"pullRequest": pull}}})
}

func commandResult(stdout string, exit int) model.CommandResult {
	return model.CommandResult{ExitCode: exit, Stdout: stdout}
}

func newForge(t *testing.T, name string, run forge.Runner) *forge.Forge {
	t.Helper()

	f, err := forge.New(name, run)
	require.NoError(t, err)

	return f
}
