package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWorktreePaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		listing string
		want    []string
	}{
		{"empty", "", nil},
		{"one worktree", "worktree /a\nHEAD abc\nbranch refs/heads/x\n", []string{"/a"}},
		{"two worktrees", "worktree /a\nHEAD abc\n\nworktree /b c\nHEAD def\n", []string{"/a", "/b c"}},
		{"CRLF line ends", "worktree /a\r\nHEAD abc\r\n\r\nworktree /b\r\n", []string{"/a", "/b"}},
		{"no final newline", "worktree /a", []string{"/a"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, worktreePaths(test.listing))
		})
	}
}
