// Package skypilot is the host provider that creates a machine with the sky command and runs jobs on it through Slurm over ssh.
// It runs the sky, ssh and Slurm command lines through proc.Runner. It links no cloud library and opens no listener of its own.
// The SkyPilot CLI starts its own local API server; only a project that selects this provider gets it.
package skypilot

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

// ErrConfig is returned by New for a cluster name or a setting that the sky command line cannot take.
var ErrConfig = errors.New("invalid SkyPilot host setting")

var (
	errCommand     = errors.New("command failed")
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
	diedGrace    = time.Minute     // the time an empty record repeats before it reads as died: the accounting record can come late
	runLimitKey  = "gcp.managed_instance_group.run_duration="
	safeWordRune = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"
)

var wordPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Host is a SkyPilot cluster with Slurm on its head node.
type Host struct {
	cluster string
	config  model.HostConfig
	sky     proc.Runner
	ssh     proc.Runner
	poll    time.Duration
	now     func() time.Time                           // the clock of a wait; a test replaces it
	sleep   func(context.Context, time.Duration) error // the pause between two polls; a test replaces it
	bank    func(ctx context.Context) error
	nonce   func() string
}

// New returns a host for the cluster. sky is the sky command line. ssh is the ssh command line: the first argument of a call is a cluster name, as SkyPilot writes one ssh config entry for each cluster.
// Up is the first call and Down the last. A setting that cannot reach the sky command line safely is refused.
func New(cluster string, config model.HostConfig, sky, ssh proc.Runner) (*Host, error) {
	if err := checkConfig(cluster, config); err != nil {
		return nil, err
	}

	h := &Host{cluster: cluster, config: config, sky: sky, ssh: ssh, poll: defaultPoll, nonce: rand.Text, now: time.Now, sleep: sleepFor}
	h.bank = h.bankWork

	return h, nil
}

func checkConfig(cluster string, config model.HostConfig) error {
	settings := map[string]string{
		"cluster name": cluster, "host.skypilot.cloud": config.SkyPilot.Cloud,
		"host.skypilot.region": config.SkyPilot.Region, "host.skypilot.instance_type": config.SkyPilot.InstanceType,
	}

	for name, value := range settings {
		if value != "" && !wordPattern.MatchString(value) {
			return fmt.Errorf("%w: %s %q must be letters, digits, dots, dashes and underscores", ErrConfig, name, value)
		}
	}

	return checkRequired(cluster, config)
}

func checkRequired(cluster string, config model.HostConfig) error {
	switch {
	case cluster == "":
		return fmt.Errorf("%w: the project names no cluster in `build_host`", ErrConfig)
	case config.SkyPilot.Region != "" && config.SkyPilot.Cloud == "":
		return fmt.Errorf("%w: `host.skypilot.region` needs `host.skypilot.cloud`", ErrConfig)
	case config.IdleMinutes <= 0:
		return fmt.Errorf("%w: `host.idle_minutes` must be above zero", ErrConfig)
	case config.MaxRun < time.Second:
		return fmt.Errorf("%w: `host.max_run` must be at least one second", ErrConfig)
	}

	return nil
}

// Up creates the cluster. The cluster deletes itself after idle_minutes without a job. The run limit goes in as the SkyPilot setting for the lifetime of an instance.
// SkyPilot documents that setting for GPU requests only, so a CPU host may ignore it. The approved GCP trial checks that the instance carries the limit.
// Up never passes `--wait-for none`: the default mode counts jobs, and the slot job of Submit relies on it.
func (h *Host) Up(ctx context.Context) error {
	args := []string{
		"launch", "-y", "-c", h.cluster, "--down",
		"-i", strconv.Itoa(h.config.IdleMinutes),
		"--config", runLimitKey + strconv.FormatInt(int64(h.config.MaxRun/time.Second), 10),
	}

	if infra := h.infra(); infra != "" {
		args = append(args, "--infra", infra)
	}

	if h.config.SkyPilot.InstanceType != "" {
		args = append(args, "-t", h.config.SkyPilot.InstanceType)
	}

	_, err := run(ctx, h.sky, "sky launch", args)

	return err
}

func (h *Host) infra() string {
	cloud, region := h.config.SkyPilot.Cloud, h.config.SkyPilot.Region
	if region == "" {
		return cloud
	}

	return cloud + "/" + region
}

