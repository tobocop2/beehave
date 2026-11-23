package opencode

import (
	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

// hookEvent is the call that the guard plugin forwards: the folder of the run, the tool of the call and its arguments, as OpenCode gives them to the hook.
type hookEvent struct {
	Directory string `json:"directory"`
	Tool      string `json:"tool"`
	Args      struct {
		Command string `json:"command"`
	} `json:"args"`
}

// DecodeHook reads the call that the guard plugin of OpenCode forwards as JSON on stdin.
func DecodeHook(input string) (agent.HookCall, error) {
	event, err := model.Decode[hookEvent]("the hook event", input)
	if err != nil {
		return agent.HookCall{}, err
	}

	return agent.HookCall{Shell: event.Tool == bashTool, Dir: event.Directory, Command: event.Args.Command}, nil
}
