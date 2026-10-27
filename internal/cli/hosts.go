package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
)

// Machine is a build host that can also bank its work and stop itself. Teardown is host.Teardown with the runners of the host.
// ClusterID is the id that the host reports for its cluster. It differs between two clusters that share a name.
type Machine interface {
	host.Host
	Teardown(ctx context.Context) error
	ClusterID(ctx context.Context) (string, error)
}

// HostDeps opens the build host of a project. A nil Open leaves out the host commands.
type HostDeps struct {
	Open func(model.Project) (Machine, error)
}

func hostSpecs(deps HostDeps) []spec {
	if deps.Open == nil {
		return nil
	}

	return []spec{
		{use: "up <project>", short: "Start the build host of a project and mark it for host clean", args: cobra.ExactArgs(1), bind: bindHostUp(deps)},
		{use: "submit <project> <command>", short: "Queue a job on the build host of a project", args: cobra.ExactArgs(twoArgs), bind: bindHostSubmit(deps)},
		{use: "clean <project>", short: "Bank the work on the build host of a project and stop it", args: cobra.ExactArgs(1), bind: bindHostClean(deps)},
	}
}

func bindConfigFlag(f *pflag.FlagSet, folder *string) {
	f.StringVar(folder, configFlag, "", "the config folder (default: $"+config.ConfigDirVariable+", then ~/.config/obee)")
}

// submitOptions holds the flags of host submit.
type submitOptions struct {
	folder string
	name   string
	log    string
	marker string
	wait   bool
}

func bindHostSubmit(deps HostDeps) func(*pflag.FlagSet) func(input) error {
	return func(f *pflag.FlagSet) func(input) error {
		opts := &submitOptions{}
		bindConfigFlag(f, &opts.folder)
		f.StringVar(&opts.name, "name", "obee-job", "the name of the job")
		f.StringVar(&opts.log, "log", "", "the log file of the job, as the command writes it")
		f.StringVar(&opts.marker, "marker", "", "the line the command prints when it ends")
		f.BoolVar(&opts.wait, "wait", false, "wait for the job and exit 1 when it does not succeed")

		return func(in input) error { return runHostSubmit(deps, in, opts) }
	}
}

func bindHostClean(deps HostDeps) func(*pflag.FlagSet) func(input) error {
	return func(f *pflag.FlagSet) func(input) error {
		var folder string

		bindConfigFlag(f, &folder)

		return func(in input) error { return runHostClean(deps, in, folder) }
	}
}

// clusterID is the id that the host reports for its cluster. An empty id is an error, because an empty marker would match it.
func clusterID(in input, machine Machine) (string, error) {
	id, err := machine.ClusterID(in.ctx)
	if err != nil {
		return "", err
	}

	if id == "" {
		return "", errors.New("the host reported no id for its cluster")
	}

	return id, nil
}

// openMachine loads the config folder and opens the build host of the named project. It returns the project with the host.
//
//nolint:ireturn // Machine is the contract of a build host; the caller picks the host
func openMachine(deps HostDeps, in input, flag string) (Machine, model.Project, error) {
	dir, err := config.FolderPath(in.env, flag)
	if err != nil {
		return nil, model.Project{}, err
	}

	cfg, problems := config.LoadFolder(dir, in.env)
	if len(problems) > 0 {
		return nil, model.Project{}, problems[0]
	}

	project, found := cfg.Projects[in.args[0]]
	if !found {
		return nil, model.Project{}, fmt.Errorf("the config has no project named %q", in.args[0])
	}

	machine, err := deps.Open(project.Project)

	return machine, project.Project, err
}

func runHostSubmit(deps HostDeps, in input, opts *submitOptions) error {
	if opts.log == "" || opts.marker == "" {
		return errors.New("give the log file with --log and the end marker with --marker: the launched line names both")
	}

	machine, _, err := openMachine(deps, in, opts.folder)
	if err != nil {
		return err
	}

	id, err := machine.Submit(in.ctx, model.JobSpec{Name: opts.name, Command: in.args[1]})
	if err != nil {
		return err
	}

	announce(in, opts, id)

	return waitForJob(in, machine, id, opts.wait)
}

// announce prints the line that names the log and the marker of a launched job, and the job.
func announce(in input, opts *submitOptions, id model.JobID) {
	say(in.stdout, fmt.Sprintf("launched: %s, marker %s", opts.log, opts.marker))
	say(in.stderr, "job "+string(id))
}

// waitForJob waits for the job when wait is set, and fails when the job does not succeed.
func waitForJob(in input, machine Machine, id model.JobID, wait bool) error {
	if !wait {
		return nil
	}

	state, err := host.WaitJob(in.ctx, machine, id)
	if err != nil {
		return err
	}

	say(in.stdout, fmt.Sprintf("job %s %s", id, state))

	if state != model.JobSucceeded {
		return &ExitError{Code: exitError}
	}

	return nil
}

func bindHostUp(deps HostDeps) func(*pflag.FlagSet) func(input) error {
	return func(f *pflag.FlagSet) func(input) error {
		var folder string

		bindConfigFlag(f, &folder)

		return func(in input) error { return runHostUp(deps, in, folder) }
	}
}

// runHostUp starts the cluster of a project and marks it, so that host clean may stop it.
func runHostUp(deps HostDeps, in input, folder string) error {
	machine, project, err := openMachine(deps, in, folder)
	if err != nil {
		return err
	}

	if err = startAndMark(in, machine, project); err != nil {
		return err
	}

	say(in.stdout, "host up: "+in.args[0])

	return nil
}

// runHostClean stops only a cluster that host up started in this state folder. The marker goes after the teardown succeeds.
func runHostClean(deps HostDeps, in input, folder string) error {
	machine, project, err := openMachine(deps, in, folder)
	if err != nil {
		return err
	}

	if err = teardownOwned(in, machine, project); err != nil {
		return err
	}

	say(in.stdout, "host stopped: "+in.args[0])

	return nil
}
