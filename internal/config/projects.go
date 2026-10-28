package config

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/tobocop2/obee/internal/model"
)

// Project is one project profile and the path of its file.
type Project struct {
	model.Project

	Path string
}

// rawProject is a project file as written. Each key has a range for the refusals that come after the decode.
// A key that is not in the file has an empty range.
type rawProject struct {
	// At is the start of the file. It stands in for a key that is not in the file.
	At            hcl.Range
	Repo          string    `hcl:"repo,optional"`
	RepoAt        hcl.Range `hcl:"repo,attr_name_range"`
	Source        string    `hcl:"source,optional"`
	SourceAt      hcl.Range `hcl:"source,attr_name_range"`
	Tracker       string    `hcl:"tracker,optional"`
	TrackerAt     hcl.Range `hcl:"tracker,attr_name_range"`
	Claims        string    `hcl:"claims,optional"`
	ClaimsAt      hcl.Range `hcl:"claims,attr_name_range"`
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

	return Project{Project: profile, Path: path}, nil
}

// profileOf checks the keys of a project file and returns the profile they describe.
func profileOf(path string, raw rawProject) (model.Project, error) {
	names, err := adapterNames(raw)
	if err != nil {
		return model.Project{}, err
	}

	host, source, err := checkKeys(raw, names)
	if err != nil {
		return model.Project{}, err
	}

	hostTable, err := hostConfig(host, raw)
	if err != nil {
		return model.Project{}, err
	}

	return model.Project{
		Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Repo: raw.Repo, Source: source, Tracker: names.tracker, Claims: names.claims,
		Forge: names.forge, BeadsPrefix: raw.BeadsPrefix, BaseBranch: raw.BaseBranch, Gate: raw.Gate, BuildHost: host, Host: hostTable,
	}, nil
}

// checkKeys checks the keys that the named adapters need and returns the build host and the source.
func checkKeys(raw rawProject, names adapters) (string, string, error) {
	if err := requireAdapterKeys(raw, names); err != nil {
		return "", "", err
	}

	host, err := buildHost(raw)
	if err != nil {
		return "", "", err
	}

	source, err := sourceOf(raw, names.forge, host)
	if err != nil {
		return "", "", err
	}

	return host, source, errors.Join(requireText(raw.at(raw.BaseBranchAt), "base_branch", raw.BaseBranch), requireText(raw.at(raw.GateAt), "gate", raw.Gate))
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

// adapters holds the adapters that a project names.
type adapters struct {
	tracker model.TrackerName
	claims  model.ClaimsName
	forge   model.ForgeName
}

// adapterNames returns the tracker, the claim store and the forge that the project names. A key that is not in the file
// gives the local tracker, the local claim store and no forge. A key that is in the file must name an adapter.
func adapterNames(raw rawProject) (adapters, error) {
	tracker, err := trackerName(raw)
	if err != nil {
		return adapters{}, err
	}

	claims, err := claimsName(raw)
	if err != nil {
		return adapters{}, err
	}

	forge, err := forgeName(raw)

	return adapters{tracker: tracker, claims: claims, forge: forge}, err
}

// present reports whether a key is in the file.
func present(key hcl.Range) bool { return key.Filename != "" }

// given returns the text of a key that is in the file, and false for a key that is not. A key that is in the file must not be blank.
func given(at hcl.Range, key, value string) (string, bool, error) {
	if !present(at) {
		return "", false, nil
	}

	return value, true, requireText(at, key, value)
}

func trackerName(raw rawProject) (model.TrackerName, error) {
	text, found, err := given(raw.TrackerAt, "tracker", raw.Tracker)
	if err != nil || !found {
		return model.TrackerLocal, err
	}

	tracker, err := model.ParseTracker(text)
	if err != nil {
		return "", refuseAt(raw.TrackerAt, "`tracker`: %w. Fix the value.", err)
	}

	return tracker, nil
}

func claimsName(raw rawProject) (model.ClaimsName, error) {
	text, found, err := given(raw.ClaimsAt, "claims", raw.Claims)
	if err != nil || !found {
		return model.ClaimsLocal, err
	}

	claims, err := model.ParseClaims(text)
	if err != nil {
		return "", refuseAt(raw.ClaimsAt, "`claims`: %w. Fix the value.", err)
	}

	return claims, nil
}

func forgeName(raw rawProject) (model.ForgeName, error) {
	text, found, err := given(raw.ForgeAt, "forge", raw.Forge)
	if err != nil || !found {
		return "", err
	}

	forge, err := model.ParseForge(text)
	if err != nil {
		return "", refuseAt(raw.ForgeAt, "`forge`: %w. Fix the value.", err)
	}

	return forge, nil
}

// requireAdapterKeys checks the keys that the named adapters need: repo for GitHub, and the beads prefix for beads.
// A repo that is in the file is checked even when no adapter needs it.
func requireAdapterKeys(raw rawProject, names adapters) error {
	if names.tracker == model.TrackerGithub || names.forge == model.ForgeGithub || present(raw.RepoAt) {
		if err := requireRepo(raw); err != nil {
			return err
		}
	}

	if names.tracker == model.TrackerBeads || names.claims == model.ClaimsBeads {
		return requireText(raw.at(raw.BeadsPrefixAt), "beads_prefix", raw.BeadsPrefix)
	}

	return nil
}

func requireRepo(raw rawProject) error {
	if err := requireText(raw.at(raw.RepoAt), "repo", raw.Repo); err != nil {
		return err
	}

	owner, name, found := strings.Cut(raw.Repo, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return refuseAt(raw.at(raw.RepoAt), "`repo` must be written owner/name. Fix the value.")
	}

	return nil
}

// urlSource matches a git URL: a scheme with "://", or the scp form [user@]host:path. As git reads it, a colon before any slash makes the scp form.
var urlSource = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*://|([^/\s@:]+@)?[^/\s@:]+:)`)

// sourceOf checks the source of the project and returns it. A project with no forge must have one. A path expands a
// leading "~", must be absolute and needs a local build host, because a remote host cannot read a path on this machine.
func sourceOf(raw rawProject, forge model.ForgeName, host string) (string, error) {
	if !present(raw.SourceAt) {
		if forge == "" {
			return "", refuseAt(raw.At, "the project has no forge and no `source`. Set `source` to a git URL or an absolute path, or set `forge = \"github\"`.")
		}

		return "", nil
	}

	if err := requireText(raw.SourceAt, "source", raw.Source); err != nil {
		return "", err
	}

	if urlSource.MatchString(raw.Source) {
		return raw.Source, nil
	}

	return sourcePath(raw, host)
}

func sourcePath(raw rawProject, host string) (string, error) {
	path, err := expandHome(raw.Source)
	if err != nil {
		return "", err
	}

	if !filepath.IsAbs(path) {
		return "", refuseAt(raw.SourceAt, "`source` must be a git URL or an absolute path; got `%s`. Fix the value.", raw.Source)
	}

	if host != LocalHost {
		return "", refuseAt(raw.SourceAt, "a path `source` needs `build_host = %q`, because the host %q cannot read a path on this machine. Use a git URL for `source`.", LocalHost, host)
	}

	return path, nil
}
