package claude

import (
	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/model"
)

// hookEvent is the part of a PreToolUse event that the guard reads.
type hookEvent struct {
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// DecodeHook reads one PreToolUse event of Claude Code, the JSON that the hook gets on stdin.
func DecodeHook(input string) (agent.HookCall, error) {
	event, err := model.Decode[hookEvent]("the hook event", input)
	if err != nil {
		return agent.HookCall{}, err
	}

	return agent.ShellCall(event.ToolName, bashTool, event.Cwd, event.ToolInput.Command)
}
