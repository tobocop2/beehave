package model

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// HostProvider names a build host adapter that a project selects.
type HostProvider string

// The build host adapters.
const HostSkyPilot HostProvider = "skypilot"

// ErrUnknownHostProvider is returned when text names no build host adapter.
var ErrUnknownHostProvider = errors.New("unknown host provider")

// HostProviders lists every build host adapter.
func HostProviders() []HostProvider {
	return []HostProvider{HostSkyPilot}
}

// ParseHostProvider returns the build host adapter that text names.
func ParseHostProvider(text string) (HostProvider, error) {
	for _, name := range HostProviders() {
		if string(name) == text {
			return name, nil
		}
	}

	return "", fmt.Errorf("%w %q: use one of %s", ErrUnknownHostProvider, text, joinNames(HostProviders()))
}

// MaxDiskFloorGB is the largest disk_floor_gb. It keeps the floor in bytes inside a 64-bit integer.
const MaxDiskFloorGB = 1 << 20

// HostConfig is the [host] table of a project: the keys every provider shares, and the table of the chosen provider.
type HostConfig struct {
	Provider      HostProvider
	IdleMinutes   int
	MaxRun        time.Duration
	DiskFloorGB   int
	PrivateRemote string
	SkyPilot      SkyPilotConfig
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

// The states of a job. JobUnknown means the scheduler gave no answer that obee can read.
const (
	JobPending   JobState = "pending"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCanceled  JobState = "canceled"
	JobUnknown   JobState = "unknown"
)

// Terminal reports whether the job has ended.
func (s JobState) Terminal() bool {
	return s == JobSucceeded || s == JobFailed || s == JobCanceled
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
