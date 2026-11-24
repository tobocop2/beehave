package agent

import "errors"

// HookCall is one shell call that an agent CLI sends to the guard before it runs the call.
type HookCall struct {
	// Dir is the folder the shell command runs in, or empty when the hook input names none.
	Dir string
	// Command is the command line of the call.
	Command string
}

// HookDecoder reads the hook input of one agent CLI, as it arrives on stdin, and returns the shell call in it.
// The guard hook runs for the shell tool only, so an input that is not a shell call with a command is an error, never an allowed call.
type HookDecoder func(input string) (HookCall, error)

var (
	errNotShellCall = errors.New("the hook event is not a call of the shell tool")
	errNoCommand    = errors.New("the shell call of the hook event has no command")
)

// ShellCall returns the call of a hook event that names tool, dir and command. It is an error when tool is not shellTool, and when command is empty.
func ShellCall(tool, shellTool, dir, command string) (HookCall, error) {
	switch {
	case tool != shellTool:
		return HookCall{}, errNotShellCall
	case command == "":
		return HookCall{}, errNoCommand
	}

	return HookCall{Dir: dir, Command: command}, nil
}
