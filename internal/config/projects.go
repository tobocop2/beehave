package config

import (
	"path/filepath"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// Project is one project profile and the settings of its workflow steps.
type Project struct {
	model.Project

	Path  string
	Steps Steps
}

type rawProject struct {
	Repo       string             `toml:"repo"`
	BaseBranch string             `toml:"base_branch"`
	Gate       string             `toml:"gate"`
	BuildHost  *string            `toml:"build_host"`
	Steps      map[string]rawStep `toml:"steps"`
}

// loadProject reads one project file. The project name is the file name without ".toml".
func loadProject(path string) (Project, error) {
	var raw rawProject
	if err := decodeFile(path, &raw, projectKeys+"; [steps.<name>]: "+stepKeys); err != nil {
		return Project{}, err
	}

	host, err := validateProject(path, raw)
	if err != nil {
		return Project{}, err
	}

	steps, err := applySteps(path, DefaultSteps(), raw.Steps)

	return Project{
		Project: model.Project{
			Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Repo: raw.Repo,
			BaseBranch: raw.BaseBranch, Gate: raw.Gate, BuildHost: host,
		},
		Path:  path,
		Steps: steps,
	}, err
}

// validateProject checks the four keys and returns the build host.
func validateProject(path string, raw rawProject) (string, error) {
	if err := requireText(path, "repo", raw.Repo); err != nil {
		return "", err
	}

	owner, name, found := strings.Cut(raw.Repo, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", refuse("%s: `repo` must be written owner/name. Fix the value.", path)
	}

	host, err := buildHost(path, raw.BuildHost)
	if err != nil {
		return "", err
	}

	if err = requireText(path, "base_branch", raw.BaseBranch); err != nil {
		return "", err
	}

	return host, requireText(path, "gate", raw.Gate)
}

func buildHost(path string, raw *string) (string, error) {
	if raw == nil {
		return LocalHost, nil
	}

	if err := requireText(path, "build_host", *raw); err != nil {
		return "", err
	}

	if !clusterName.MatchString(*raw) {
		return "", refuse("%s: `build_host` must be %q or a SkyPilot cluster name (a letter, then letters, digits and hyphens); got `%s`. Fix the value.",
			path, LocalHost, *raw)
	}

	return *raw, nil
}
