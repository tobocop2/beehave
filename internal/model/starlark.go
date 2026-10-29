package model

import "go.starlark.net/syntax"

// StarlarkMaxSteps is the number of Starlark instructions that one file or one run may execute. A workflow run, a workflow check and a config file share it.
// The count is the same on a replay, so a run that hits it hits it every time.
const StarlarkMaxSteps = 1_000_000

// StarlarkFileOptions returns the file options of every Starlark file that obee reads: the zero value, so no while, no recursion and no top-level control flow.
func StarlarkFileOptions() *syntax.FileOptions { return &syntax.FileOptions{} }
