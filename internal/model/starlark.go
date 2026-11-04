package model

import (
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// StarlarkMaxSteps is the number of Starlark instructions that one file or one run may execute. A workflow run, a workflow check and a config file share it.
// The count is the same on a replay, so a run that hits it hits it every time.
const StarlarkMaxSteps = 1_000_000

// StarlarkFileOptions returns the file options of every Starlark file that beehave reads: the zero value, so no while, no recursion and no top-level control flow.
func StarlarkFileOptions() *syntax.FileOptions { return &syntax.FileOptions{} }

// builtinFile is the file name that starlark-go gives to the frame of a Go builtin.
const builtinFile = "<builtin>"

// InnermostPosition returns the position of the innermost frame of a call stack that is in a file. A builtin such as fail has no position of its own.
func InnermostPosition(stack starlark.CallStack) syntax.Position {
	var pos syntax.Position

	for i := range stack {
		if pos = stack.At(i).Pos; pos.Filename() != builtinFile {
			break
		}
	}

	return pos
}
