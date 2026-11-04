// Package ssh is the host provider for a machine that the owner provides and obee reaches over ssh. It creates and deletes nothing.
// A job runs as a transient systemd user unit, so the cgroup of the unit gives the kill of every process of the job, the run limit and the record of the result.
// The ssh command line is the link package; the wait for a job is the jobwait engine over the systemd reads.
package ssh

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/jobwait"
	"github.com/tobocop2/obee/internal/host/link"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

var (
	// ErrLinger is returned by Up for a user whose linger is off. A user manager that stops at logout takes every unit with it.
	ErrLinger = errors.New("linger is off for the login user, so the user manager and every job stop at logout")
	// ErrSystemd is returned by Up for a machine that cannot run a transient user unit with a log file.
	ErrSystemd = errors.New("the machine cannot run jobs as systemd user units")
	// ErrDraining is returned by Submit for a host that Drain marked.
	ErrDraining = errors.New("the host is draining and takes no new job. Run resume to take jobs again")

	errJobFields = errors.New("a job needs a name and a command")
	errWorkRoot  = errors.New("the machine gave a login folder that is not one absolute path")
)

const (
	defaultPoll    = 10 * time.Second
	stopTimeout    = 30 // seconds that systemd waits for a unit after SIGTERM before SIGKILL
	stopNotLoaded  = 5  // the exit status of systemctl stop for a unit that is not loaded
	logTailBytes   = 16384
	stateFolder    = ".obee"
	drainMarker    = "drain"
	canceledSuffix = ".canceled"
	logSuffix      = ".log"
)

// Host is a machine with systemd that obee reaches over ssh.
//
// Up is the first call; it checks the machine and makes the state folder in the work root, and changes nothing else on the machine.
// Down sends nothing: obee did not create the machine and never deletes it. A caller releases the host with Teardown, which banks the work.
type Host struct {
	config model.HostConfig
	link   *link.Link
	nonce  func() string
	bank   func(ctx context.Context) error
	engine *jobwait.Engine

	mu   sync.Mutex
	root string // the work root, once read
}

// settings are the production values that an Option replaces. They are for tests.
type settings struct {
	clock jobwait.Clock
	bank  func(ctx context.Context) error
	nonce func() string
}

type Option func(*settings)

func WithClock(clock jobwait.Clock) Option { return func(s *settings) { s.clock = clock } }

func WithBank(bank func(context.Context) error) Option { return func(s *settings) { s.bank = bank } }

func WithNonce(nonce func() string) Option { return func(s *settings) { s.nonce = nonce } }

// New returns the host for the machine. target is the ssh alias, or a label when `host.ssh.address` names the machine. runner is the ssh command line.
// It runs nothing, so a check of the config needs no network. A setting that could change a command line is refused.
func New(target string, config model.HostConfig, ssh proc.Runner, opts ...Option) (*Host, error) {
	if err := checkConfig(config); err != nil {
		return nil, err
	}

	l, err := link.ForMachine(target, linkOptions(config.SSH), ssh)
	if err != nil {
		return nil, err
	}

	h := &Host{config: config, link: l}
	set := settings{clock: jobwait.Clock{Poll: defaultPoll, Now: time.Now, Sleep: proc.Sleep}, bank: h.bankWork, nonce: rand.Text}

	for _, opt := range opts {
		opt(&set)
	}

	h.nonce, h.bank = set.nonce, set.bank
	h.engine = jobwait.New(scheduler{h}, h.release, set.clock, set.bank, time.Duration(config.StaleMinutes)*time.Minute)

	return h, nil
}

func linkOptions(s model.SSHConfig) link.Options {
	return link.Options{Address: s.Address, User: s.User, Identity: s.Identity, KnownHosts: s.KnownHosts, Port: s.Port}
}

