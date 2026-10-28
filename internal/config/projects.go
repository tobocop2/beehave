package config

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/tobocop2/obee/internal/model"
)

// Project is one project profile and the settings of its workflow steps.
type Project struct {
	model.Project

	Path  string
	Steps Steps
}

// rawProject is a project file as written. Each key has a range for the refusals that come after the decode.
// A key that is not in the file has an empty range.
type rawProject struct {
	// At is the start of the file. It stands in for a key that is not in the file.
	At            hcl.Range
	Repo          string    `hcl:"repo,optional"`
	RepoAt        hcl.Range `hcl:"repo,attr_name_range"`
	Tracker       string    `hcl:"tracker,optional"`
	TrackerAt     hcl.Range `hcl:"tracker,attr_name_range"`
	Forge         string    `hcl:"forge,optional"`
	ForgeAt       hcl.Range `hcl:"forge,attr_name_range"`
	BeadsPrefix   string    `hcl:"beads_prefix,optional"`
	BeadsPrefixAt hcl.Range `hcl:"beads_prefix,attr_name_range"`
	BaseBranch    string    `hcl:"base_branch,optional"`
	BaseBranchAt  hcl.Range `hcl:"base_branch,attr_name_range"`
	Gate          string    `hcl:"gate,optional"`
	GateAt        hcl.Range `hcl:"gate,attr_name_range"`
	BuildHost     *string   `hcl:"build_host,optional"`
	BuildHostAt   hcl.Range `hcl:"build_host,attr_name_range"`
	Host          *rawHost  `hcl:"host,block"`
	Steps         []rawStep `hcl:"steps,block"`
}

// at returns the range of a key, or the start of the file when the key is not in it.
func (p rawProject) at(key hcl.Range) hcl.Range { return keyAt(key, p.At) }

// loadProject reads one project file. The project name is the file name without ".hcl".
func loadProject(path string) (Project, error) {
	var raw rawProject
	if err := decodeFile(path, &raw); err != nil {
		return Project{}, err
	}

	raw.At = hcl.Range{Filename: path, Start: hcl.InitialPos, End: hcl.InitialPos}

	profile, err := profileOf(path, raw)
	if err != nil {
		return Project{}, err
	}

	steps, err := applySteps(DefaultSteps(), raw.Steps)

	return Project{Project: profile, Path: path, Steps: steps}, err
}

// profileOf checks the keys of a project file and returns the profile they describe.
func profileOf(path string, raw rawProject) (model.Project, error) {
	host, err := validateProject(raw)
	if err != nil {
		return model.Project{}, err
	}

	tracker, forge, err := adapterNames(raw)
	if err != nil {
		return model.Project{}, err
	}

	hostTable, err := hostConfig(host, raw)
	if err != nil {
		return model.Project{}, err
	}

	return model.Project{
		Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Repo: raw.Repo, Tracker: tracker, Forge: forge,
		BeadsPrefix: raw.BeadsPrefix, BaseBranch: raw.BaseBranch, Gate: raw.Gate, BuildHost: host, Host: hostTable,
	}, nil
}

// validateProject checks the keys and returns the build host.
func validateProject(raw rawProject) (string, error) {
	if err := requireText(raw.at(raw.RepoAt), "repo", raw.Repo); err != nil {
		return "", err
	}

	owner, name, found := strings.Cut(raw.Repo, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", refuseAt(raw.RepoAt, "`repo` must be written owner/name. Fix the value.")
	}

	host, err := buildHost(raw)
	if err != nil {
		return "", err
	}

	if err = requireText(raw.at(raw.BaseBranchAt), "base_branch", raw.BaseBranch); err != nil {
		return "", err
	}

	return host, requireText(raw.at(raw.GateAt), "gate", raw.Gate)
}

func buildHost(raw rawProject) (string, error) {
	if raw.BuildHost == nil {
		return LocalHost, nil
	}

	if err := requireText(raw.BuildHostAt, "build_host", *raw.BuildHost); err != nil {
		return "", err
	}

	if !clusterName.MatchString(*raw.BuildHost) {
		return "", refuseAt(raw.BuildHostAt, "`build_host` must be %q or a SkyPilot cluster name (a letter, then letters, digits and hyphens); got `%s`. Fix the value.",
			LocalHost, *raw.BuildHost)
	}

	return *raw.BuildHost, nil
}

// adapterNames checks that the project names a tracker and a forge that obee has an adapter for, and its beads prefix. The claim rows live in beads whatever tracker the project uses. No adapter is a default.
func adapterNames(raw rawProject) (model.TrackerName, model.ForgeName, error) {
	if err := errors.Join(requireText(raw.at(raw.TrackerAt), "tracker", raw.Tracker), requireText(raw.at(raw.ForgeAt), "forge", raw.Forge)); err != nil {
		return "", "", err
	}

	tracker, err := model.ParseTracker(raw.Tracker)
	if err != nil {
		return "", "", refuseAt(raw.TrackerAt, "`tracker`: %w. Fix the value.", err)
	}

	forge, err := model.ParseForge(raw.Forge)
	if err != nil {
		return "", "", refuseAt(raw.ForgeAt, "`forge`: %w. Fix the value.", err)
	}

	// The claim rows live in beads whatever tracker the project uses, so every project names its beads prefix.
	return tracker, forge, requireText(raw.at(raw.BeadsPrefixAt), "beads_prefix", raw.BeadsPrefix)
}