// Status reports whether the cluster exists, whether Slurm drains its nodes, and its free disk space.
// A cluster that is in the list but not up, and any answer that obee does not recognize, is an error.
func (h *Host) Status(ctx context.Context) (model.HostStatus, error) {
	out, err := run(ctx, h.sky, "sky status", []string{"status", "-r", "-o", "json"})
	if err != nil {
		return model.HostStatus{}, err
	}

	exists, err := parseCluster(out, h.cluster)
	if err != nil {
		return model.HostStatus{}, err
	}

	if !exists {
		return model.HostStatus{State: model.HostAbsent}, nil
	}

	return h.readings(ctx)
}

// readings asks the head node of a cluster that is up whether Slurm drains and how much disk is free.
func (h *Host) readings(ctx context.Context) (model.HostStatus, error) {
	draining, err := h.draining(ctx)
	if err != nil {
		return model.HostStatus{}, err
	}

	free, err := h.freeBytes(ctx)
	if err != nil {
		return model.HostStatus{}, err
	}

	state := model.HostUp
	if draining {
		state = model.HostDraining
	}

	return model.HostStatus{State: state, Disk: model.DiskReading{FreeBytes: free, Readable: true}}, nil
}

func (h *Host) draining(ctx context.Context) (bool, error) {
	out, err := h.remote(ctx, "sinfo", "sinfo", "-h", "-N", "-o", "%T")
	if err != nil {
		return false, err
	}

	return parseDraining(out)
}

func (h *Host) freeBytes(ctx context.Context) (uint64, error) {
	out, err := h.remote(ctx, "df", "df", "-P", "-k", ".")
	if err != nil {
		return 0, err
	}

	return parseFreeBytes(out)
}

// Down deletes the cluster. A caller stops a host with host.Teardown, which banks the work first.
func (h *Host) Down(ctx context.Context) error {
	_, err := run(ctx, h.sky, "sky down", []string{"down", "-y", h.cluster})

	return err
}

// Submit queues the command in Slurm and returns the job id.
//
// SkyPilot counts only its own job queue as work, so Submit also starts a SkyPilot job (`sky exec -d`) that holds the slot of the
// cluster until Wait banks the work of the job, for at most max_run. The job waits for the bank marker that Wait writes.
// If the slot job cannot start, Submit cancels the Slurm job.
func (h *Host) Submit(ctx context.Context, spec model.JobSpec) (model.JobID, error) {
	if spec.Name == "" || spec.Command == "" {
		return "", errJobFields
	}

	job, err := h.queue(ctx, spec)
	if err != nil {
		return "", err
	}

	if err = h.holdSlot(ctx, job); err != nil {
		return "", err
	}

	return model.JobID(job.String()), nil
}

// queue runs sbatch and returns the handle of the new job, with a nonce made for this Submit.
func (h *Host) queue(ctx context.Context, spec model.JobSpec) (jobHandle, error) {
	out, err := h.remote(ctx, "sbatch", "sbatch", "--parsable", "--job-name="+spec.Name, "--wrap="+spec.Command)
	if err != nil {
		return jobHandle{}, err
	}

	slurm, err := parseSubmitted(out)
	if err != nil {
		return jobHandle{}, err
	}

	return jobHandle{slurm: slurm, nonce: h.nonce()}, nil
}

// holdSlot starts the SkyPilot job that keeps the cluster busy. If the job cannot start, the Slurm job is canceled.
func (h *Host) holdSlot(ctx context.Context, job jobHandle) error {
	_, err := run(ctx, h.sky, "sky exec", []string{"exec", h.cluster, "-d", slotCommand(h.config.MaxRun, h.markerName(job))})
	if err == nil {
		return nil
	}

	if _, cancelErr := h.remote(ctx, "scancel", "scancel", job.slurm); cancelErr != nil {
		err = errors.Join(err, cancelErr)
	}

	return err
}

// markerName is the name of the file in the login folder that ends the slot job of a job. It holds the nonce made for the Submit,
// so a job id that Slurm uses again never matches the marker of an earlier job.
func (h *Host) markerName(job jobHandle) string {
	return ".obee-banked-" + h.cluster + "-" + job.String()
}

