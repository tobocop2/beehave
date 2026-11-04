// Package host holds the contract of a build host, its errors and the guards every provider gets. Each provider is a child package.
package host

import (
	"context"
	"errors"

	"github.com/tobocop2/obee/internal/model"
)

// Host is a machine that runs the gate jobs of a project. SkyPilot is the first provider.
//
// Up is the first call. Status, Submit, Wait, Cancel, Drain and Resume need a host that Up made.
// Down is the last call. Wait may run while Cancel or Drain runs. A Wait and a Cancel of one job may both bank at once;
// one of the two pushes can then fail on a ref lock, and that call returns the bank error with the state. A caller retries it: the bank is idempotent.
//
// Invariant: a job holds its slot until its work is banked, for at most max_run. The provider deletes an idle host with no obee code alive,
// so a provider's Submit keeps the host busy for the provider, and a provider's Wait banks the work of the host (BankAll) before it returns a final state.
// Cancel stops the job and returns once the job is final, its work banked and its slot ended, as Wait does; a canceled job keeps the host busy only that long.
// When the stop itself fails, the job may still run, so its slot holds (at most max_run) and Cancel returns the error.
// Wait returns the state of the job together with an error when the bank fails. A caller stops a host with Teardown, never with Down.
// Wait and Teardown scan the same root for repositories: WorkRoot. A provider with no setting for it uses the login folder of the host by its absolute path.
// A provider's Up turns on the provider-side idle delete only when its Submit and Wait hold that invariant.
// A provider that does not own its machine (ssh and slurm: the owner provides it) has no idle delete, so the invariant holds without a slot.
// Its Down sends nothing and its Teardown banks the work and leaves the machine up.
type Host interface {
	Up(ctx context.Context) error
	Status(ctx context.Context) (model.HostStatus, error)
	Down(ctx context.Context) error
	Submit(ctx context.Context, spec model.JobSpec) (model.JobID, error)
	Wait(ctx context.Context, id model.JobID) (model.JobState, error)
	Cancel(ctx context.Context, id model.JobID) error
	// WorkRoot is the one absolute path under which the host keeps its repositories. It is the same in every call.
	WorkRoot(ctx context.Context) (string, error)
	// Log is the end of the log of a job: the last part of what the job wrote.
	Log(ctx context.Context, id model.JobID) (string, error)
	Drain(ctx context.Context) error
	Resume(ctx context.Context) error
}

// The refusals of the guards.
var (
	ErrUnbanked   = errors.New("work on the host is not on a remote")
	ErrNoRepos    = errors.New("no repository found on the host")
	ErrDiskLow    = errors.New("the host is under its free-space floor. Free disk space on the host, or lower `host.disk_floor_gb`")
	ErrDiskRead   = errors.New("cannot read the free space of the host. Check that the host is up and answers `df`")
	ErrNotDeleted = errors.New("the delete call returned, but the host is not confirmed gone. Check the host in the console of the provider and delete it there; a host left up keeps billing")
	ErrInUse      = errors.New("a process uses the directory")
	ErrInUseRead  = errors.New("cannot tell whether a process uses the directory. Run the scan as the user that owns the processes")
	ErrLost       = errors.New("the link to the host stayed down, so the wait cannot tell where the job stands")
	ErrJobState   = errors.New("the scheduler gave no final state for the job. Read the state of the job in the scheduler of the host")
)
