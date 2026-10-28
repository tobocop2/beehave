package config

import (
	"errors"
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
	Repo        string             `toml:"repo"`
	Tracker     string             `toml:"tracker"`
	Forge       string             `toml:"forge"`
	BeadsPrefix string             `toml:"beads_prefix"`
	BaseBranch  string             `toml:"base_branch"`
	Gate        string             `toml:"gate"`
	BuildHost   *string            `toml:"build_host"`
	Steps       map[string]rawStep `toml:"steps"`
}

// loadProject reads one project file. The project name is the file name without ".toml".
func loadProject(path string) (Project, error) {
	var raw rawProject
	if err := decodeFile(path, &raw, projectKeys+"; [steps.<name>]: "+stepKeys); err != nil {
		return Project{}, err
	}

	profile, err := profileOf(path, raw)
	if err != nil {
		return Project{}, err
	}

	steps, err := applySteps(path, DefaultSteps(), raw.Steps)

	return Project{Project: profile, Path: path, Steps: steps}, err
}

// profileOf checks the keys of a project file and returns the profile they describe.
func profileOf(path string, raw rawProject) (model.Project, error) {
	host, err := validateProject(path, raw)
	if err != nil {
		return model.Project{}, err
	}

	tracker, forge, err := adapterNames(path, raw)
	if err != nil {
		return model.Project{}, err
	}

	return model.Project{
		Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Repo: raw.Repo, Tracker: tracker, Forge: forge,
		BeadsPrefix: raw.BeadsPrefix, BaseBranch: raw.BaseBranch, Gate: raw.Gate, BuildHost: host,
	}, nil
}

// validateProject checks the keys and returns the build host.
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

// adapterNames checks that the project names a tracker and a forge that obee has an adapter for, and its beads prefix. The claim rows live in beads whatever tracker the project uses. No adapter is a default.
func adapterNames(path string, raw rawProject) (model.TrackerName, model.ForgeName, error) {
	if err := errors.Join(requireText(path, "tracker", raw.Tracker), requireText(path, "forge", raw.Forge)); err != nil {
		return "", "", err
	}

	tracker, err := model.ParseTracker(raw.Tracker)
	if err != nil {
		return "", "", refuse("%s: `tracker`: %w. Fix the value.", path, err)
	}

	forge, err := model.ParseForge(raw.Forge)
	if err != nil {
		return "", "", refuse("%s: `forge`: %w. Fix the value.", path, err)
	}

	// The claim rows live in beads whatever tracker the project uses, so every project names its beads prefix.
	return tracker, forge, requireText(path, "beads_prefix", raw.BeadsPrefix)
}
