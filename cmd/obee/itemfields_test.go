package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store/beads"
	"github.com/tobocop2/obee/internal/store/github"
	"github.com/tobocop2/obee/internal/store/local"
)

// The check of a workflow file reads model.ItemFields, so each tracker must declare exactly that set.
func TestItemFields_EveryTrackerDeclaresTheSetThatTheCheckReads(t *testing.T) {
	t.Parallel()

	want := model.ItemFields()

	assert.Equal(t, want, (*local.Store)(nil).ItemFields())
	assert.Equal(t, want, (*beads.Store)(nil).ItemFields())
	assert.Equal(t, want, (*github.Store)(nil).ItemFields())
}
