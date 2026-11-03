//go:build linux && !amd64 && !arm64

package landlock

import "errors"

// errNoFilter is the reason that Check gives on a processor architecture where obee has no socket filter.
var errNoFilter = errors.New("obee has no socket filter for this processor architecture")

// newKernel returns the seccomp filter of this machine. On this processor architecture obee has no filter, so the enforcer says it cannot run where the
// kernel does not close unix sockets by itself (Landlock ABI 9).
func newKernel() filterKernel { return filterKernel{} }

type filterKernel struct{}

func (filterKernel) install() error { return errNoFilter }
