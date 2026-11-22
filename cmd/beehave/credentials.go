package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/credential"
	"github.com/tobocop2/beehave/internal/flow"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

var errCredentialsNotLoaded = errors.New("the credentials of the project are not loaded. Start the engine with beehave run")

// writerRole is the writer credential of one project, read at engine start: the token, and the empty gh config folder that its calls own.
type writerRole struct {
	token     string
	configDir string
}

// writers holds the writer role of each project that declares credentials. The engine fills it once at start, before any step runs, and nothing writes it later.
type writers struct {
	byProject map[string]writerRole
	env       map[string]string
}

// start reads the writer credential of each project that declares one and makes the gh config folder of its role. It then runs the start probe on the
// environment that an agent step and a command step get, and refuses to start when that environment holds a forge token or a token value.
func (w *writers) start(ctx context.Context, cfg config.Config, opts flow.Options) error {
	found, err := readRoles(ctx, cfg, opts)
	if err != nil || len(found) == 0 {
		return err
	}

	if err := probe(found, opts.Env); err != nil {
		return err
	}

	w.byProject, w.env = found, opts.Env

	return nil
}

// readRoles reads the writer role of each project that declares credentials, in the order of the project names.
func readRoles(ctx context.Context, cfg config.Config, opts flow.Options) (map[string]writerRole, error) {
	found := map[string]writerRole{}

	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		if _, declared := cfg.Projects[name].Credentials[model.RoleWriter]; !declared {
			continue
		}

		role, err := readRole(ctx, cfg.Projects[name].Project, opts)
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", name, err)
		}

		found[name] = role
	}

	return found, nil
}

// probe checks the environment that an agent step and a command step get against the forbidden names and the tokens of the roles.
func probe(roles map[string]writerRole, env map[string]string) error {
	tokens := make([]string, 0, len(roles))
	for _, role := range roles {
		tokens = append(tokens, role.token)
	}

	return credential.Probe(agent.LoginEnv(env, agent.Profile{}, ""), tokens...)
}

// readRole reads the writer token of the project and makes the gh config folder of its role. A source that is not an https address is refused: the token
// serves https only. An empty source is the clone address of the forge, which is https.
func readRole(ctx context.Context, project model.Project, opts flow.Options) (writerRole, error) {
	if project.Source != "" {
		if _, err := credential.HTTPSHost(project.Source); err != nil {
			return writerRole{}, err
		}
	}

	token, err := credential.Load(ctx, project.Credentials[model.RoleWriter], opts.Env)
	if err != nil {
		return writerRole{}, fmt.Errorf("the writer credential: %w", err)
	}

	dir, err := emptyFolder(filepath.Join(opts.StateDir, "roles", model.SafeName(project.Name), "gh"))

	return writerRole{token: token, configDir: dir}, err
}

// emptyFolder makes the folder dir with mode 0700, and empties it when it exists.
func emptyFolder(dir string) (string, error) {
	if err := errors.Join(os.RemoveAll(dir), os.MkdirAll(dir, ownerOnly)); err != nil {
		return "", fmt.Errorf("make the gh config folder: %w", err)
	}

	return dir, nil
}

// of returns the writer role of a project. A nil writers holds none.
func (w *writers) of(project string) (writerRole, bool) {
	if w == nil {
		return writerRole{}, false
	}

	role, found := w.byProject[project]

	return role, found
}

// runner returns the gh runner of a project. A project with no credentials runs gh with the login of the owner. A project with credentials runs it as the
// writer, with its own environment, and fails when the credentials are not loaded.
func (w *writers) runner(project model.Project) (proc.ExecRunner, error) {
	if _, declared := project.Credentials[model.RoleWriter]; !declared {
		return proc.NewExecRunner(forgegithub.Tool, ""), nil
	}

	role, found := w.of(project.Name)
	if !found {
		return proc.ExecRunner{}, fmt.Errorf("project %s: %w", project.Name, errCredentialsNotLoaded)
	}

	return forgegithub.RoleRunner(role.token, role.configDir, w.env), nil
}

// refused is the runner of a call that must not run. It runs nothing and returns the reason as the error text of a failed call.
type refused struct{ err error }

// Run returns a failed result that carries the reason.
func (r refused) Run(context.Context, []string) model.CommandResult {
	return model.CommandResult{ExitCode: 1, Stderr: r.err.Error(), SetupErr: r.err}
}

// git returns the git runner for a call in dir that reaches the address remote of a project. This is the one place that picks the runner of such a call,
// for the engine and for the client alike. A project with no credentials gets owner. A project with credentials gets the writer: its environment is built
// from the allow list, the credential helper of the call for the host of remote, and pairs, so no helper, token or ssh agent of the owner reaches git.
// A project with credentials whose role is not loaded, or whose remote is not an https address, gets a runner that refuses the call.
func (w *writers) git(projects map[string]config.Project, project, dir, remote string, owner func() proc.Runner, pairs ...string) proc.Runner { //nolint:ireturn // the callers take the interface
	role, loaded := w.of(project)
	if _, declared := projects[project].Credentials[model.RoleWriter]; !loaded && !declared {
		return owner()
	}

	if !loaded {
		return refused{err: fmt.Errorf("project %s: %w", project, errCredentialsNotLoaded)}
	}

	host, err := credential.HTTPSHost(remote)
	if err != nil {
		return refused{err: fmt.Errorf("project %s: %w", project, err)}
	}

	runner := gitexec.NewRemote(dir, proc.DefaultTimeout)
	runner.Only = gitexec.RoleEnv(role.token, host, w.env, pairs...)

	return runner
}
