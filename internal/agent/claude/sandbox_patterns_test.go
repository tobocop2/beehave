package claude_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent/claude"
)

func TestSandbox_DenyWritePatternsAreDeniedForWriteAndEditAndStayReadable(t *testing.T) {
	t.Parallel()

	p := plan()
	p.DenyWritePatterns = []string{"/work/tree/**/.husky", "/work/tree/**/.claude/settings*.json"}

	args := claude.New().Sandbox(p, "", "/run").Args

	var parsed struct {
		Sandbox struct {
			Filesystem struct{ DenyRead, DenyWrite []string }
		}
		Permissions struct{ Deny []string }
	}

	require.NoError(t, json.Unmarshal([]byte(args[len(args)-1]), &parsed))

	for _, pattern := range p.DenyWritePatterns {
		assert.Contains(t, parsed.Sandbox.Filesystem.DenyWrite, pattern)
		assert.NotContains(t, parsed.Sandbox.Filesystem.DenyRead, pattern)
	}

	assert.Contains(t, parsed.Permissions.Deny, "Edit(//work/tree/**/.husky)")
	assert.Contains(t, parsed.Permissions.Deny, "Edit(//work/tree/**/.claude/settings*.json)")
	assert.NotContains(t, parsed.Permissions.Deny, "Read(//work/tree/**/.husky)")
}