// checkConfig refuses a work root that systemd or the shell would change: systemd expands a percent sign in a path, and a dollar sign is no path.
func checkConfig(config model.HostConfig) error {
	root := config.SSH.WorkRoot

	switch {
	case config.MaxRun < time.Second:
		return fmt.Errorf("%w: `host.max_run` must be at least one second", link.ErrConfig)
	case root != "" && !path.IsAbs(root) && !strings.HasPrefix(root, "~/"):
		return fmt.Errorf("%w: `host.ssh.work_root` %q must be an absolute path or start with ~/", link.ErrConfig, root)
	case strings.ContainsAny(root, "%$"):
		return fmt.Errorf("%w: `host.ssh.work_root` %q must not hold a percent or dollar sign, which systemd and the shell would expand", link.ErrConfig, root)
	}

	return nil
}

// run runs one command on the machine and returns what it printed. A dropped link wraps link.ErrDropped. Any other failure wraps link.ErrCommand.
func (h *Host) run(ctx context.Context, name string, words ...string) (string, error) {
	result := h.link.Exec(ctx, words...)
	if err := h.link.Drop(result); err != nil {
		return "", err
	}

	if result.ExitCode != 0 {
		return "", link.Failure(name, result)
	}

	return result.Stdout, nil
}

// Up checks that the machine answers, that linger is on for the login user, that the user manager runs, that systemd is new enough,
// and that the work root exists with the state folder in it. obee never turns linger on: it is a setting of the owner of the machine.
func (h *Host) Up(ctx context.Context) error {
	user, err := h.run(ctx, "id", "id", "-un")
	if err != nil {
		return fmt.Errorf("cannot reach the host. Check that `ssh %s` works from this computer: %w", h.target(), err)
	}

	user = model.FirstLine(user)
	if err = h.checkLinger(ctx, user); err != nil {
		return err
	}

	if err = h.checkSystemd(ctx); err != nil {
		return err
	}

	_, err = h.stateDir(ctx)

	return err
}

func (h *Host) target() string { return h.link.Target() }

func (h *Host) checkLinger(ctx context.Context, user string) error {
	out, err := h.run(ctx, "loginctl", "loginctl", "show-user", user, "-p", "Linger")
	if err != nil {
		return err
	}

	on, err := parseLinger(out)
	if err != nil {
		return err
	}

	if !on {
		return fmt.Errorf("%w. Ask the owner of the machine to run `loginctl enable-linger %s` there, then run this again. obee does not change the setting", ErrLinger, user)
	}

	return nil
}

func (h *Host) checkSystemd(ctx context.Context) error {
	result := h.link.Exec(ctx, "systemctl", "--user", "is-system-running")
	if err := h.link.Drop(result); err != nil {
		return err
	}

	// A user manager with a failed unit reports degraded and exits non-zero. It still runs jobs.
	if state := strings.TrimSpace(result.Stdout); state != "running" && state != "degraded" {
		return fmt.Errorf("%w: `systemctl --user is-system-running` gave %q. Start the user manager of the login user", ErrSystemd, model.FirstLine(result.Stdout+result.Stderr))
	}

	return h.checkVersion(ctx)
}

func (h *Host) checkVersion(ctx context.Context) error {
	out, err := h.run(ctx, "systemd-run", "systemd-run", "--version")
	if err != nil {
		return err
	}

	version, err := parseSystemd(out)
	if err != nil {
		return err
	}

	if version < minSystemd {
		return fmt.Errorf("%w: systemd %d is older than %d, which a job log needs", ErrSystemd, version, minSystemd)
	}

	return nil
}

// stateDir returns the folder under the work root where the logs and markers of obee live, and makes it.
func (h *Host) stateDir(ctx context.Context) (string, error) {
	root, err := h.WorkRoot(ctx)
	if err != nil {
		return "", err
	}

	dir := path.Join(root, stateFolder)
	_, err = h.run(ctx, "mkdir", "mkdir", "-p", dir)

	return dir, err
}

