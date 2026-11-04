package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnderAny_AChildWhoseNameStartsWithTwoDotsLiesUnderTheFolder(t *testing.T) {
	t.Parallel()

	roots := []string{"/gates", "/real/gates"}

	assert.True(t, underAny(roots, "/gates/..x"))
	assert.True(t, underAny(roots, "/real/gates/a/..x/b"))
	assert.True(t, underAny(roots, "/gates/work"))
}

func TestUnderAny_TheFolderItselfAndItsSiblingsAreNotUnderIt(t *testing.T) {
	t.Parallel()

	roots := []string{"/gates"}

	assert.False(t, underAny(roots, "/gates"))
	assert.False(t, underAny(roots, "/gates-other/work"))
	assert.False(t, underAny(roots, "/other"))
	assert.False(t, underAny(nil, "/gates/work"))
	assert.False(t, underAny(roots, "/gates/"))
	assert.False(t, underAny(roots, "/gates/.."))
	assert.False(t, underAny(roots, "/gates/../other"))
	assert.False(t, underAny(roots, ".."))
}

func TestUnderAny_ARootThatReachesTheFolderThroughALinkMatchesByBothNames(t *testing.T) {
	t.Parallel()

	roots := []string{"/link/gates", "/real/gates"}

	assert.True(t, underAny(roots, "/link/gates/w"))
	assert.True(t, underAny(roots, "/real/gates/w"))
	assert.True(t, underAny(roots, "/real/gates/..x"))
	assert.False(t, underAny(roots, "/link/gates"))
	assert.False(t, underAny(roots, "/real/gates/"))
	assert.False(t, underAny(roots, "/real/gates/../w"))
}
