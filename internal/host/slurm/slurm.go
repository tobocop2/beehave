// Package slurm is the job half of the host contract for a Slurm cluster that obee reaches over ssh.
// It runs sbatch, squeue, sacct, scancel, sinfo and scontrol on the head node through proc.Runner. It creates and deletes no machine:
// an adapter that owns a cluster, such as skypilot, builds a Queue over the ssh runner of that cluster and adds its own Up, Down and Status.
package slurm

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

// ErrCommand is returned for a command on the head node that exits with a failure.
var ErrCommand = errors.New("command failed")

var (
	errLink        = errors.New("the ssh link to the host dropped")
	errJobFields   = errors.New("a job needs a name and a command")
	errCancelStuck = errors.New("the cancel did not take")
	errWorkRoot    = errors.New("the head node gave a login folder that is not one absolute path")
)

const (
	sshFailed    = 255
	drainReason  = "obee"
	defaultPoll  = 10 * time.Second
	defaultStale = 20 * time.Minute
	lostAfter    = 5 * time.Minute // the time without an answered read before a wait ends as lost
	stopLimit    = 5 * time.Minute // the time a job may take to end after the cancel
	timeoutExit  = "124"           // the exit status of `timeout` for a command that it stopped
	diedGrace    = time.Minute     // the time an empty record repeats before it reads as died: the accounting record can come late
)

// Slot is what the owner of the cluster does to keep the cluster busy while a job holds its work.
// Hold runs after the job is queued. Release runs when the work of the job is banked.
type Slot interface {
	Hold(ctx context.Context, job Job) error
	Release(ctx context.Context, job Job) error
}

// Queue is the Slurm queue on the head node of a cluster.
//
// Poll, Now, Sleep, Bank and Nonce are the time between two polls, the clock, the pause, the bank of the work of the host and the nonce source.
// New sets each to its production value; a test replaces them.
type Queue struct {
	Poll  time.Duration
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	Bank  func(ctx context.Context) error
	Nonce func() string

	target string
	config model.HostConfig
	ssh    proc.Runner
	slot   Slot
}

// New returns the queue of the cluster that ssh reaches. The first argument of an ssh call is target: the name of the head node in the ssh config.
func New(target string, config model.HostConfig, ssh proc.Runner, slot Slot) *Queue {
	q := &Queue{Poll: defaultPoll, Now: time.Now, Sleep: proc.Sleep, Nonce: rand.Text, target: target, config: config, ssh: ssh, slot: slot}
	q.Bank = q.bankWork

	return q
}

// Readings asks the head node whether Slurm drains and how much disk is free. The host is up.
func (q *Queue) Readings(ctx context.Context) (model.HostStatus, error) {
	draining, err := q.draining(ctx)
	if err != nil {
		return model.HostStatus{}, err
	}

	free, err := q.freeBytes(ctx)
	if err != nil {
		return model.HostStatus{}, err
	}

	state := model.HostUp
	if draining {
		state = model.HostDraining
	}

	return model.HostStatus{State: state, Disk: model.DiskReading{FreeBytes: free, Readable: true}}, nil
}

func (q *Queue) draining(ctx context.Context) (bool, error) {
	out, err := q.Remote(ctx, "sinfo", "sinfo", "-h", "-N", "-o", "%T")
	if err != nil {
		return false, err
	}

	return parseDraining(out)
}

func (q *Queue) freeBytes(ctx context.Context) (uint64, error) {
	out, err := q.Remote(ctx, "df", "df", "-P", "-k", ".")
	if err != nil {
		return 0, err
	}

	return parseFreeBytes(out)
}

// Submit queues the command in Slurm and returns the job id. Then it asks the slot to hold the cluster for the job.
// If the slot cannot hold, Submit cancels the Slurm job.
func (q *Queue) Submit(ctx context.Context, spec model.JobSpec) (model.JobID, error) {
	if spec.Name == "" || spec.Command == "" {
		return "", errJobFields
	}

	job, err := q.queue(ctx, spec)
	if err != nil {
		return "", err
	}

	if err = q.hold(ctx, job); err != nil {
		return "", err
	}

	return model.JobID(job.String()), nil
}

// queue runs sbatch and returns the handle of the new job, with a nonce made for this Submit.
func (q *Queue) queue(ctx context.Context, spec model.JobSpec) (Job, error) {
	out, err := q.Remote(ctx, "sbatch", "sbatch", "--parsable", "--job-name="+spec.Name, "--wrap="+spec.Command)
	if err != nil {
		return Job{}, err
	}

	id, err := parseSubmitted(out)
	if err != nil {
		return Job{}, err
	}

	return Job{ID: id, Nonce: q.Nonce()}, nil
}