// Status reports the machine absent when ssh cannot connect, draining when Drain marked it, and its free disk space.
// A df answer that does not parse is a reading that is not readable, which the guards refuse.
func (h *Host) Status(ctx context.Context) (model.HostStatus, error) {
	status, err := h.readings(ctx)
	if errors.Is(err, link.ErrDropped) {
		return model.HostStatus{State: model.HostAbsent}, nil
	}

	return status, err
}

func (h *Host) readings(ctx context.Context) (model.HostStatus, error) {
	root, err := h.WorkRoot(ctx)
	if err != nil {
		return model.HostStatus{}, err
	}

	out, err := h.run(ctx, "df", "df", "-P", "-k", root)
	if err != nil {
		return model.HostStatus{}, err
	}

	state, err := h.upOrDraining(ctx, root)
	if err != nil {
		return model.HostStatus{}, err
	}

	free, parseErr := host.ParseFreeBytes(out)

	return model.HostStatus{State: state, Disk: model.DiskReading{FreeBytes: free, Readable: parseErr == nil}}, nil
}

func (h *Host) upOrDraining(ctx context.Context, root string) (model.HostState, error) {
	draining, err := h.draining(ctx, root)
	if err != nil || !draining {
		return model.HostUp, err
	}

	return model.HostDraining, nil
}

// draining says whether the drain marker exists. test exits 1 for a file that is not there.
func (h *Host) draining(ctx context.Context, root string) (bool, error) {
	result := h.link.Exec(ctx, "test", "-e", path.Join(root, stateFolder, drainMarker))
	if err := h.link.Drop(result); err != nil {
		return false, err
	}

	switch result.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}

	return false, link.Failure("test", result)
}

// Down sends nothing and stops no job. obee did not create the machine, so it never deletes it.
func (*Host) Down(context.Context) error { return nil }

// Teardown banks the work of the machine to `host.private_remote` and leaves the machine up. It is the release of a host that obee does not own:
// host.Teardown cannot be used, as it ends with an audit that the host is gone.
func (h *Host) Teardown(ctx context.Context) error {
	if err := h.bank(ctx); err != nil {
		return fmt.Errorf("host keeps its work: %w", err)
	}

	return nil
}

// bankWork banks the work of the machine. git and find run on the machine through ssh, from the work root.
func (h *Host) bankWork(ctx context.Context) error {
	root, err := h.WorkRoot(ctx)
	if err != nil {
		return err
	}

	return host.BankAll(ctx, h.link.Runner("git"), h.link.Runner("find"), root, h.config.PrivateRemote)
}

// WorkRoot is the absolute path where the repositories live: `host.ssh.work_root`, or the login folder of the machine.
// A folder that starts with ~/ is read against the login folder. The folder must exist. The path is read once and is the same in every call.
func (h *Host) WorkRoot(ctx context.Context) (string, error) {
	h.mu.Lock()
	root := h.root
	h.mu.Unlock()

	if root != "" {
		return root, nil
	}

	root, err := h.resolveRoot(ctx)
	if err != nil {
		return "", err
	}

	h.mu.Lock()
	h.root = root
	h.mu.Unlock()

	return root, nil
}

func (h *Host) resolveRoot(ctx context.Context) (string, error) {
	configured := h.config.SSH.WorkRoot

	if path.IsAbs(configured) {
		return h.existing(ctx, configured)
	}

	login, err := h.loginFolder(ctx)
	if err != nil {
		return "", err
	}

	if configured == "" {
		return login, nil
	}

	return h.existing(ctx, path.Join(login, strings.TrimPrefix(configured, "~/")))
}

func (h *Host) loginFolder(ctx context.Context) (string, error) {
	out, err := h.run(ctx, "pwd", "pwd", "-P")
	if err != nil {
		return "", err
	}

	lines := model.NonEmptyLines(out)
	if len(lines) != 1 || !path.IsAbs(lines[0]) {
		return "", fmt.Errorf("%w: %q", errWorkRoot, model.FirstLine(out))
	}

	return lines[0], nil
}

