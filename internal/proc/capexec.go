//go:build !linux

package proc

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// capArg0 is the name that the cap helper gets as its argument 0. A process that starts with this name sets the process limit, then replaces itself with the command.
// The name alone triggers the helper: any process that can start obee with this argv[0] can run a program, but it runs with its own rights, so the name gives no privilege.
// The limit comes from the system call, not from a shell: the builtin ulimit differs between shells, and dash has no option -u.
const capArg0 = "obee-proc-cap"

// capFailFD is the file descriptor of the pipe that carries the error of the helper. Only the helper writes it. It closes on exec, so the command never holds it.
const capFailFD = 3

// capHelperArgs is the count of the arguments that the helper needs: the limit and the program.
const capHelperArgs = 2

// capSystem holds the system calls of the helper, so that tests run the helper without a real limit or a real exec.
type capSystem struct {
	setrlimit func(resource int, rlim *unix.Rlimit) error
	exec      func(program string, argv, env []string) error
	environ   func() []string
	exit      func(code int)
}

// init runs the cap helper when this process was started as one. The check comes before any other code of the program, so a test binary and the obee binary both work as the helper.
func init() { //nolint:gochecknoinits // a re-executed process must act before main, the way a shell wrapper acts before the command
	runIfCapHelper(os.Args, capFailFD, capSystem{
		setrlimit: unix.Setrlimit,
		exec:      syscall.Exec,
		environ:   syscall.Environ,
		exit:      os.Exit, //nolint:forbidigo // the helper process ends here, before main; exec only returns on a failure
	})
}

// runIfCapHelper does nothing unless argv[0] is the name of the helper. Then it runs the helper, writes its failure to the descriptor failFD and exits with ExitCapFailed.
// The descriptor becomes an os.File only in the helper: an os.File closes its descriptor when the garbage collector frees it, which would break a normal process that uses descriptor 3.
func runIfCapHelper(argv []string, failFD uintptr, sys capSystem) {
	if len(argv) == 0 || argv[0] != capArg0 {
		return
	}

	fail := os.NewFile(failFD, "cap-error")

	fmt.Fprintln(fail, runCapHelper(argv[1:], fail, sys))
	sys.exit(ExitCapFailed)
}

// runCapHelper sets the process limit to args[0], then replaces the process with the program args[1] with the arguments args[2:]. It returns, always with an error, only when it fails.
func runCapHelper(args []string, fail *os.File, sys capSystem) error {
	if len(args) < capHelperArgs {
		return errors.New("the cap helper needs a limit and a program")
	}

	limit, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil {
		return err
	}

	if err := sys.setrlimit(unix.RLIMIT_NPROC, &unix.Rlimit{Cur: limit, Max: limit}); err != nil {
		return fmt.Errorf("setrlimit: %w", err)
	}

	syscall.CloseOnExec(int(fail.Fd()))

	return fmt.Errorf("%s: %w", args[1], sys.exec(args[1], args[1:], sys.environ()))
}
