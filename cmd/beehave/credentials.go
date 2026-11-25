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
	"github.com/tobocop2/beehave/internal/forge"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec/reach"
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
	// owner is the token of the owner for each https host, for the git calls of the projects that have no writer.
	owner ownerTokens
	// forges builds the forge of a project, which answers whether the writer token can change CI.
	forges func(model.Project) (forge.Forge, error)
}

// start reads the token of the owner for the https hosts of the projects with no credentials and the writer credential of each project that declares one,
// runs the start probe on the environment of the steps, refuses a writer token that can change CI, then refuses a project whose forge lacks an ability that the project needs.
// The ability check covers every project with a forge, with a writer or not.
func (w *writers) start(ctx context.Context, cfg config.Config, opts flow.Options) error {
	w.env = opts.Env
	w.readOwnerTokens(ctx, cfg)

	found, err := readRoles(ctx, cfg, opts)
	if err != nil {
		return err
	}

	if len(found) > 0 {
		if err := probe(found, opts.Env); err != nil {
			return err
		}

		w.byProject = found
	}

	if err := w.lockedCI(ctx, cfg); err != nil {
		return err
	}

	return w.abilities(ctx, cfg)
}

// forgeNeeds lists the abilities that the project needs from its forge: commit statuses for a project with a writer, and security alerts when the project sets the key.
func forgeNeeds(project config.Project) []forge.Ability {
	var needs []forge.Ability

	if project.HasWriter() {
		needs = append(needs, forge.AbilityStatuses)
	}

	if project.SecurityAlerts {
		needs = append(needs, forge.AbilityAlerts)
	}

	return needs
}

// abilities asks the forge of each project whether it has the abilities that the project needs, and refuses the start with the name of each one that it lacks.
// A project with no forge, or with no need, makes no call.
func (w *writers) abilities(ctx context.Context, cfg config.Config) error {
	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		project := cfg.Projects[name]

		needs := forgeNeeds(project)
		if project.Forge == "" || len(needs) == 0 {
			continue
		}

		built, err := w.forges(project.Project)
		if err != nil {
			return fmt.Errorf("project %s: %w", name, err)
		}

		if err := forge.Need(ctx, built, needs...); err != nil {
			return fmt.Errorf("project %s: the forge cannot serve the project: %w", name, err)
		}
	}

	return nil
}

// lockedCI asks the forge of each project with a writer, as the writer, whether the token can change CI, and refuses the start on the answer.
func (w *writers) lockedCI(ctx context.Context, cfg config.Config) error {
	for _, name := range slices.Sorted(maps.Keys(w.byProject)) {
		project := cfg.Projects[name].Project

		answer, err := w.ciAnswer(ctx, project)
		if err != nil {
			return fmt.Errorf("project %s: cannot read whether the writer token can change workflow files: %w", name, err)
		}

		if err := credential.CIStart(answer, project.Credentials[model.RoleWriter].CILocked); err != nil {
			return fmt.Errorf("project %s: %w", name, err)
		}
	}

	return nil
}

// ciAnswer reads the answer of the forge of the project. A project with no forge has no one to ask, so its answer is unknown.
func (w *writers) ciAnswer(ctx context.Context, project model.Project) (model.CIWrite, error) {
	if project.Forge == "" {
		return model.CIWriteUnknown, nil
	}

	built, err := w.forges(project)
	if err != nil {
		return model.CIWriteUnknown, err
	}

	return built.CanWriteCI(ctx)
}

// readRoles reads the writer role of each project that declares credentials, in the order of the project names.
func readRoles(ctx context.Context, cfg config.Config, opts flow.Options) (map[string]writerRole, error) {
	found := map[string]writerRole{}

	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		if !cfg.Projects[name].HasWriter() {
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
	if !project.HasWriter() {
		return proc.NewExecRunner(forgegithub.Tool, ""), nil
	}

	role, found := w.of(project.Name)
	if !found {
		return proc.ExecRunner{}, fmt.Errorf("project %s: %w", project.Name, reach.ErrNotLoaded)
	}

	return forgegithub.RoleRunner(role.token, role.configDir, w.env), nil
}

// git returns the git runner for a call in dir that reaches the address remote of a project, for the engine and for the client alike. owner is the runner of
// a project with no credentials, or nil for the runner of the owner with ownerEnv. reach.Runner picks the runner (see there).
func (w *writers) git(projects map[string]config.Project, project, dir, remote string, owner func(string) proc.Runner, ownerEnv []string, pairs ...string) proc.Runner { //nolint:ireturn // the callers take the interface
	role, loaded := w.of(project)

	return reach.Runner(reach.Call{
		Project: project, Dir: dir, Remote: remote, Declared: projects[project].HasWriter(), Loaded: loaded, Token: role.token,
		Base: w.base(), Pairs: pairs, OwnerEnv: ownerEnv, OwnerToken: w.ownerTokenOf,
	}, owner)
}

// base returns the environment that a writer call takes its names from. A nil writers holds none.
func (w *writers) base() map[string]string {
	if w == nil {
		return nil
	}

	return w.env
}