func (h *Host) existing(ctx context.Context, root string) (string, error) {
	if _, err := h.run(ctx, "test", "test", "-d", root); err != nil {
		return "", fmt.Errorf("the work root %s does not exist on the host: %w", root, err)
	}

	return root, nil
}

// Submit starts the command as a transient unit of the login user and returns the unit name as the job id.
// The command travels as base64 and the unit runs it with eval, because systemd expands a dollar sign in a command line before the shell sees it.
// --remain-after-exit keeps the finished unit loaded, so its result stays readable. A unit that is not kept reads as gone, like a lost one.
func (h *Host) Submit(ctx context.Context, spec model.JobSpec) (model.JobID, error) {
	if spec.Name == "" || spec.Command == "" {
		return "", errJobFields
	}

	root, err := h.WorkRoot(ctx)
	if err != nil {
		return "", err
	}

	if err = h.refuseDraining(ctx, root); err != nil {
		return "", err
	}

	dir, err := h.stateDir(ctx)
	if err != nil {
		return "", err
	}

	return h.start(ctx, root, dir, spec.Command)
}

// refuseDraining returns ErrDraining when Drain marked the host.
func (h *Host) refuseDraining(ctx context.Context, root string) error {
	draining, err := h.draining(ctx, root)
	if err != nil {
		return err
	}

	if draining {
		return ErrDraining
	}

	return nil
}

// start runs the unit. dir is the state folder under root.
func (h *Host) start(ctx context.Context, root, dir, command string) (model.JobID, error) {
	unit := "obee-" + h.nonce()
	encoded := base64.StdEncoding.EncodeToString([]byte(command))

	_, err := h.run(ctx, "systemd-run", "systemd-run", "--user", "--quiet", "--unit="+unit, "--remain-after-exit",
		"-p", "RuntimeMaxSec="+strconv.FormatInt(int64(h.config.MaxRun/time.Second), 10), "-p", "TimeoutStopSec="+strconv.Itoa(stopTimeout),
		"-p", "WorkingDirectory="+root, "-p", "StandardOutput=append:"+path.Join(dir, unit+logSuffix), "-p", "StandardError=inherit",
		"--", "sh", "-c", `eval "$(printf %s `+encoded+` | base64 -d)"`)
	if err != nil {
		return "", err
	}

	return model.JobID(unit), nil
}

// Wait polls the unit until the job has a final state. See jobwait for the states, the lost, stale and died limits, and the bank.
// A unit is ended (stopped and reset) after the bank. The first poll that reads a final state records it beside the log,
// so a second Wait for the same id, such as a replay after a crash, reads the same result.
// Wait refuses an id that Submit did not return.
func (h *Host) Wait(ctx context.Context, id model.JobID) (model.JobState, error) {
	job, err := parseJobID(id)
	if err != nil {
		return model.JobUnknown, err
	}

	return h.engine.Wait(ctx, job)
}

// Cancel writes the marker of the cancel, stops the unit, waits until the processes are gone, banks the work and ends the unit.
// systemctl stop signals the whole cgroup of the unit, so a process that called setsid is stopped too.
func (h *Host) Cancel(ctx context.Context, id model.JobID) error {
	job, err := parseJobID(id)
	if err != nil {
		return err
	}

	return h.engine.Cancel(ctx, job)
}

// Log returns the end of the log of a job, at most logTailBytes bytes.
func (h *Host) Log(ctx context.Context, id model.JobID) (string, error) {
	job, err := parseJobID(id)
	if err != nil {
		return "", err
	}

	base, err := h.base(ctx, job)
	if err != nil {
		return "", err
	}

	return h.run(ctx, "tail", "tail", "-c", strconv.Itoa(logTailBytes), base+logSuffix)
}

// base is the path of the files of a job without a suffix: its log and its cancel marker.
func (h *Host) base(ctx context.Context, job jobwait.Job) (string, error) {
	root, err := h.WorkRoot(ctx)
	if err != nil {
		return "", err
	}

	return path.Join(root, stateFolder, job.ID), nil
}

