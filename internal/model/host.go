package model

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ErrUndeclaredHost is returned when a run names a build host that the project file does not declare.
var ErrUndeclaredHost = errors.New("undeclared build host")

// HostFor returns the cluster that a run uses. An empty choice gives BuildHost, the default. A named cluster must be a declared host of the project.
// A project that builds here declares only "local", so a workflow that names its host "local" works, and a cluster name is refused.
func (p Project) HostFor(cluster string) (string, error) {
	if cluster == "" {
		return p.BuildHost, nil
	}

	declared := p.BuildHosts
	if len(declared) == 0 {
		declared = []string{p.BuildHost}
	}

	if slices.Contains(declared, cluster) {
		return cluster, nil
	}

	return "", fmt.Errorf("%w %q for project %s: use one of %s", ErrUndeclaredHost, cluster, p.Name, strings.Join(declared, ", "))
}

// HostProvider names a build host adapter that a project selects. The package of each host exports its name.
type HostProvider string

// MaxDiskFloorGB is the largest disk_floor_gb. It keeps the floor in bytes inside a 64-bit integer.
const MaxDiskFloorGB = 1 << 20

// GateClass is the slot class of the gates. It is reserved: `host.slots` cannot name it and a command call cannot use it.
const GateClass = "gate"

// DefaultClass is the slot class of a host job that names none. `host.slots` can set its count. A host that does not runs one job of the class at once.
const DefaultClass = "default"

// DefaultSlots is the count of the default slot class when `host.slots` does not name it.
const DefaultSlots = 1

// HostConfig is the [host] table of a project: the keys every provider shares, and the block of the chosen provider.
type HostConfig struct {
	Provider     HostProvider
	IdleMinutes  int
	StaleMinutes int // zero means the default of the provider
	MaxRun       time.Duration
	DiskFloorGB  int
	// DiskWaitMinutes is how long a job waits for free space when the disk is under the floor. Zero means the job is refused at once.
	DiskWaitMinutes int
	// BankBudget is the time that the bank scan gives to each repository on the host. Zero means the default of the host package.
	BankBudget time.Duration
	// LostJobWait is how long the engine waits for a proof that a lost job can no longer run. It is always set by the loader, from one minute to 24 hours.
	LostJobWait time.Duration
	// LostAfter is the time without an answered read before a wait for a job ends as lost. StopLimit is the time a job may take to end after a cancel.
	// The loader always sets both, above zero. Zero means the default of the config package.
	LostAfter, StopLimit time.Duration
	PrivateRemote        string
	// Slots is the number of jobs of each slot class that the host runs at once. A class that is not a key has no slot on the host. It is nil when the block names none.
	Slots map[string]int
	// GateSlots is the number of gates that the host builds at once. The loader always sets it, from 1 to 1048576, and 1 is the default.
	GateSlots int
	// Block is the config block of the chosen provider, when the provider declares one and the project gave it. The provider reads its keys once, in its constructor.
	Block *Block
	// Credentials is HostCredentialsNone when the owner declares that the host gets no credential of the owner. It is empty otherwise.
	Credentials HostCredentials
}

// HostCredentials is what the owner declares about the credentials that a build host gets.
type HostCredentials string

// HostCredentialsNone declares that the host gets no credential of the owner: no agent, key, display or secret variable.
const HostCredentialsNone HostCredentials = "none"

// ErrUnknownHostCredentials is returned when text names no value of host.credentials.
var ErrUnknownHostCredentials = errors.New(`unknown value: the only value is "none"`)

// ParseHostCredentials returns the credentials value that text names.
func ParseHostCredentials(text string) (HostCredentials, error) {
	if text == string(HostCredentialsNone) {
		return HostCredentialsNone, nil
	}

	return "", fmt.Errorf("%w; got %q", ErrUnknownHostCredentials, text)
}

// RunLimitSeconds is MaxRun in whole seconds, rounded up, so the limit that a host or a wrap writes is never shorter than MaxRun.
// It divides first and adds one for a rest, so it cannot overflow for any MaxRun.
func (h HostConfig) RunLimitSeconds() int64 {
	seconds := int64(h.MaxRun / time.Second)
	if h.MaxRun%time.Second > 0 {
		seconds++
	}

	return seconds
}

// The host key policies of the ssh link. Neither one accepts a host key that changed.
const (
	KnownHostsStrict    = "strict"
	KnownHostsAcceptNew = "accept-new"
)

// The kinds of LostProof. An empty Kind is no proof.
const (
	// LostStopped means the host answered a stop of the job, so the job no longer runs.
	LostStopped = "stopped"
	// LostEnded means the scheduler names a final state for the job.
	LostEnded = "ended"
	// LostGone means the host that ran the job no longer exists.
	LostGone = "gone"
)

