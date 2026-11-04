// Package slurm is the job half of the host contract for a Slurm cluster that beehave reaches over ssh.
// It runs sbatch, squeue, sacct, scancel, sinfo and scontrol on the head node through proc.Runner. It creates and deletes no machine:
// an adapter that owns a cluster, such as skypilot, builds a Queue over the ssh runner of that cluster and adds its own Up, Down and Status.
package slurm

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/jobwait"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

var (
	errJobFields = errors.New("a job needs a name and a command")
	errWorkRoot  = errors.New("the head node gave a login folder that is not one absolute path")
)

const (
	drainReason = "beehave"
	defaultPoll = 10 * time.Second
	timeoutExit = "124" // the exit status of `timeout` for a command that it stopped
)

// Slot is what the owner of the cluster does to keep the cluster busy while a job holds its work.
// Hold runs after the job is queued. Release runs when the work of the job is banked.
type Slot interface {
	Hold(ctx context.Context, job jobwait.Job) error
	Release(ctx context.Context, job jobwait.Job) error
}

// Queue is the Slurm queue on the head node of a cluster. The wait for a job is the jobwait engine over the Slurm reads.
type Queue struct {
	nonce  func() string
	link   *link.Link
	config model.HostConfig
	slot   Slot
	engine *jobwait.Engine
	limit  time.Duration // the run limit that sbatch gets; zero sends none
	bank   func(ctx context.Context) error
}

// settings are the production values that an Option replaces. They are for tests.
type settings struct {
	clock jobwait.Clock
	bank  func(ctx context.Context) error
	nonce func() string
	limit time.Duration
}

type Option func(*settings)

func WithClock(clock jobwait.Clock) Option { return func(s *settings) { s.clock = clock } }

func WithBank(bank func(context.Context) error) Option { return func(s *settings) { s.bank = bank } }

func WithNonce(nonce func() string) Option { return func(s *settings) { s.nonce = nonce } }

// New returns the queue of the cluster that the link reaches.
func New(l *link.Link, config model.HostConfig, slot Slot, opts ...Option) *Queue {
	q := &Queue{link: l, config: config, slot: slot}
	s := settings{clock: jobwait.Clock{Poll: defaultPoll, Now: time.Now, Sleep: proc.Sleep}, bank: q.bankWork, nonce: rand.Text}

	for _, opt := range opts {
		opt(&s)
	}

	q.nonce, q.limit, q.bank = s.nonce, s.limit, s.bank
	q.engine = jobwait.New(scheduler{l}, slot.Release, s.clock, s.bank, time.Duration(config.StaleMinutes)*time.Minute)

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
	out, err := q.read(ctx, "sinfo", "sinfo", "-h", "-N", "-o", "%T")
	if err != nil {
		return false, err
	}

	return parseDraining(out)
}

// read runs a command on the head node. A dropped ssh link wraps link.ErrDropped, so a caller tells a head node that does not answer from a command that fails.
func (q *Queue) read(ctx context.Context, name string, words ...string) (string, error) {
	result := q.link.Exec(ctx, words...)
	if err := q.link.Drop(result); err != nil {
		return "", err
	}

	if result.ExitCode != 0 {
		return "", link.Failure(name, result)
	}

	return result.Stdout, nil
}

func (q *Queue) freeBytes(ctx context.Context) (uint64, error) {
	out, err := q.read(ctx, "df", "df", "-P", "-k", ".")
	if err != nil {
		return 0, err
	}

	return host.ParseFreeBytes(out)
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
func (q *Queue) queue(ctx context.Context, spec model.JobSpec) (jobwait.Job, error) {
	words := []string{"sbatch", "--parsable", "--job-name=" + spec.Name}
	if q.limit > 0 {
		words = append(words, "--time="+strconv.FormatInt(int64(math.Ceil(q.limit.Minutes())), 10))
	}

	out, err := q.link.Remote(ctx, "sbatch", append(words, "--wrap="+spec.Command)...)
	if err != nil {
		return jobwait.Job{}, err
	}

	id, err := parseSubmitted(out)
	if err != nil {
		return jobwait.Job{}, err
	}

	return jobwait.Job{ID: id, Nonce: q.nonce()}, nil
}

// hold asks the slot to hold the cluster. If it cannot, the Slurm job is canceled.
func (q *Queue) hold(ctx context.Context, job jobwait.Job) error {
	err := q.slot.Hold(ctx, job)
	if err == nil {
		return nil
	}

	if _, cancelErr := q.link.Remote(ctx, "scancel", "scancel", job.ID); cancelErr != nil {
		err = errors.Join(err, cancelErr)
	}

	return err
}

// Wait polls Slurm until the job has a final state. It reads squeue while the controller knows the job and sacct afterwards.
// A state that beehave does not know is an error, never a guess.
//
// A job holds its slot until its work is banked, for at most max_run. Wait banks the work in every repository under the work root
// (WorkRoot, to host.private_remote), then releases the slot of the job. A host with no repository has nothing to bank.
// Work that cannot be banked is an error, and Wait returns the state of the job together with that error.
// The job id holds the Slurm job id and the nonce of the slot, so any handle can wait for a job it is given, also after a restart.
// Wait refuses an id that Submit did not return.
func (q *Queue) Wait(ctx context.Context, id model.JobID) (model.JobState, error) {
	job, err := parseJobID(id)
	if err != nil {
		return model.JobUnknown, err
	}

	return q.engine.Wait(ctx, job)
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

	return q.engine.Cancel(ctx, job)
}

type scheduler struct{ link *link.Link }

// Observe reads the state, and the size of the log only for a running job.
func (s scheduler) Observe(ctx context.Context, job jobwait.Job) (model.JobState, int64, error) {
	state, err := s.state(ctx, job)
	if err != nil || state != model.JobRunning {
		return state, -1, err
	}

	size, err := s.logSize(ctx, job)

	return state, size, err
}

// state reads squeue while the controller knows the job and sacct afterwards.
func (s scheduler) state(ctx context.Context, job jobwait.Job) (model.JobState, error) {
	result := s.link.Exec(ctx, "squeue", "-h", "-j", job.ID, "-o", "%T")
	if err := s.link.Drop(result); err != nil {
		return model.JobUnknown, err
	}

	if result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
		return parseJobState(result.Stdout)
	}

	return s.recorded(ctx, job.ID)
}

