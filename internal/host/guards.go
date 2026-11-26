package host

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

const (
	// procLinks are the links in each /proc/<pid> folder that name a directory a process holds.
	procLinks     = "cwd exe fd/* task/*/cwd task/*/exe"
	bytesPerGB    = 1 << 30
	procRoot      = "/proc"
	procScanDepth = 4
	globMeta      = `\*?[`
)

// BankAll scans root on the host for repositories and banks the work in each one to private. See Bank.
// git and find are the command lines of git and find on the host. A scan that fails or finds nothing is a refusal.
func BankAll(ctx context.Context, git, find proc.Runner, root, private string, opts ...ScanOption) error {
	repos, err := FindRepos(ctx, find, root)
	if err != nil {
		return err
	}

	return Bank(ctx, git, repos, private, opts...)
}

// BankFolder banks the work in the repositories under one folder, the folder of a job, as BankAll does for the work root. probe is the command line of test on the host.
// A folder that does not exist holds no work, and the answer is ErrNoRepos, as for a root with no repository. A probe that fails any other way is a refusal.
func BankFolder(ctx context.Context, git, find, probe proc.Runner, folder, private string, opts ...ScanOption) error {
	switch result := probe.Run(ctx, []string{"-d", folder}); result.ExitCode {
	case 0:
		return BankAll(ctx, git, find, folder, private, opts...)
	case 1:
		return fmt.Errorf("%w: %s", ErrNoRepos, folder)
	default:
		return fmt.Errorf("check the folder %s: exit %d: %s", folder, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
}

// Teardown scans the host, banks its work and stops it. It is the only way to stop a host with the guards:
// a host with unbanked work, or one whose work cannot be read, stays up. A host with no repository stays up too: Teardown refuses zero repositories.
// It scans the root that Host.WorkRoot names, the root that Wait scans.
// After Down it audits the delete: it reads the status of the host again and returns ErrNotDeleted unless the host is absent.
func Teardown(ctx context.Context, h Host, git, find proc.Runner, private string, opts ...ScanOption) error {
	root, err := h.WorkRoot(ctx)
	if err != nil {
		return fmt.Errorf("host stays up: %w", err)
	}

	if err = BankAll(ctx, git, find, root, private, opts...); err != nil {
		return fmt.Errorf("host stays up: %w", err)
	}

	if err = h.Down(ctx); err != nil {
		return err
	}

	return auditGone(ctx, h)
}

// auditGone reads the status of the host after Down and accepts only an absent host. A status that fails, or a host that is still there, is ErrNotDeleted.
func auditGone(ctx context.Context, h Host) error {
	status, err := h.Status(ctx)
	if err != nil {
		return fmt.Errorf("read the host after the delete: %w: %w", err, ErrNotDeleted)
	}

	if status.State != model.HostAbsent {
		return fmt.Errorf("the host is %q after the delete: %w", status.State, ErrNotDeleted)
	}

	return nil
}

// DiskFloor refuses a new job when the host has less free space than floorGB, or when it cannot report its free space.
func DiskFloor(ctx context.Context, h Host, floorGB int) error {
	status, err := h.Status(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", err, ErrDiskRead)
	}

	return belowFloor(status.Disk, floorGB)
}

// belowFloor is nil when the reading is readable and holds at least floorGB of free space.
func belowFloor(disk model.DiskReading, floorGB int) error {
	if !disk.Readable {
		return ErrDiskRead
	}

	if free := disk.FreeBytes; free < uint64(floorGB)*bytesPerGB { //nolint:gosec // config keeps the floor between 1 and MaxDiskFloorGB, so the product cannot wrap
		return fmt.Errorf("%d GB free, floor %d GB: %w", free/bytesPerGB, floorGB, ErrDiskLow)
	}

	return nil
}

// readier is a Host that says whether it takes a new job now. The wrapper of the disk floor is one.
type readier interface {
	Ready(ctx context.Context) (bool, error)
}

// Ready says whether h takes a new job: it is up, and the wrapper of the disk floor, when h has one, finds enough free space.
// A host that cannot be read gives the error of the read. A host that is absent, draining or under its floor gives false and no error.
func Ready(ctx context.Context, h Host) (bool, error) {
	if r, ok := h.(readier); ok {
		return r.Ready(ctx)
	}

	status, err := h.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("read the host: %w", err)
	}

	return status.State == model.HostUp, nil
}

// Ready reads the status once: the host is ready when it is up and its disk holds the floor.
func (f floored) Ready(ctx context.Context) (bool, error) {
	status, err := f.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("read the host: %w", err)
	}

	return status.State == model.HostUp && belowFloor(status.Disk, f.floorGB) == nil, nil
}

// floored is a Host that checks the disk floor before it starts a job.
type floored struct {
	Host

	floorGB int
	wait    time.Duration
	poll    time.Duration
	fleet   *Fleet
}

// WithDiskFloor returns h with a Submit that first runs DiskFloor. A job does not start on a host under its floor, or on a host that cannot report its free space.
// Every other method of Host is the method of h, with the Wait of diskwatch.go. Of the methods outside Host, the wrapper keeps only Teardown, so a wrapped host has no Drain or Resume.
// It does not watch the disk while a job runs.
func WithDiskFloor(h Host, floorGB int) Host { //nolint:ireturn // the wrapper is a Host of any provider
	return keepTeardown(floored{Host: h, floorGB: floorGB, fleet: NewFleet()}, h)
}

