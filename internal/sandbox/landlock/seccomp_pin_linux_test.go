//go:build linux && (amd64 || arm64)

package landlock

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// TestInstall_BothCallsRunOnOneThread covers the rule of the kernel that no_new_privs is a per-thread attribute, so the filter load fails with EACCES on a
// thread that did not set it. The first call tries to push the goroutine to another thread: it yields while other goroutines keep the other processors
// busy. An install that does not pin its thread moves, and the test sees two thread numbers. The test depends on the scheduler to move an unpinned
// goroutine, so it shows the pin works and cannot show that every unpinned run fails.
func TestInstall_BothCallsRunOnOneThread(t *testing.T) {
	t.Parallel()

	const yields = 10000

	var stop atomic.Bool

	var spinners sync.WaitGroup

	for range runtime.GOMAXPROCS(0) {
		spinners.Add(1)

		go func() {
			defer spinners.Done()

			for !stop.Load() {
				runtime.Gosched()
			}
		}()
	}

	defer func() {
		stop.Store(true)
		spinners.Wait()
	}()

	threads := map[uintptr]int{}

	kernel := filterKernel{
		assemble: bpf.Assemble,
		call: func(number, _, _ uintptr, _ unsafe.Pointer) error {
			first := unix.Gettid()
			threads[number] = first

			if number == unix.SYS_PRCTL {
				for range yields {
					if unix.Gettid() != first {
						break
					}

					runtime.Gosched()
				}
			}

			return nil
		},
	}

	require.NoError(t, kernel.install())
	assert.Equal(t, threads[unix.SYS_PRCTL], threads[unix.SYS_SECCOMP])
}
