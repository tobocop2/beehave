package agent

// HookCall is one tool call that an agent CLI sends to the guard before it runs the call.
type HookCall struct {
	// Shell is true when the call runs a shell command.
	Shell bool
	// Dir is the folder the shell command runs in, or empty when the hook input names none.
	Dir string
	// Command is the command line of a shell call.
	Command string
}

// HookDecoder reads the hook input of one agent CLI, as it arrives on stdin, and returns the call. It returns an error for an input that is not in the format of the CLI.
type HookDecoder func(input string) (HookCall, error)
