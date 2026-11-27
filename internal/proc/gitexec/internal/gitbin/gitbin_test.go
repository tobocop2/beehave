package gitbin_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/proc/gitexec/internal/gitbin"
)

func TestArgs_PutsTheHooksSettingFirstAndKeepsTheArguments(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"-c", "core.hooksPath=/dev/null", "status", "-s"}, gitbin.Args([]string{"status", "-s"}))
	assert.Equal(t, "git", gitbin.Name)
}
