package model

import (
	"errors"
	"fmt"
	"net/url"
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

// HostConfig is the [host] table of a project: the keys every provider shares, and the table of the chosen provider.
type HostConfig struct {
	Provider     HostProvider
	IdleMinutes  int
	StaleMinutes int // zero means the default of the provider
	MaxRun       time.Duration
	DiskFloorGB  int
	// DiskWaitMinutes is how long a job waits for free space when the disk is under the floor. Zero means the job is refused at once.
	DiskWaitMinutes int
	// BankBudget is the time that the bank scan gives to each repository on the host. Zero means the default of the host package.
	BankBudget    time.Duration
	PrivateRemote string
	SkyPilot      SkyPilotConfig
	SSH           SSHConfig
}

// The host key policies of the ssh link. Neither one accepts a host key that changed.
const (
	KnownHostsStrict    = "strict"
	KnownHostsAcceptNew = "accept-new"
)

// SSHConfig is the [host.ssh] table, the link to a machine or to the head node of a cluster. An empty field leaves the choice to the ssh config of the user.
// KnownHosts is never empty after the loader ran.
type SSHConfig struct {
	Address    string
	User       string
	Port       int
	Identity   string
	WorkRoot   string
	KnownHosts string
}

// SkyPilotConfig is the [host.skypilot] table. An empty field leaves the choice to SkyPilot.
type SkyPilotConfig struct {
	Cloud        string
	Region       string
	InstanceType string
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
	// JobLost means the link to the host stayed down. The job may still run on the host. It is not a final state of the job.
	JobLost JobState = "lost"
)

// Terminal reports whether the job has ended. A lost job has not: the wait cannot tell.
func (s JobState) Terminal() bool {
	return s == JobSucceeded || s == JobFailed || s == JobCanceled || s == JobDied || s == JobStale || s == JobTimedOut
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
