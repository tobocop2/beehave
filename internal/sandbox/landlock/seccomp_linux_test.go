//go:build linux && (amd64 || arm64)

package landlock

import (
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"slices"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// childMarker is the argument that makes the helper test below act as the child that loads the filter.
const childMarker = "beehave-filter-child"

func TestRawCall_ReturnsTheErrorNumberOfTheKernel(t *testing.T) {
	t.Parallel()

	require.NoError(t, rawCall(unix.SYS_GETPID, 0, 0, nil))
	require.ErrorIs(t, rawCall(unix.SYS_CLOSE, ^uintptr(0), 0, nil), unix.EBADF)
}

func TestSocketProgram_AssemblesAndHasTheJumpsOfTheFilter(t *testing.T) {
	t.Parallel()

	program := socketProgram()
	_, err := bpf.Assemble(program)
	require.NoError(t, err)
	assert.Len(t, program, 12)

	vm, err := bpf.NewVM(program)
	require.NoError(t, err)

	cases := []struct {
		name   string
		arch   uint32
		number uint32
		domain uint32
		want   uint32
	}{
		{"socket for AF_UNIX is refused", auditArch, unix.SYS_SOCKET, unix.AF_UNIX, unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{"socket for AF_INET is allowed", auditArch, unix.SYS_SOCKET, unix.AF_INET, unix.SECCOMP_RET_ALLOW},
		{"socket for AF_INET6 is allowed", auditArch, unix.SYS_SOCKET, unix.AF_INET6, unix.SECCOMP_RET_ALLOW},
		{"io_uring_setup is refused", auditArch, unix.SYS_IO_URING_SETUP, 0, unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{"another call is allowed", auditArch, unix.SYS_GETPID, unix.AF_UNIX, unix.SECCOMP_RET_ALLOW},
		{"a call of another interface is refused", auditArch + 1, unix.SYS_GETPID, 0, unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		{"a call of the x32 interface is refused", auditArch, x32Flag | unix.SYS_GETPID, 0, unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			// A seccomp_data is the number, the architecture, the instruction pointer and six 64-bit arguments. The test machine of the BPF interpreter reads
			// words in network order, so the words are written in that order.
			data := make([]byte, 64)
			binary.BigEndian.PutUint32(data[offsetNumber:], c.number)
			binary.BigEndian.PutUint32(data[offsetArch:], c.arch)
			binary.BigEndian.PutUint32(data[offsetArgument:], c.domain)

			got, err := vm.Run(data)
			require.NoError(t, err)
			assert.Equal(t, int(c.want), got)
		})
	}
}

func TestInstall_EachStepThatFailsStopsTheInstallation(t *testing.T) {
	t.Parallel()

	failure := errors.New("refused")
	calls := []uintptr{}

	recording := func(by uintptr) filterKernel {
		return filterKernel{
			assemble: bpf.Assemble,
			call: func(number, _, _ uintptr, _ unsafe.Pointer) error {
				calls = append(calls, number)
				if number == by {
					return failure
				}

				return nil
			},
		}
	}

	require.NoError(t, recording(0).install())
	assert.Equal(t, []uintptr{unix.SYS_PRCTL, unix.SYS_SECCOMP}, calls)

	err := recording(unix.SYS_PRCTL).install()
	require.ErrorIs(t, err, failure)
	require.ErrorContains(t, err, "no_new_privs")

	err = recording(unix.SYS_SECCOMP).install()
	require.ErrorIs(t, err, failure)
	require.ErrorContains(t, err, "load the socket filter")

	broken := filterKernel{assemble: func([]bpf.Instruction) ([]bpf.RawInstruction, error) { return nil, failure }}
	err = broken.install()
	require.ErrorIs(t, err, failure)
	assert.ErrorContains(t, err, "assemble the socket filter")
}

// TestKernel_TheFilterRefusesAUnixSocketInAChildThatLoadedIt runs a child of the test binary that loads the real filter and then asks for a unix socket and
// a TCP socket. The test process itself stays unfiltered.
func TestKernel_TheFilterRefusesAUnixSocketInAChildThatLoadedIt(t *testing.T) {
	t.Parallel()

	self, err := os.Executable()
	require.NoError(t, err)

	out, err := exec.CommandContext(t.Context(), self, "-test.v", "-test.run=^TestHelperProcess_LoadsTheFilterAndOpensSockets$", childMarker).CombinedOutput() //nolint:gosec // the program is the test binary
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "unix refused, tcp allowed")
}

func TestHelperProcess_LoadsTheFilterAndOpensSockets(t *testing.T) {
	t.Parallel()

	if !slices.Contains(os.Args, childMarker) {
		t.Skip("runs only as the child of the test above")
	}

	require.NoError(t, newKernel().install())

	_, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	require.ErrorIs(t, err, unix.EPERM)

	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	require.NoError(t, err)
	require.NoError(t, unix.Close(fd))

	t.Log("unix refused, tcp allowed")
}