// Drain makes the host refuse new jobs by a marker file in the state folder. Running jobs finish.
// It is a file, not a system setting, because obee changes nothing on the machine beyond its own folder.
func (h *Host) Drain(ctx context.Context) error {
	dir, err := h.stateDir(ctx)
	if err != nil {
		return err
	}

	_, err = h.run(ctx, "touch", "touch", path.Join(dir, drainMarker))

	return err
}

// Resume makes the host take jobs again.
func (h *Host) Resume(ctx context.Context) error {
	root, err := h.WorkRoot(ctx)
	if err != nil {
		return err
	}

	_, err = h.run(ctx, "rm", "rm", "-f", path.Join(root, stateFolder, drainMarker))

	return err
}

// release ends the unit of a job after its work is banked. reset-failed unloads a failed unit and stop unloads a finished one.
// The first exits non-zero for a unit that is gone, and the goal is the absence of the unit, so only a dropped link is an error.
func (h *Host) release(ctx context.Context, job jobwait.Job) error {
	if err := h.link.Drop(h.link.Exec(ctx, "systemctl", "--user", "reset-failed", job.ID)); err != nil {
		return err
	}

	return h.stop(ctx, job)
}

// stop stops the unit. An exit status of 5 is a unit that is not loaded: it is already stopped.
func (h *Host) stop(ctx context.Context, job jobwait.Job) error {
	result := h.link.Exec(ctx, "systemctl", "--user", "stop", job.ID)
	if err := h.link.Drop(result); err != nil {
		return err
	}

	if result.ExitCode != 0 && result.ExitCode != stopNotLoaded {
		return link.Failure("systemctl stop", result)
	}

	return nil
}

type scheduler struct{ h *Host }

// Observe reads the cancel marker, the properties of the unit and the size of its log in one ssh call.
func (s scheduler) Observe(ctx context.Context, job jobwait.Job) (model.JobState, int64, error) {
	base, err := s.h.base(ctx, job)
	if err != nil {
		return model.JobUnknown, -1, err
	}

	r, size, err := s.read(ctx, job, base)
	if err != nil {
		return model.JobUnknown, -1, err
	}

	state, err := r.state()
	if err == nil {
		err = s.record(ctx, base, r, state)
	}

	return state, size, err
}

// read runs the poll script on the host and parses what it printed.
func (s scheduler) read(ctx context.Context, job jobwait.Job, base string) (reading, int64, error) {
	out, err := s.h.run(ctx, "systemctl show", "sh", "-c", observeShell, "sh", job.ID, base)
	if err != nil {
		return reading{}, -1, err
	}

	return parseReading(out)
}

// record writes the final state of a job beside its log before the unit is released, so a later read of the job still has its result.
func (s scheduler) record(ctx context.Context, base string, r reading, state model.JobState) error {
	if r.recorded == state || !isRecorded(state) {
		return nil
	}

	_, err := s.h.run(ctx, "touch", "touch", base+"."+string(state))

	return err
}

// Stop writes the marker of the cancel and then stops the unit. The marker comes first, so a stopped unit is never read as a success.
func (s scheduler) Stop(ctx context.Context, job jobwait.Job) error {
	base, err := s.h.base(ctx, job)
	if err != nil {
		return err
	}

	if _, err = s.h.run(ctx, "touch", "touch", base+canceledSuffix); err != nil {
		return err
	}

	return s.h.stop(ctx, job)
}

// LostAdvice is what a person does about a job whose wait ended as lost. The job may still run, so a second run needs a stop of the first one.
func (scheduler) LostAdvice(job jobwait.Job) string {
	return fmt.Sprintf("job %s may still run on the host. Check it with `systemctl --user status %s` on the host and stop it there with `systemctl --user stop %s`. Run it again only after the stop", job, job.ID, job.ID)
}

func (scheduler) StuckAdvice(job jobwait.Job) string {
	return fmt.Sprintf("Check it with `systemctl --user status %s` on the host and stop it again with `systemctl --user stop %s`", job.ID, job.ID)
}
