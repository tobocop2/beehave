// Package gitbin names the git program and the setting that turns its hooks off. Only the package gitexec and the packages below it import it.
package gitbin

import "os"

// Name is the name of the git program.
const Name = "git"

// hooksOff is the setting that points the hooks folder at an empty place.
const hooksOff = "core.hooksPath=" + os.DevNull

// Args returns args with the setting that turns the hooks off in front of them.
func Args(args []string) []string {
	return append([]string{"-c", hooksOff}, args...)
}
