package claude_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/agent/claude"
)

func TestCalls_TheResultLineReportsEachToolCallThatThePermissionRulesRefused(t *testing.T) {
	t.Parallel()

	// The shape of SDKPermissionDenial on the result message: tool_name, tool_use_id and tool_input.
	line := `{"type":"result","subtype":"success","session_id":"s1","permission_denials":[` +
		`{"tool_name":"Write","tool_use_id":"t1","tool_input":{"file_path":"/state/outputs/1.review.json","content":"{}"}},` +
		`{"tool_name":"NotebookEdit","tool_use_id":"t2","tool_input":{"notebook_path":"/w/a.ipynb"}},` +
		`{"tool_name":"Bash","tool_use_id":"t3","tool_input":{"command":"git push"}},` +
		`{"tool_use_id":"t4","tool_input":{"file_path":"/no/tool"}},` +
		`{"tool_name":"Edit","tool_use_id":"t5","tool_input":{"file_path":"/w/files.go"}},` +
		`{"tool_name":"Read","tool_use_id":"t6","tool_input":{"file_path":"/w/secret"}},` +
		`"not an object"]}`

	assert.Equal(t, []agent.Denial{
		{Tool: "Write", Target: "/state/outputs/1.review.json", Edit: true},
		{Tool: "NotebookEdit", Target: "/w/a.ipynb", Edit: true},
		{Tool: "Bash", Target: "git push"},
		{Tool: "Edit", Target: "/w/files.go", Edit: true},
		{Tool: "Read", Target: "/w/secret"},
	}, claude.New(nil).Calls(line).Denials)
}

func TestCalls_OnlyTheResultLineReportsDenials(t *testing.T) {
	t.Parallel()

	for _, line := range []string{
		`{"type":"result","subtype":"success","permission_denials":[]}`,
		`{"type":"result","subtype":"success"}`,
		`{"type":"result","subtype":"success","permission_denials":"none"}`,
		`{"type":"assistant","permission_denials":[{"tool_name":"Write","tool_input":{"file_path":"/x"}}]}`,
	} {
		assert.Empty(t, claude.New(nil).Calls(line).Denials, line)
	}
}
