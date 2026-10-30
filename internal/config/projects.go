package config

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/obee/internal/model"
)

// Project is one project profile and the path of its file.
type Project struct {
	model.Project

	Path string
}

// rawProject is a project block as written. At is the position of its call; every refusal after the call points there.
// A key that the call leaves out has its default, and the keys with no default record whether the call gave them.
type rawProject struct {
	At          syntax.Position
	Repo        text
	Source      text
	Tracker     string
	Claims      string
	Forge       text
	BeadsPrefix string
	BaseBranch  string
	Gate        string
	BuildHost   string
	Host        *rawHost
	TestReport  text
}

// loadProject reads one project file. The project name is the file name without ".star".
func loadProject(path string) (Project, error) {
	raw, err := evalProject(path)
	if err != nil {
		return Project{}, err
	}

	if raw == nil {
		return Project{}, refuse("%s: the file does not call `project`. Call project(source = ..., base_branch = ..., gate = ...) once.", path)
	}

	profile, err := profileOf(path, *raw)
	if err != nil {
		return Project{}, err
	}

	return Project{Project: profile, Path: path}, nil
}

func projectSpec() spec {
	return spec{"project", []string{"base_branch", "gate", "repo?", "source?", "tracker?", "claims?", "forge?", "beads_prefix?", "build_host?", "host?", "test_report?"}}
}

func projectSpecs() []spec { return []spec{projectSpec(), hostSpec(), skypilotSpec()} }

// evalProject evaluates a project file and returns the one project block that it calls.
func evalProject(path string) (*rawProject, error) {
	file := newLoader(path, projectSpecs())

	var raw *rawProject

	err := file.run(
		file.constructor(projectSpec(), true, func(at syntax.Position, kwargs []starlark.Tuple) error {
			var err error

			raw, err = newProject(at, kwargs)

			return err
		}),
		blocks(file, hostSpec(), true, newHost),
		blocks(file, skypilotSpec(), true, newSkyPilot),
	)

	return raw, err
}

// newProject is the constructor of the project block. The tracker and the claim store default to local, and the build host to this machine.
func newProject(at syntax.Position, kwargs []starlark.Tuple) (*rawProject, error) {
	raw := &rawProject{At: at, Tracker: string(model.TrackerLocal), Claims: string(model.ClaimsLocal), BuildHost: LocalHost}

	host := nested[rawHost]{want: hostSpec().name}

	err := projectSpec().unpack(at, kwargs, &raw.BaseBranch, &raw.Gate, &raw.Repo, &raw.Source, &raw.Tracker, &raw.Claims, &raw.Forge, &raw.BeadsPrefix, &raw.BuildHost, &host, &raw.TestReport)
	raw.Host = host.block

	return raw, err
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
		Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Repo: raw.Repo.Value, Source: source, Tracker: names.tracker, Claims: names.claims,
		Forge: names.forge, BeadsPrefix: raw.BeadsPrefix, BaseBranch: raw.BaseBranch, Gate: raw.Gate, BuildHost: host, Host: hostTable, TestReport: raw.TestReport.Value,
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

	return host, source, errors.Join(requireText(raw.At, "base_branch", raw.BaseBranch), requireText(raw.At, "gate", raw.Gate), testReport(raw, host))
}

// testReport refuses a test_report that is set and empty, or set for a gate on a build host. An unset key is allowed.
func testReport(raw rawProject, host string) error {
	if !raw.TestReport.Set {
		return nil
	}

	if host != LocalHost {
		return refuseAt(raw.At, "`test_report` is set with `build_host = %q`. The report is read only from a local gate. Remove `test_report` or set `build_host = %q`.", host, LocalHost)
	}

	return requireText(raw.At, "test_report", raw.TestReport.Value)
}

func buildHost(raw rawProject) (string, error) {
	if err := requireText(raw.At, "build_host", raw.BuildHost); err != nil {
		return "", err
	}

	if !clusterName.MatchString(raw.BuildHost) {
		return "", refuseAt(raw.At, "`build_host` must be %q or a SkyPilot cluster name (a letter, then letters, digits and hyphens); got `%s`. Fix the value.",
			LocalHost, raw.BuildHost)
	}

	return raw.BuildHost, nil
}