// hold asks the slot to hold the cluster. If it cannot, the Slurm job is canceled.
func (q *Queue) hold(ctx context.Context, job Job) error {
	err := q.slot.Hold(ctx, job)
	if err == nil {
		return nil
	}

	if _, cancelErr := q.Remote(ctx, "scancel", "scancel", job.ID); cancelErr != nil {
		err = errors.Join(err, cancelErr)
	}

	return err
}

// Wait polls Slurm until the job has a final state. It reads squeue while the controller knows the job and sacct afterwards.
// A state that obee does not know is an error, never a guess.
//
// A job holds its slot until its work is banked, for at most max_run. Wait banks the work in every repository under the work root
// (WorkRoot, to host.private_remote), then releases the slot of the job. A host with no repository has nothing to bank.
// Work that cannot be banked is an error, and Wait returns the state of the job together with that error.
// The job id holds the Slurm job id and the nonce of the slot, so any handle can wait for a job it is given, also after a restart.
// Wait refuses an id that Submit did not return.
func (q *Queue) Wait(ctx context.Context, id model.JobID) (model.JobState, error) {
	return q.waitFor(ctx, id, false)
}

// waitFor is Wait. A stop wait follows a cancel: it has no stale check, as the stale stop would call itself, and it ends with an error
// when the job is not final after stopLimit on the clock of the host.
func (q *Queue) waitFor(ctx context.Context, id model.JobID, stopping bool) (model.JobState, error) {
	job, err := parseJobID(id)
	if err != nil {
		return model.JobUnknown, err
	}

	w := waiting{grew: q.Now(), answered: q.Now(), size: -1}
	if stopping {
		w.stopBy = q.Now().Add(stopLimit)
	}

	for {
		if end, over := q.pollOnce(ctx, job, &w); over {
			return end.state, end.err
		}

		if err := q.Sleep(ctx, q.Poll); err != nil {
			return model.JobUnknown, fmt.Errorf("wait for job %s: %w", id, err)
		}
	}
}

// ending is what Wait returns when the wait is over.
type ending struct {
	state model.JobState
	err   error
}

// pollOnce reads the job once and says whether the wait is over, with what Wait returns when it is.
func (q *Queue) pollOnce(ctx context.Context, job Job, w *waiting) (ending, bool) {
	state, err := q.observe(ctx, job, w)

	switch {
	case errors.Is(err, errLink) && q.Now().Sub(w.answered) >= lostAfter:
		return ending{model.JobLost, fmt.Errorf("job %s: no read of the job answered for %s (%w): %w. %s", job, lostAfter, err, host.ErrLost, model.JobID(job.String()).LostAdvice())}, true
	case err != nil && !errors.Is(err, errLink):
		return ending{model.JobUnknown, err}, true
	case err == nil && state.Terminal():
		return endOf(q.banked(ctx, job, state)), true
	case err == nil:
		return q.unfinished(ctx, job, w)
	}

	return ending{}, false
}

// unfinished says whether the wait is over for a job that is not final. A stop wait ends at its limit; any other wait ends when the log is stale.
func (q *Queue) unfinished(ctx context.Context, job Job, w *waiting) (ending, bool) {
	switch {
	case !w.stopBy.IsZero() && !q.Now().Before(w.stopBy):
		return ending{model.JobUnknown, fmt.Errorf("%w: job %s was not final %s after the cancel. Check it with `squeue` on the host and cancel it again with `scancel %s`", errCancelStuck, job, stopLimit, job.ID)}, true
	case w.stopBy.IsZero() && q.Now().Sub(w.grew) >= q.staleAfter():
		return endOf(q.stale(ctx, job)), true
	}

	return ending{}, false
}

func endOf(state model.JobState, err error) ending { return ending{state, err} }

// waiting is what one Wait remembers between its polls: the time of the last answered read, the time an empty record was first read,
// and the last size of the log with the time it last grew.
type waiting struct {
	answered time.Time
	empty    time.Time
	size     int64
	grew     time.Time
	stopBy   time.Time // the end of a stop wait; zero for a wait with no stop
}

