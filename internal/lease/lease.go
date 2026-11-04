// Package lease holds what every binary that claims an item shares: how the claims are built over the rows, and how a repository finds its project in the beehave config.
// It imports no backend. The binary that owns the backend opens the rows and passes them to Open.
package lease

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

var (
	errNoProject    = errors.New("no project of the beehave config has this repository")
	errManyProjects = errors.New("more than one project of the beehave config has this repository")
)

// Open builds the claims over the rows. The claims say nothing on their own: the caller shows what it wants to show.
func Open(rows claim.Rows) (*claim.Claims, error) {
	return claim.New(claim.Options{Rows: rows, Clock: time.Now, Out: io.Discard})
}

// ProjectOf reads the beehave config folder that env names, with the same rules as `beehave run`, and returns the project that owns the repository (owner/name, or the `source` of the project).
func ProjectOf(env map[string]string, repo string) (model.Project, error) {
	dir, err := config.FolderPath(env, "")
	if err != nil {
		return model.Project{}, err
	}

	cfg, problems := config.LoadFolder(dir, env)
	if len(problems) > 0 {
		return model.Project{}, problems
	}

	names := namesOf(cfg, repo)

	switch len(names) {
	case 0:
		return model.Project{}, fmt.Errorf("%w: %s, in %s", errNoProject, repo, dir)
	case 1:
		return cfg.Projects[names[0]].Project, nil
	}

	return model.Project{}, fmt.Errorf("%w: %s is in the projects %s. Keep one project for each repository", errManyProjects, repo, strings.Join(names, ", "))
}

// namesOf returns the names of the projects of the repository, in order.
func namesOf(cfg config.Config, repo string) []string {
	var names []string

	for name, project := range cfg.Projects {
		if project.Repo == repo || (project.Source != "" && project.Source == repo) {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	return names
}