// adapters holds the adapters that a project names.
type adapters struct {
	tracker model.TrackerName
	claims  model.ClaimsName
	forge   model.ForgeName
}

// adapterNames returns the tracker, the claim store and the forge that the project names. A key that the call leaves out
// gives the local tracker, the local claim store and no forge. A key that the call gives must name an adapter.
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

func trackerName(raw rawProject) (model.TrackerName, error) {
	if err := requireText(raw.At, "tracker", raw.Tracker); err != nil {
		return "", err
	}

	tracker, err := model.ParseTracker(raw.Tracker)
	if err != nil {
		return "", refuseAt(raw.At, "`tracker`: %w. Fix the value.", err)
	}

	return tracker, nil
}

func claimsName(raw rawProject) (model.ClaimsName, error) {
	if err := requireText(raw.At, "claims", raw.Claims); err != nil {
		return "", err
	}

	claims, err := model.ParseClaims(raw.Claims)
	if err != nil {
		return "", refuseAt(raw.At, "`claims`: %w. Fix the value.", err)
	}

	return claims, nil
}

func forgeName(raw rawProject) (model.ForgeName, error) {
	if !raw.Forge.Set {
		return "", nil
	}

	if err := requireText(raw.At, "forge", raw.Forge.Value); err != nil {
		return "", err
	}

	forge, err := model.ParseForge(raw.Forge.Value)
	if err != nil {
		return "", refuseAt(raw.At, "`forge`: %w. Fix the value.", err)
	}

	return forge, nil
}

// requireAdapterKeys checks the keys that the named adapters need: repo for GitHub, and the beads prefix for beads.
// A repo that the call gives is checked even when no adapter needs it.
func requireAdapterKeys(raw rawProject, names adapters) error {
	if names.tracker == model.TrackerGithub || names.forge == model.ForgeGithub || raw.Repo.Set {
		if err := requireRepo(raw); err != nil {
			return err
		}
	}

	if names.tracker == model.TrackerBeads || names.claims == model.ClaimsBeads {
		return requireText(raw.At, "beads_prefix", raw.BeadsPrefix)
	}

	return nil
}

func requireRepo(raw rawProject) error {
	if err := requireText(raw.At, "repo", raw.Repo.Value); err != nil {
		return err
	}

	owner, name, found := strings.Cut(raw.Repo.Value, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return refuseAt(raw.At, "`repo` must be written owner/name. Fix the value.")
	}

	return nil
}

// urlSource matches a git URL: a scheme with "://", or the scp form [user@]host:path. As git reads it, a colon before any slash makes the scp form.
var urlSource = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*://|([^/\s@:]+@)?[^/\s@:]+:)`)

// sourceOf checks the source of the project and returns it. A project with no forge must have one. A path expands a
// leading "~", must be absolute and needs a local build host, because a remote host cannot read a path on this machine.
func sourceOf(raw rawProject, forge model.ForgeName, host string) (string, error) {
	if !raw.Source.Set {
		if forge == "" {
			return "", refuseAt(raw.At, "the project has no forge and no `source`. Set `source` to a git URL or an absolute path, or set `forge = \"github\"`.")
		}

		return "", nil
	}

	if err := requireText(raw.At, "source", raw.Source.Value); err != nil {
		return "", err
	}

	if urlSource.MatchString(raw.Source.Value) {
		return raw.Source.Value, nil
	}

	return sourcePath(raw, host)
}

func sourcePath(raw rawProject, host string) (string, error) {
	path, err := expandHome(raw.Source.Value)
	if err != nil {
		return "", err
	}

	if !filepath.IsAbs(path) {
		return "", refuseAt(raw.At, "`source` must be a git URL or an absolute path; got `%s`. Fix the value.", raw.Source.Value)
	}

	if host != LocalHost {
		return "", refuseAt(raw.At, "a path `source` needs `build_host = %q`, because the host %q cannot read a path on this machine. Use a git URL for `source`.", LocalHost, host)
	}

	return path, nil
}