// observe reads the state of the job once. A read that the ssh link drops returns errLink; a read that answers restarts the lost clock.
// While the job runs it also reads the size of the log, which restarts the stale clock when the log grew.
// A job that runs counts as answered only when its log read answers too, so a link that drops every log read ends as lost.
func (q *Queue) observe(ctx context.Context, job Job, w *waiting) (model.JobState, error) {
	state, err := q.jobState(ctx, job.ID)
	if errors.Is(err, errLink) {
		return model.JobUnknown, err
	}

	state = q.afterGrace(state, w)

	if err != nil || state != model.JobRunning {
		w.answered, w.grew = q.Now(), q.Now()

		return state, err
	}

	if err = q.readLog(ctx, job.ID, w); err != nil {
		return state, err
	}

	w.answered = q.Now()

	return state, nil
}

// afterGrace turns an empty record into died only after it repeats for diedGrace. Until then the job reads as pending, as the accounting record can come late.
// Any other state clears the empty read.
func (q *Queue) afterGrace(state model.JobState, w *waiting) model.JobState {
	if state != model.JobDied {
		w.empty = time.Time{}

		return state
	}

	if w.empty.IsZero() {
		w.empty = q.Now()
	}

	if q.Now().Sub(w.empty) < diedGrace {
		return model.JobPending
	}

	return model.JobDied
}

// readLog reads the size of the log of the job. A log that has no size yet counts as no growth.
func (q *Queue) readLog(ctx context.Context, id string, w *waiting) error {
	result := q.ssh.Run(ctx, q.sshArgs("stat", "-c", "%s", "slurm-"+id+".out"))
	if result.ExitCode == sshFailed {
		return q.linkError(result)
	}

	size, err := strconv.ParseInt(strings.TrimSpace(result.Stdout), 10, 64)
	if result.ExitCode == 0 && err == nil && size != w.size {
		w.size, w.grew = size, q.Now()
	}

	return nil
}

// staleAfter is the time a running job may keep its log still.
func (q *Queue) staleAfter() time.Duration {
	if q.config.StaleMinutes > 0 {
		return time.Duration(q.config.StaleMinutes) * time.Minute
	}

	return defaultStale
}

// stale stops a job whose log stopped growing, as Cancel does: it waits for the final state of the job, then banks its work and releases its slot.
// The state is stale, with the error of a stop that failed.
func (q *Queue) stale(ctx context.Context, job Job) (model.JobState, error) {
	if err := q.Cancel(ctx, model.JobID(job.String())); err != nil {
		return model.JobStale, fmt.Errorf("job %s is stale and its stop did not finish, so it may still run: %w", job.ID, err)
	}

	return model.JobStale, nil
}

// banked banks the work of the host, releases the slot of the job, and returns the final state. The state comes back with every error.
func (q *Queue) banked(ctx context.Context, job Job, state model.JobState) (model.JobState, error) {
	id := job.ID

	if err := q.Bank(ctx); err != nil && !errors.Is(err, host.ErrNoRepos) {
		return state, fmt.Errorf("job %s is %s but the host holds work that is not banked: %w", id, state, err)
	}

	if err := q.slot.Release(ctx, job); err != nil {
		return state, fmt.Errorf("job %s is %s and its work is banked, but the slot of the job cannot end: %w", id, state, err)
	}

	return state, nil
}

// WorkRoot is the absolute path of the login folder of the head node. Wait and host.Teardown scan it for repositories.
func (q *Queue) WorkRoot(ctx context.Context) (string, error) {
	out, err := q.Remote(ctx, "pwd", "pwd", "-P")
	if err != nil {
		return "", err
	}

	lines := nonEmptyLines(out)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "/") {
		return "", fmt.Errorf("%w: %q", errWorkRoot, model.FirstLine(out))
	}

	return lines[0], nil
}

// bankWork banks the work of the head node. git and find run on the head node through ssh, from the work root.
func (q *Queue) bankWork(ctx context.Context) error {
	root, err := q.WorkRoot(ctx)
	if err != nil {
		return err
	}

	return host.BankAll(ctx, q.Runner("git"), q.Runner("find"), root, q.config.PrivateRemote)
}

// Runner is the command line of a binary on the head node. host.Teardown takes git and find as runners of this kind.
func (q *Queue) Runner(binary string) proc.Runner { //nolint:ireturn // host.Teardown takes the runner contract
	return remoteRunner{q: q, binary: binary}
}

type remoteRunner struct {
	q      *Queue
	binary string
}

func (r remoteRunner) Run(ctx context.Context, args []string) model.CommandResult {
	return r.q.ssh.Run(ctx, r.q.sshArgs(append([]string{r.binary}, args...)...))
}