// recorded reads the state of a job from sacct, which keeps it after the controller forgets the job.
func (s scheduler) recorded(ctx context.Context, id string) (model.JobState, error) {
	result := s.link.Exec(ctx, "sacct", "-X", "-n", "-P", "-j", id, "-o", "State,ExitCode")
	if err := s.link.Drop(result); err != nil {
		return model.JobUnknown, err
	}

	switch {
	case result.ExitCode != 0:
		return model.JobUnknown, link.Failure("sacct", result)
	case strings.TrimSpace(result.Stdout) == "":
		// Neither squeue nor sacct holds the job: nothing runs it and no result exists.
		return model.JobDied, nil
	}

	return parseJobState(result.Stdout)
}

// logSize is -1 for a log that has no size yet, which counts as no growth.
func (s scheduler) logSize(ctx context.Context, job jobwait.Job) (int64, error) {
	result := s.link.Exec(ctx, "stat", "-c", "%s", "slurm-"+job.ID+".out")
	if err := s.link.Drop(result); err != nil {
		return -1, err
	}

	size, err := strconv.ParseInt(strings.TrimSpace(result.Stdout), 10, 64)
	if result.ExitCode != 0 || err != nil {
		return -1, nil
	}

	return size, nil
}

func (s scheduler) Stop(ctx context.Context, job jobwait.Job) error {
	_, err := s.link.Remote(ctx, "scancel", "scancel", job.ID)

	return err
}

// LostAdvice is what a person does about a job whose wait ended as lost. The job may still run, so a second run needs a cancel of the first one.
func (scheduler) LostAdvice(job jobwait.Job) string {
	return fmt.Sprintf("job %s may still run on the host. Check it with `squeue` on the host and cancel it there with `scancel` and the job number (the digits before the dash in the job id). Run it again only after the cancel", job)
}

func (scheduler) StuckAdvice(job jobwait.Job) string {
	return fmt.Sprintf("Check it with `squeue` on the host and cancel it again with `scancel %s`", job.ID)
}

// WorkRoot is the absolute path of the login folder of the head node. Wait and host.Teardown scan it for repositories.
func (q *Queue) WorkRoot(ctx context.Context) (string, error) {
	out, err := q.link.Remote(ctx, "pwd", "pwd", "-P")
	if err != nil {
		return "", err
	}

	lines := model.NonEmptyLines(out)
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

	return host.BankAll(ctx, q.link.Runner("git"), q.link.Runner("find"), root, q.config.PrivateRemote, host.WithRepoBudget(q.config.BankBudget))
}

// logTailBytes is the most bytes that Log reads from the end of the log of a job.
const logTailBytes = 16384

// Log returns the end of the log of a job, at most logTailBytes bytes.
func (q *Queue) Log(ctx context.Context, id model.JobID) (string, error) {
	job, err := parseJobID(id)
	if err != nil {
		return "", err
	}

	return q.link.Remote(ctx, "tail", "tail", "-c", strconv.Itoa(logTailBytes), "slurm-"+job.ID+".out")
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
	out, err := q.link.Remote(ctx, "sinfo", "sinfo", "-h", "-N", "-o", "%N")
	if err != nil {
		return err
	}

	nodes, err := parseNodes(out)
	if err != nil {
		return err
	}

	for _, node := range nodes {
		words := append([]string{"scontrol", "update", "NodeName=" + node, "State=" + state}, extra...)
		if _, err = q.link.Remote(ctx, "scontrol", words...); err != nil {
			return err
		}
	}

	return nil
}