// LostProof is what a host adapter read about a job whose wait ended as lost. A Kind that is not empty is the proof that the job can no longer run.
// An empty Kind is no proof, for example while the link to the host is down; Note then holds the text of the error that stopped the read.
// No proof is a value, not a failed call. Both fields are text, so the value survives a recorded step.
type LostProof struct {
	Kind string
	Note string
}

// HostState is whether a build host exists and takes jobs.
type HostState string

// The states of a build host.
const (
	HostAbsent   HostState = "absent"
	HostUp       HostState = "up"
	HostDraining HostState = "draining"
)

// DiskReading is the free space of the build host. A reading that Readable is false for must not be used.
type DiskReading struct {
	FreeBytes uint64
	Readable  bool
}

// HostStatus is what a build host reports about itself.
type HostStatus struct {
	State HostState
	Disk  DiskReading
}

// JobID names one job on a build host. Only the adapter that returned it reads it. A caller stores it as it is and gives it back to any
// handle of the same host, also after a restart, so an adapter keeps in the id all it needs to find the job again.
type JobID string

// JobSpec is one job to run on a build host.
type JobSpec struct {
	Name    string
	Command string
	// Key names the call that submits the job. A Submit with the Key of a job that the host already holds returns that job and starts none.
	// An empty Key starts a new job at each Submit.
	Key string
	// Folder is the absolute path of the folder that the job works in. Wait and Cancel bank only this folder, so a job never reads the clone of another job.
	// Empty means the job has no folder of its own, and they bank the whole work root.
	Folder string
	// Slot is the limit that the job takes on the host: at most Slot.Count jobs of Slot.Class run on the host at once, for every engine that uses it. The zero Slot means no limit.
	Slot Slot
}

// Slot is a slot class and its count. The zero value means no limit.
type Slot struct {
	Class string
	Count int
}

// SlotClassPattern is the class name that the host block accepts. A provider writes the name in a folder name on the host.
var SlotClassPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ErrBadSlot is the refusal of a job whose slot has no valid class or no count.
var ErrBadSlot = errors.New("a job slot needs a class name of letters, digits, dots, dashes and underscores, and a count above zero")

// Limited reports whether the slot limits the job.
func (s Slot) Limited() bool { return s != Slot{} }

// Check refuses a slot with a class that is not a plain name, or with a count below one. The zero Slot passes.
func (s Slot) Check() error {
	if !s.Limited() {
		return nil
	}

	if !SlotClassPattern.MatchString(s.Class) || s.Count < 1 {
		return fmt.Errorf("%w: got class %q and count %d", ErrBadSlot, s.Class, s.Count)
	}

	return nil
}

// JobState is where a job stands. The adapter reads it from the scheduler of the host.
type JobState string

// The states of a job. JobUnknown means the scheduler gave no answer that beehave can read.
const (
	JobPending   JobState = "pending"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCanceled  JobState = "canceled"
	JobUnknown   JobState = "unknown"
	// JobDied means the scheduler holds no record of the job: nothing runs it and no result exists.
	JobDied JobState = "died"
	// JobStale means the log of the job did not grow for host.stale_minutes. The wait stopped the job.
	JobStale JobState = "stale"
	// JobTimedOut means the run limit (host.max_run) stopped the job: `timeout` ended it with exit status 124, or the scheduler did. It has no result.
	JobTimedOut JobState = "timed_out"
	// JobDiskFull means the host fell under its free-space floor while the job ran, and the wait stopped the job. It has no result: it is a problem of the host.
	JobDiskFull JobState = "disk_full"
	// JobLost means the link to the host stayed down. The job may still run on the host. It is not a final state of the job.
	JobLost JobState = "lost"
)

// Terminal reports whether the job has ended. A lost job has not: the wait cannot tell.
func (s JobState) Terminal() bool {
	return s == JobSucceeded || s == JobFailed || s == JobCanceled || s == JobDied || s == JobStale || s == JobTimedOut || s == JobDiskFull
}

// RemoteHasHost reports whether a git remote names a machine: a URL with a scheme other than file and a host, or a user@host:path address. A local path or a file URL names none.
// A text that starts with `-` is an option, and a text with `::` is a git transport helper that runs a command, so neither names a machine.
func RemoteHasHost(remote string) bool {
	if strings.HasPrefix(remote, "-") || strings.Contains(remote, "::") {
		return false
	}

	if strings.Contains(remote, "://") {
		parsed, err := url.Parse(remote)

		return err == nil && parsed.Scheme != "file" && parsed.Hostname() != ""
	}

	address, _, found := strings.Cut(remote, ":")
	_, machine, hasUser := strings.Cut(address, "@")

	if hasUser {
		address = machine
	}

	return found && address != "" && !strings.ContainsAny(address, "/\\")
}
