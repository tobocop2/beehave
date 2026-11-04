//go:build linux && (amd64 || arm64)

package landlock

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// The offsets of the fields of struct seccomp_data that the filter reads. The arguments are 64-bit; the low word of the first one is at offset 16 on the
// little-endian processors that beehave builds for.
const (
	offsetNumber   = 0
	offsetArch     = 4
	offsetArgument = 16
	// x32Flag is set in the system call number of the x32 interface of an amd64 kernel. A kernel that has no x32 interface never sees it.
	x32Flag = 0x40000000
	// domainLocal is AF_UNIX. The filter tests bit 0 of the domain, so it also refuses the other odd address families (AF_KEY, AF_PACKET and a few that no
	// program uses), and a value with garbage in the upper half cannot slip past it, since the kernel reads the domain as a 32-bit integer.
	domainLocal = 1
	// wordSize is the size in bytes of the word that the filter loads. skipToDeny and skipToAllow are the jumps of the two calls that the filter looks at, to
	// the instruction that refuses and to the one that allows.
	wordSize    = 4
	skipToDeny  = 3
	skipToAllow = 3
)

// newKernel returns the seccomp interface of this machine. A test builds a filterKernel with its own functions.
func newKernel() filterKernel { return filterKernel{assemble: bpf.Assemble, call: rawCall} }

// filterKernel is what the socket filter asks of the system: the assembler of the program and a system call.
type filterKernel struct {
	assemble func([]bpf.Instruction) ([]bpf.RawInstruction, error)
	call     func(number, a, b uintptr, c unsafe.Pointer) error
}

func rawCall(number, a, b uintptr, c unsafe.Pointer) error {
	if _, _, errno := unix.Syscall(number, a, b, uintptr(c)); errno != 0 {
		return errno
	}

	return nil
}

// socketProgram is the filter. Whatever the call, it first refuses every system call of another interface than the native one of this machine (a 32-bit
// program on a 64-bit kernel, the x32 interface), since the rest of the program only knows the native numbers. Then it refuses socket(2) for
// AF_UNIX, and io_uring_setup(2), since an io_uring can open a socket without a socket(2) call. It allows everything else.
func socketProgram() []bpf.Instruction {
	deny := bpf.RetConstant{Val: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)}
	foreign := bpf.RetConstant{Val: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)}

	return []bpf.Instruction{
		bpf.LoadAbsolute{Off: offsetArch, Size: wordSize},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: auditArch, SkipTrue: 1},
		foreign,
		bpf.LoadAbsolute{Off: offsetNumber, Size: wordSize},
		bpf.JumpIf{Cond: bpf.JumpLessThan, Val: x32Flag, SkipTrue: 1},
		foreign,
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.SYS_IO_URING_SETUP, SkipTrue: skipToDeny},
		bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: unix.SYS_SOCKET, SkipTrue: skipToAllow},
		bpf.LoadAbsolute{Off: offsetArgument, Size: wordSize},
		bpf.JumpIf{Cond: bpf.JumpBitsNotSet, Val: domainLocal, SkipTrue: 1},
		deny,
		bpf.RetConstant{Val: unix.SECCOMP_RET_ALLOW},
	}
}

// install loads the filter into every thread of this process, and into the children of the process. The kernel needs no_new_privs for that.
func (k filterKernel) install() error {
	raw, err := k.assemble(socketProgram())
	if err != nil {
		return fmt.Errorf("assemble the socket filter: %w", err)
	}

	if err := k.call(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, nil); err != nil {
		return fmt.Errorf("set no_new_privs for the socket filter: %w", err)
	}

	filter := sockFilters(raw)
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]} //nolint:gosec // the program has twelve instructions

	err = k.call(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, unsafe.Pointer(&program)) //nolint:gosec // the call reads the program at once
	runtime.KeepAlive(filter)

	if err != nil {
		return fmt.Errorf("load the socket filter: %w", err)
	}

	return nil
}

// sockFilters converts the assembled instructions to the form of the kernel.
func sockFilters(raw []bpf.RawInstruction) []unix.SockFilter {
	filter := make([]unix.SockFilter, len(raw))
	for i, instruction := range raw {
		filter[i] = unix.SockFilter{Code: instruction.Op, Jt: instruction.Jt, Jf: instruction.Jf, K: instruction.K}
	}

	return filter
}
