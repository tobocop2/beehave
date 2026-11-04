// Package skypilot is the host provider that creates a machine with the sky command and queues jobs on it with Slurm, through the slurm package.
// It runs the sky and ssh command lines through proc.Runner. It links no cloud library and opens no listener of its own.
// The SkyPilot CLI starts its own local API server; only a project that selects this provider gets it.
package skypilot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/jobwait"
	"github.com/tobocop2/obee/internal/host/link"
	"github.com/tobocop2/obee/internal/host/slurm"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

// ErrConfig is returned by New for a cluster name or a setting that the sky command line cannot take.
var ErrConfig = errors.New("invalid SkyPilot host setting")

const runLimitKey = "gcp.managed_instance_group.run_duration="

var wordPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Host is a SkyPilot cluster with Slurm on its head node. SkyPilot creates and deletes the cluster; the Slurm queue runs the jobs.
type Host struct {
	cluster string
	config  model.HostConfig
	sky     proc.Runner
	link    *link.Link
	*slurm.Queue
}

// New returns a host for the cluster. sky is the sky command line. ssh is the ssh command line: the first argument of a call is a cluster name, as SkyPilot writes one ssh config entry for each cluster.
// Up is the first call and Down the last. A setting that cannot reach the sky command line safely is refused.
// A test passes options to replace the clock, the bank and the nonce of the queue.
func New(cluster string, config model.HostConfig, sky, ssh proc.Runner, opts ...slurm.Option) (*Host, error) {
	if err := checkConfig(cluster, config); err != nil {
		return nil, err
	}

	h := &Host{cluster: cluster, config: config, sky: sky, link: link.New(cluster, ssh)}
	h.Queue = slurm.New(h.link, config, slot{h}, opts...)

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

	_, err := link.Run(ctx, h.sky, "sky launch", args)

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
	out, err := link.Run(ctx, h.sky, "sky status", []string{"status", "-r", "-o", "json"})
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

	return h.Readings(ctx)
}

// Down deletes the cluster. A caller stops a host with host.Teardown, which banks the work first.
func (h *Host) Down(ctx context.Context) error {
	_, err := link.Run(ctx, h.sky, "sky down", []string{"down", "-y", h.cluster})

	return err
}

// Teardown banks the work of the head node to `host.private_remote` and then deletes the cluster. It is host.Teardown with git and find on the head node.
// A head node with unbanked work, or one whose work cannot be read, stays up, and the error says why.
func (h *Host) Teardown(ctx context.Context) error {
	return host.Teardown(ctx, h, h.link.Runner("git"), h.link.Runner("find"), h.config.PrivateRemote, host.WithRepoBudget(h.config.BankBudget))
}

// slot is the part of SkyPilot that the Slurm queue calls. SkyPilot counts only its own job queue as work, so Hold starts a SkyPilot job
// (`sky exec -d`) that holds the cluster until Release writes the bank marker, for at most max_run.
type slot struct{ h *Host }

// Hold starts the SkyPilot job that keeps the cluster busy.
func (s slot) Hold(ctx context.Context, job jobwait.Job) error {
	_, err := link.Run(ctx, s.h.sky, "sky exec", []string{"exec", s.h.cluster, "-d", slotCommand(s.h.config.MaxRun, s.markerName(job))})

	return err
}

// Release writes the marker that ends the slot job.
func (s slot) Release(ctx context.Context, job jobwait.Job) error {
	marker := s.markerName(job)
	_, err := s.h.link.Remote(ctx, "touch", "touch", marker)

	return err
}

// markerName is the name of the file in the login folder that ends the slot job of a job. It holds the nonce made for the Submit,
// so a job id that Slurm uses again never matches the marker of an earlier job.
func (s slot) markerName(job jobwait.Job) string {
	return ".obee-banked-" + s.h.cluster + "-" + job.String()
}

// slotCommand is the command of the SkyPilot job that keeps the cluster busy until the marker exists.
func slotCommand(maxRun time.Duration, marker string) string {
	return fmt.Sprintf("cd ~ && timeout %d sh -c 'until test -e %s; do sleep 5; done'", int64(maxRun/time.Second), marker)
}