// slotCommand is the command of the SkyPilot job that keeps the cluster busy until the marker exists.
func slotCommand(maxRun time.Duration, marker string) string {
	return fmt.Sprintf("cd ~ && timeout %d sh -c 'until test -e %s; do sleep 5; done'", int64(maxRun/time.Second), marker)
}

// endSlot writes the marker that ends the slot job.
func (h *Host) endSlot(ctx context.Context, marker string) error {
	_, err := h.remote(ctx, "touch", "touch", marker)

	return err
}

// Wait polls Slurm until the job has a final state. It reads squeue while the controller knows the job and sacct afterwards.
// A state that obee does not know is an error, never a guess.
//
// A job holds its slot until its work is banked, for at most max_run. Wait banks the work in every repository under the work root
// (WorkRoot, to host.private_remote), then writes the bank marker that ends the slot job of Submit. A host with no repository has nothing to bank.
// Work that cannot be banked is an error, and Wait returns the state of the job together with that error.
// The job id holds the Slurm job id and the nonce of the slot job, so any handle can wait for a job it is given, also after a restart.
// Wait refuses an id that Submit did not return.
func (h *Host) Wait(ctx context.Context, id model.JobID) (model.JobState, error) {
	return h.waitFor(ctx, id, false)
}

// waitFor is Wait. A stop wait follows a cancel: it has no stale check, as the stale stop would call itself, and it ends with an error
// when the job is not final after stopLimit on the clock of the host.
func (h *Host) waitFor(ctx context.Context, id model.JobID, stopping bool) (model.JobState, error) {
	job, err := parseJobID(id)
	if err != nil {
		return model.JobUnknown, err
	}

	w := waiting{grew: h.now(), answered: h.now(), size: -1}
	if stopping {
		w.stopBy = h.now().Add(stopLimit)
	}

	for {
		if end, over := h.pollOnce(ctx, job, &w); over {
			return end.state, end.err
		}

		if err := h.sleep(ctx, h.poll); err != nil {
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
func (h *Host) pollOnce(ctx context.Context, job jobHandle, w *waiting) (ending, bool) {
	state, err := h.observe(ctx, job, w)

	switch {
	case errors.Is(err, errLink) && h.now().Sub(w.answered) >= lostAfter:
		return ending{model.JobLost, fmt.Errorf("job %s: no read of the job answered for %s (%w): %w. %s", job, lostAfter, err, host.ErrLost, model.JobID(job.String()).LostAdvice())}, true
	case err != nil && !errors.Is(err, errLink):
		return ending{model.JobUnknown, err}, true
	case err == nil && state.Terminal():
		return endOf(h.banked(ctx, job, state)), true
	case err == nil:
		return h.unfinished(ctx, job, w)
	}

	return ending{}, false
}

// unfinished says whether the wait is over for a job that is not final. A stop wait ends at its limit; any other wait ends when the log is stale.
func (h *Host) unfinished(ctx context.Context, job jobHandle, w *waiting) (ending, bool) {
	switch {
	case !w.stopBy.IsZero() && !h.now().Before(w.stopBy):
		return ending{model.JobUnknown, fmt.Errorf("%w: job %s was not final %s after the cancel. Check it with `squeue` on the host and cancel it again with `scancel %s`", errCancelStuck, job, stopLimit, job.slurm)}, true
	case w.stopBy.IsZero() && h.now().Sub(w.grew) >= h.staleAfter():
		return endOf(h.stale(ctx, job)), true
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
func (h *Host) observe(ctx context.Context, job jobHandle, w *waiting) (model.JobState, error) {
	state, err := h.jobState(ctx, job.slurm)
	if errors.Is(err, errLink) {
		return model.JobUnknown, err
	}

	state = h.afterGrace(state, w)

	if err != nil || state != model.JobRunning {
		w.answered, w.grew = h.now(), h.now()

		return state, err
	}

	if err = h.readLog(ctx, job.slurm, w); err != nil {
		return state, err
	}

	w.answered = h.now()

	return state, nil
}

// afterGrace turns an empty record into died only after it repeats for diedGrace. Until then the job reads as pending, as the accounting record can come late.
// Any other state clears the empty read.
func (h *Host) afterGrace(state model.JobState, w *waiting) model.JobState {
	if state != model.JobDied {
		w.empty = time.Time{}

		return state
	}

	if w.empty.IsZero() {
		w.empty = h.now()
	}

	if h.now().Sub(w.empty) < diedGrace {
		return model.JobPending
	}

	return model.JobDied
}

// readLog reads the size of the log of the job. A log that has no size yet counts as no growth.
func (h *Host) readLog(ctx context.Context, id string, w *waiting) error {
	result := h.ssh.Run(ctx, h.sshArgs("stat", "-c", "%s", "slurm-"+id+".out"))
	if result.ExitCode == sshFailed {
		return linkError(h.cluster, result)
	}

	size, err := strconv.ParseInt(strings.TrimSpace(result.Stdout), 10, 64)
	if result.ExitCode == 0 && err == nil && size != w.size {
		w.size, w.grew = size, h.now()
	}

	return nil
}

// staleAfter is the time a running job may keep its log still.
func (h *Host) staleAfter() time.Duration {
	if h.config.StaleMinutes > 0 {
		return time.Duration(h.config.StaleMinutes) * time.Minute
	}

	return defaultStale
}

// stale stops a job whose log stopped growing, as Cancel does: it waits for the final state of the job, then banks its work and ends its slot.
// The state is stale, with the error of a stop that failed.
func (h *Host) stale(ctx context.Context, job jobHandle) (model.JobState, error) {
	if err := h.Cancel(ctx, model.JobID(job.String())); err != nil {
		return model.JobStale, fmt.Errorf("job %s is stale and its stop did not finish, so it may still run: %w", job.slurm, err)
	}

	return model.JobStale, nil
}

// banked banks the work of the host, ends the slot job of the job, and returns the final state. The state comes back with every error.
func (h *Host) banked(ctx context.Context, job jobHandle, state model.JobState) (model.JobState, error) {
	id := job.slurm

	if err := h.bank(ctx); err != nil && !errors.Is(err, host.ErrNoRepos) {
		return state, fmt.Errorf("job %s is %s but the host holds work that is not banked: %w", id, state, err)
	}

	if err := h.endSlot(ctx, h.markerName(job)); err != nil {
		return state, fmt.Errorf("job %s is %s and its work is banked, but the slot of the job cannot end: %w", id, state, err)
	}

	return state, nil
}

// WorkRoot is the absolute path of the login folder of the head node. Wait and host.Teardown scan it for repositories.
func (h *Host) WorkRoot(ctx context.Context) (string, error) {
	out, err := h.remote(ctx, "pwd", "pwd", "-P")
	if err != nil {
		return "", err
	}

	lines := nonEmptyLines(out)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "/") {
		return "", fmt.Errorf("%w: %q", errWorkRoot, firstLine(out))
	}

	return lines[0], nil
}

// Teardown banks the work of the head node to `host.private_remote` and then deletes the cluster. It is host.Teardown with git and find on the head node.
// A head node with unbanked work, or one whose work cannot be read, stays up, and the error says why.
func (h *Host) Teardown(ctx context.Context) error {
	return host.Teardown(ctx, h, remoteRunner{h: h, binary: "git"}, remoteRunner{h: h, binary: "find"}, h.config.PrivateRemote)
}

// bankWork banks the work of the head node. git and find run on the head node through ssh, from the work root.
func (h *Host) bankWork(ctx context.Context) error {
	root, err := h.WorkRoot(ctx)
	if err != nil {
		return err
	}

	return host.BankAll(ctx, remoteRunner{h: h, binary: "git"}, remoteRunner{h: h, binary: "find"}, root, h.config.PrivateRemote)
}

// remoteRunner is the command line of a binary on the head node.
type remoteRunner struct {
	h      *Host
	binary string
}

func (r remoteRunner) Run(ctx context.Context, args []string) model.CommandResult {
	return r.h.ssh.Run(ctx, r.h.sshArgs(append([]string{r.binary}, args...)...))
}

// sleepFor sleeps for d, or returns the error of the context.
func sleepFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (h *Host) jobState(ctx context.Context, id string) (model.JobState, error) {
	result := h.ssh.Run(ctx, h.sshArgs("squeue", "-h", "-j", id, "-o", "%T"))
	if result.ExitCode == sshFailed {
		return model.JobUnknown, linkError(h.cluster, result)
	}

	if result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
		return parseJobState(result.Stdout)
	}

	return h.recorded(ctx, id)
}

// recorded reads the state of a job from sacct, which keeps it after the controller forgets the job.
func (h *Host) recorded(ctx context.Context, id string) (model.JobState, error) {
	result := h.ssh.Run(ctx, h.sshArgs("sacct", "-X", "-n", "-P", "-j", id, "-o", "State"))

	switch {
	case result.ExitCode == sshFailed:
		return model.JobUnknown, linkError(h.cluster, result)
	case result.ExitCode != 0:
		return model.JobUnknown, fmt.Errorf("%w: sacct: exit %d: %s", errCommand, result.ExitCode, strings.TrimSpace(result.Stderr))
	case strings.TrimSpace(result.Stdout) == "":
		// Neither squeue nor sacct holds the job: nothing runs it and no result exists.
		return model.JobDied, nil
	}

	return parseJobState(result.Stdout)
}

func linkError(cluster string, result model.CommandResult) error {
	return fmt.Errorf("%w: ssh to %s: %s", errLink, cluster, strings.TrimSpace(result.Stderr))
}

// Cancel stops the job, waits until Slurm reports it final, banks the work of the host and ends the slot job, as Wait does.
// scancel only signals the job, so the bank waits for the job to stop writing. When scancel fails, the job may still run,
// so the slot job keeps the cluster busy (at most max_run) and Cancel returns the error.
// Any handle can cancel a job it is given: the job id holds the nonce of the slot job. Cancel refuses an id that Submit did not return.
func (h *Host) Cancel(ctx context.Context, id model.JobID) error {
	job, err := parseJobID(id)
	if err != nil {
		return err
	}

	if _, err = h.remote(ctx, "scancel", "scancel", job.slurm); err != nil {
		return err
	}

	_, err = h.waitFor(ctx, id, true)

	return err
}

// logTailBytes is the most bytes that Log reads from the end of the log of a job.
const logTailBytes = 16384

// Log returns the end of the log of a job, at most logTailBytes bytes.
func (h *Host) Log(ctx context.Context, id model.JobID) (string, error) {
	job, err := parseJobID(id)
	if err != nil {
		return "", err
	}

	return h.remote(ctx, "tail", "tail", "-c", strconv.Itoa(logTailBytes), "slurm-"+job.slurm+".out")
}

// Drain makes every node refuse new jobs. Running jobs finish.
func (h *Host) Drain(ctx context.Context) error {
	return h.setNodes(ctx, "DRAIN", "Reason="+drainReason)
}

// Resume makes every node take jobs again.
func (h *Host) Resume(ctx context.Context) error {
	return h.setNodes(ctx, "RESUME")
}

func (h *Host) setNodes(ctx context.Context, state string, extra ...string) error {
	out, err := h.remote(ctx, "sinfo", "sinfo", "-h", "-N", "-o", "%N")
	if err != nil {
		return err
	}

	nodes, err := parseNodes(out)
	if err != nil {
		return err
	}

	for _, node := range nodes {
		words := append([]string{"scontrol", "update", "NodeName=" + node, "State=" + state}, extra...)
		if _, err = h.remote(ctx, "scontrol", words...); err != nil {
			return err
		}
	}

	return nil
}

// remote runs a command on the head node of the cluster and returns what it printed.
func (h *Host) remote(ctx context.Context, name string, words ...string) (string, error) {
	return run(ctx, h.ssh, name, h.sshArgs(words...))
}

// sshArgs returns the ssh arguments for a command. ssh joins the words with spaces and sends them to the shell of the host, so each word is quoted.
func (h *Host) sshArgs(words ...string) []string {
	args := []string{"-o", "BatchMode=yes", h.cluster}

	for _, word := range words {
		args = append(args, quote(word))
	}

	return args
}

func run(ctx context.Context, runner proc.Runner, name string, args []string) (string, error) {
	result := runner.Run(ctx, args)
	if result.ExitCode != 0 {
		return "", fmt.Errorf("%w: %s: exit %d: %s", errCommand, name, result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	return result.Stdout, nil
}

// quote returns the word as one word for a POSIX shell.
func quote(word string) string {
	if word != "" && strings.Trim(word, safeWordRune) == "" {
		return word
	}

	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}
