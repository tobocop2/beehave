package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The wrapper puts the program and the arguments of the caller in separate words: a dropped separator makes the word /usr/bin/gitinit.
func TestWrapperText_RunsTheProgramAndTheArgumentsAsSeparateWords(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "#!/bin/sh\nexec \"/usr/bin/git\" \"$@\"\n", wrapperText("/usr/bin/git"))
	assert.Contains(t, wrapperText("/usr/bin/git"), `"/usr/bin/git" "$@"`)
}
