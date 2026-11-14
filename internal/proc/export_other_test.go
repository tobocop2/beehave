//go:build !linux

package proc

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// WithCount returns r with a count of the processes of the user that tests supply.
func WithCount(r ExecRunner, count func() (int, error)) ExecRunner {
	r.hooks.count = count

	return r
}

// CapArg0 is the name that starts the cap helper.
const CapArg0 = capArg0

// RunCapHelper runs the cap helper for argv with system calls that tests supply. The helper writes a failure to a copy of the descriptor of fail, and owns the copy.
func RunCapHelper(argv []string, fail *os.File, setrlimit func(int, *unix.Rlimit) error, exec func(string, []string, []string) error, exit func(int)) error {
	fd, err := syscall.Dup(int(fail.Fd()))
	if err != nil {
		return err
	}

	runIfCapHelper(argv, uintptr(fd), capSystem{setrlimit: setrlimit, exec: exec, environ: func() []string { return []string{"A=b"} }, exit: exit})

	return nil
}

// WithPipe returns r with a maker of the setup pipe that tests supply.
func WithPipe(r ExecRunner, pipe func() (*os.File, *os.File, error)) ExecRunner {
	r.hooks.pipe = pipe

	return r
}

// WithExecutable returns r with a lookup of the running program that tests supply.
func WithExecutable(r ExecRunner, executable func() (string, error)) ExecRunner {
	r.hooks.executable = executable

	return r
}