func (q *Queue) jobState(ctx context.Context, id string) (model.JobState, error) {
	result := q.ssh.Run(ctx, q.sshArgs("squeue", "-h", "-j", id, "-o", "%T"))
	if result.ExitCode == sshFailed {
		return model.JobUnknown, q.linkError(result)
	}

	if result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
		return parseJobState(result.Stdout)
	}

	return q.recorded(ctx, id)
}

// recorded reads the state of a job from sacct, which keeps it after the controller forgets the job.
func (q *Queue) recorded(ctx context.Context, id string) (model.JobState, error) {
	result := q.ssh.Run(ctx, q.sshArgs("sacct", "-X", "-n", "-P", "-j", id, "-o", "State,ExitCode"))

	switch {
	case result.ExitCode == sshFailed:
		return model.JobUnknown, q.linkError(result)
	case result.ExitCode != 0:
		return model.JobUnknown, fmt.Errorf("%w: sacct: exit %d: %s", ErrCommand, result.ExitCode, strings.TrimSpace(result.Stderr))
	case strings.TrimSpace(result.Stdout) == "":
		// Neither squeue nor sacct holds the job: nothing runs it and no result exists.
		return model.JobDied, nil
	}

	return parseJobState(result.Stdout)
}

func (q *Queue) linkError(result model.CommandResult) error {
	return fmt.Errorf("%w: ssh to %s: %s", errLink, q.target, strings.TrimSpace(result.Stderr))
}

// Cancel stops the job, waits until Slurm reports it final, banks the work of the host and releases the slot, as Wait does.
// scancel only signals the job, so the bank waits for the job to stop writing. When scancel fails, the job may still run,
// so the slot keeps the cluster busy (at most max_run) and Cancel returns the error.
// Any handle can cancel a job it is given: the job id holds the nonce of the slot. Cancel refuses an id that Submit did not return.
func (q *Queue) Cancel(ctx context.Context, id model.JobID) error {
	job, err := parseJobID(id)
	if err != nil {
		return err
	}

	if _, err = q.Remote(ctx, "scancel", "scancel", job.ID); err != nil {
		return err
	}

	_, err = q.waitFor(ctx, id, true)

	return err
}

// logTailBytes is the most bytes that Log reads from the end of the log of a job.
const logTailBytes = 16384

// Log returns the end of the log of a job, at most logTailBytes bytes.
func (q *Queue) Log(ctx context.Context, id model.JobID) (string, error) {
	job, err := parseJobID(id)
	if err != nil {
		return "", err
	}

	return q.Remote(ctx, "tail", "tail", "-c", strconv.Itoa(logTailBytes), "slurm-"+job.ID+".out")
}

// Drain makes every node refuse new jobs. Running jobs finish.
func (q *Queue) Drain(ctx context.Context) error {
	return q.setNodes(ctx, "DRAIN", "Reason="+drainReason)
}

// Resume makes every node take jobs again.
func (q *Queue) Resume(ctx context.Context) error {
	return q.setNodes(ctx, "RESUME")
}

func (q *Queue) setNodes(ctx context.Context, state string, extra ...string) error {
	out, err := q.Remote(ctx, "sinfo", "sinfo", "-h", "-N", "-o", "%N")
	if err != nil {
		return err
	}

	nodes, err := parseNodes(out)
	if err != nil {
		return err
	}

	for _, node := range nodes {
		words := append([]string{"scontrol", "update", "NodeName=" + node, "State=" + state}, extra...)
		if _, err = q.Remote(ctx, "scontrol", words...); err != nil {
			return err
		}
	}

	return nil
}

// Remote runs a command on the head node and returns what it printed. A failure wraps ErrCommand. name is the word that the error shows.
func (q *Queue) Remote(ctx context.Context, name string, words ...string) (string, error) {
	result := q.ssh.Run(ctx, q.sshArgs(words...))
	if result.ExitCode != 0 {
		return "", fmt.Errorf("%w: %s: exit %d: %s", ErrCommand, name, result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	return result.Stdout, nil
}

// sshArgs returns the ssh arguments for a command. ssh joins the words with spaces and sends them to the shell of the host, so each word is quoted.
func (q *Queue) sshArgs(words ...string) []string {
	args := []string{"-o", "BatchMode=yes", q.target}

	for _, word := range words {
		args = append(args, model.ShellQuote(word))
	}

	return args
}