// WithDiskWait is WithDiskFloor with a wait: a job under the floor reads the disk again every poll, for at most wait, and starts when the space is back.
// A job still under the floor when the wait ends is refused with ErrDiskLow. A reading that cannot be read is refused at once, because waiting does not fix it.
// A wait of zero or less does not wait before the start.
// While a job runs, the guard reads the disk every poll and stops the youngest job of the fleet when the host is under the floor; see Wait.
// Guards of one host share one fleet. A poll of zero or less does not watch the disk.
func WithDiskWait(h Host, floorGB int, wait, poll time.Duration, fleet *Fleet) Host { //nolint:ireturn // the wrapper is a Host of any provider
	return keepTeardown(floored{Host: h, floorGB: floorGB, wait: wait, poll: poll, fleet: fleet}, h)
}

// teardowner is the Teardown that a provider adds to the contract: it banks the work of the host and releases it.
type teardowner interface {
	Teardown(ctx context.Context) error
}

// flooredKeeper is a floored Host whose provider has a Teardown. The embedded interface hides a method that is not in Host,
// so the wrapper repeats it; a wrapped host with no Teardown stays without one.
type flooredKeeper struct {
	floored

	inner teardowner
}

func (k flooredKeeper) Teardown(ctx context.Context) error { return k.inner.Teardown(ctx) }

// keepTeardown returns the wrapper w, with the Teardown of h when h has one.
func keepTeardown(w floored, h Host) Host { //nolint:ireturn // the wrapper is a Host of any provider
	if t, ok := h.(teardowner); ok {
		return flooredKeeper{floored: w, inner: t}
	}

	return w
}

func (f floored) Submit(ctx context.Context, spec model.JobSpec) (model.JobID, error) {
	if err := f.waitForSpace(ctx); err != nil {
		return "", fmt.Errorf("job %s does not start: %w", spec.Name, err)
	}

	return f.Host.Submit(ctx, spec)
}

// waitForSpace runs DiskFloor and, while the host is under the floor, runs it again after each poll until the wait ends.
func (f floored) waitForSpace(ctx context.Context) error {
	return waitUntil(ctx, f.wait, f.poll, func(ctx context.Context) error { return DiskFloor(ctx, f.Host, f.floorGB) })
}

// waitUntil runs check and, while it answers ErrDiskLow, runs it again after each poll until the wait ends, and then returns that ErrDiskLow.
// Any other answer ends the wait at once, because waiting does not fix it. A wait of zero or less runs check once.
func waitUntil(ctx context.Context, wait, poll time.Duration, check func(context.Context) error) error {
	deadline := time.Now().Add(wait)

	for {
		err := check(ctx)
		if err == nil || !errors.Is(err, ErrDiskLow) || wait <= 0 || !time.Now().Before(deadline) {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // the cancel of the caller is the error
		case <-time.After(min(poll, time.Until(deadline))):
		}
	}
}

// EnsureUnused refuses when a process has dir as its working directory or holds a file open in it. sh is the command line of `sh` on the host.
// A scan that fails counts as in use, because a process of another user hides its directory from the scan.
//
// The scan covers the working directory, the executable and the open files of each process and of each of its threads.
// /proc shows resolved paths, so one call on the host resolves dir (`readlink -f`) and scans for both the cleaned and the resolved path.
// Nothing resolves on the machine of beehave.
func EnsureUnused(ctx context.Context, sh proc.Runner, dir string) error {
	result := sh.Run(ctx, unusedArgs(dir))
	if result.ExitCode != 0 {
		return fmt.Errorf("scan for users of %s: exit %d: %s: %w", dir, result.ExitCode, strings.TrimSpace(result.Stderr), ErrInUseRead)
	}

	if strings.TrimSpace(result.Stdout) != "" {
		return fmt.Errorf("%w %s: stop it before the directory is deleted", ErrInUse, dir)
	}

	return nil
}

// unusedArgs returns the shell arguments that resolve dir on the host and print a link in /proc that points at dir, at its resolved path, or below either.
// $1 is the cleaned path and $2 is the same path with the glob characters escaped. The script escapes the resolved path itself.
func unusedArgs(dir string) []string {
	var paths []string

	for _, suffix := range strings.Fields(procLinks) {
		paths = append(paths, "-path '"+procRoot+"/[0-9]*/"+suffix+"'")
	}

	script := `resolved=$(readlink -f -- "$1") || exit 2
resolved=$(printf '%s' "$resolved" | sed 's/[][\\*?]/\\&/g')
find ` + procRoot + ` -maxdepth ` + strconv.Itoa(procScanDepth) + ` \( ` + strings.Join(paths, " -o ") + ` \)` +
		` \( -lname "$2" -o -lname "$2/*" -o -lname "$resolved" -o -lname "$resolved/*" \) -print -quit`

	cleaned := filepath.Clean(dir)

	return []string{"-c", script, "beehave", cleaned, escapeGlob(cleaned)}
}

func escapeGlob(text string) string {
	var out strings.Builder

	for _, r := range text {
		if strings.ContainsRune(globMeta, r) {
			out.WriteByte('\\')
		}

		out.WriteRune(r)
	}

	return out.String()
}

// WaitJob waits for a job and returns its final state, which the host reads from its scheduler.
// A state that is not final, such as an unknown one, is a refusal: a log line never decides that a job ended.
func WaitJob(ctx context.Context, h Host, id model.JobID) (model.JobState, error) {
	state, err := h.Wait(ctx, id)
	if err != nil {
		return state, fmt.Errorf("wait for job %s: %w", id, err)
	}

	if !state.Terminal() {
		return model.JobUnknown, fmt.Errorf("job %s is %q: %w", id, state, ErrJobState)
	}

	return state, nil
}
