package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/credential"
	"github.com/tobocop2/beehave/internal/flow"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

var (
	errCredentialsNotLoaded = errors.New("the credentials of the project are not loaded. Start the engine with beehave run")
	errSSHSource            = errors.New("the writer credential is a token for https, and the source of the project is an ssh address. Write the source as an https address")

	// scpAddress matches the scp-like form of an ssh address, user@host:path.
	scpAddress = regexp.MustCompile(`^[^/:@]+@[^/:]+:`)
)

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

// readRole reads the writer token of the project and makes the gh config folder of its role. A source that is an ssh address is refused: the token cannot serve it.
func readRole(ctx context.Context, project model.Project, opts flow.Options) (writerRole, error) {
	if sshAddress(project.Source) {
		return writerRole{}, errSSHSource
	}

	token, err := credential.Load(ctx, project.Credentials[model.RoleWriter], opts.Env)
	if err != nil {
		return writerRole{}, fmt.Errorf("the writer credential: %w", err)
	}

	dir, err := emptyFolder(filepath.Join(opts.StateDir, "roles", model.SafeName(project.Name), "gh"))

	return writerRole{token: token, configDir: dir}, err
}

// sshAddress tells whether a git address names the ssh transport, as ssh://host/path or as user@host:path.
func sshAddress(address string) bool {
	return strings.HasPrefix(address, "ssh://") || strings.HasPrefix(address, "git+ssh://") || scpAddress.MatchString(address)
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

// gitRunner returns the git runner for a call in dir that reaches the remote of a project that has credentials. Its environment is built from the allow
// list, the credential helper of the call and pairs, so no helper, token or ssh agent of the owner reaches git. A project with none gets no runner.
func (w *writers) gitRunner(project, dir string, pairs ...string) (gitexec.Runner, bool) {
	role, found := w.of(project)
	if !found {
		return gitexec.Runner{}, false
	}

	runner := gitexec.NewRemote(dir, proc.DefaultTimeout)
	runner.Only = gitexec.RoleEnv(role.token, w.env, pairs...)

	return runner, true
}
