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
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/flow"
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
	found := map[string]writerRole{}

	var tokens []string

	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		cred, declared := cfg.Projects[name].Credentials[model.RoleWriter]
		if !declared {
			continue
		}

		token, err := credential.Load(ctx, cred, opts.Env)
		if err != nil {
			return fmt.Errorf("project %s: the writer credential: %w", name, err)
		}

		dir, err := emptyFolder(filepath.Join(opts.StateDir, "roles", model.SafeName(name), "gh"))
		if err != nil {
			return fmt.Errorf("project %s: %w", name, err)
		}

		found[name] = writerRole{token: token, configDir: dir}
		tokens = append(tokens, token)
	}

	if len(found) == 0 {
		return nil
	}

	if err := credential.Probe(agent.LoginEnv(opts.Env, agent.Profile{}, ""), tokens...); err != nil {
		return err
	}

	w.byProject, w.env = found, opts.Env

	return nil
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

// pushRunner returns the git runner for the push of a project. A project with credentials pushes as the writer: the environment is built from the
// allow list, the credential helper of the call and the repository dir, so no helper, token or ssh agent of the owner reaches git.
func (w *writers) pushRunner(project, dir string) (gitexec.Runner, bool) {
	role, found := w.of(project)
	if !found {
		return gitexec.Runner{}, false
	}

	runner := gitexec.NewRemote(dir, proc.DefaultTimeout)
	runner.Only = gitexec.RoleEnv(role.token, w.env, "GIT_DIR="+dir)

	return runner, true
}
