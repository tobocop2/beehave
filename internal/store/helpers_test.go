package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

const noAnswer = 99

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

func (s *scripted) called(words ...string) bool {
	return slices.ContainsFunc(s.calls, func(call []string) bool { return answer{words: words}.fits(call) })
}

func bodyFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "body.md")
	require.NoError(t, os.WriteFile(path, []byte("finding\n"), 0o600))

	return path
}

func toJSON(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return string(data)
}

func fill(count int, row map[string]any) []map[string]any {
	rows := make([]map[string]any, count)
	for i := range rows {
		rows[i] = map[string]any{"id": fmt.Sprintf("p-%d", i), "number": i + 1, "title": fmt.Sprintf("t%d", i)}
		for key, value := range row {
			rows[i][key] = value
		}
	}

	return rows
}

func writeFile(path, text string) error {
	return os.WriteFile(path, []byte(text), 0o600)
}
