package config

import (
	"errors"
	"path/filepath"
	"regexp"
	"slices"
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
	PinFile     text
	Changelog   text
	Visibility  text
	Sandbox     text
	SandboxRead paths
	SandboxDeny paths

	GateNetwork    paths
	GateAllowWrite paths

	SandboxEnforcer text

	People                                                                      table
	Epics                                                                       table
	GateFiles, Conventions, BriefRules, StackedRules, Limits, Traps, TokenRules paths

	Board *rawBoard
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
	return spec{"project", []string{"base_branch?", "gate?", "repo?", "source?", "tracker?", "claims?", "forge?", "beads_prefix?", "build_host?", "host?", "test_report?", "pin_file?", "visibility?", "sandbox?", "sandbox_read?", "sandbox_deny?", "gate_network?", "gate_allow_write?", "sandbox_enforcer?", "people?", "epics?", "gate_files?", "conventions?", "brief_rules?", "stacked_rules?", "limits?", "traps?", "token_rules?", "changelog?", "board?"}}
}

func projectSpecs() []spec { return []spec{projectSpec(), hostSpec(), skypilotSpec(), boardSpec()} }

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
		blocks(file, boardSpec(), true, newBoard),
	)

	return raw, err
}

// newProject is the constructor of the project block. The tracker and the claim store default to local, and the build host to this machine.
func newProject(at syntax.Position, kwargs []starlark.Tuple) (*rawProject, error) {
	raw := &rawProject{At: at, Tracker: string(model.TrackerLocal), Claims: string(model.ClaimsLocal), BuildHost: LocalHost}

	host := nested[rawHost]{want: hostSpec().name}
	board := nested[rawBoard]{want: boardSpec().name}

	err := projectSpec().unpack(at, kwargs, &raw.BaseBranch, &raw.Gate, &raw.Repo, &raw.Source, &raw.Tracker, &raw.Claims, &raw.Forge, &raw.BeadsPrefix, &raw.BuildHost, &host, &raw.TestReport, &raw.PinFile, &raw.Visibility, &raw.Sandbox, &raw.SandboxRead, &raw.SandboxDeny, &raw.GateNetwork, &raw.GateAllowWrite, &raw.SandboxEnforcer,
		&raw.People, &raw.Epics, &raw.GateFiles, &raw.Conventions, &raw.BriefRules, &raw.StackedRules, &raw.Limits, &raw.Traps, &raw.TokenRules, &raw.Changelog, &board)
	raw.Host, raw.Board = host.block, board.block

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

	hostTable, visibility, sandbox, err := tables(host, raw)
	if err != nil {
		return model.Project{}, err
	}

	return withFacts(model.Project{
		Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Repo: raw.Repo.Value, Source: source, Tracker: names.tracker, Claims: names.claims,
		Forge: names.forge, BeadsPrefix: raw.BeadsPrefix, BaseBranch: raw.BaseBranch, Gate: raw.Gate, BuildHost: host, Host: hostTable, TestReport: raw.TestReport.Value,
		PinFile: raw.PinFile.Value, Visibility: visibility, Sandbox: sandbox, SandboxRead: slices.Clone(raw.SandboxRead), SandboxDeny: slices.Clone(raw.SandboxDeny), GateNetwork: slices.Clone(raw.GateNetwork), GateAllowWrite: slices.Clone(raw.GateAllowWrite), SandboxEnforcer: raw.SandboxEnforcer.Value,
	}, raw)
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

	source, err := sourceOf(raw, host)
	if err != nil {
		return "", "", err
	}

	if source == "" && names.forge == "" {
		return host, source, noRepositoryKeys(raw)
	}

	return host, source, errors.Join(requireText(raw.At, "base_branch", raw.BaseBranch), requireText(raw.At, "gate", raw.Gate), testReport(raw), pinFile(raw, host))
}

// noRepositoryKeys checks a project with no source and no forge. Its items run in plain folders, so base_branch, gate and test_report are optional unless a gate is set.
// A pin_file is read from the base commit, so it needs a repository. A gate that is set needs a test_report: a gate with no test count passes with no test run.
func noRepositoryKeys(raw rawProject) error {
	if raw.PinFile.Set {
		return refuseAt(raw.At, "`pin_file` is set, and the project has no `source` and no `forge`. The pin file is read from the base commit. Set `source` or `forge`, or remove `pin_file`.")
	}

	if raw.Gate != "" {
		return testReport(raw)
	}

	return nil
}

// tables returns the host block, the visibility and the sandbox mode of a project.
func tables(host string, raw rawProject) (*model.HostConfig, model.Visibility, model.SandboxMode, error) {
	hostTable, err := hostConfig(host, raw)
	if err != nil {
		return nil, "", "", err
	}

	visibility, err := visibilityOf(raw)
	if err != nil {
		return nil, "", "", err
	}

	sandbox, err := sandboxOf(raw)

	return hostTable, visibility, sandbox, err
}

// visibilityOf reads visibility. An unset key gives the empty visibility, which counts as public. A set key that is empty or unknown is refused.
func visibilityOf(raw rawProject) (model.Visibility, error) {
	if !raw.Visibility.Set {
		return "", nil
	}

	if err := requireText(raw.At, "visibility", raw.Visibility.Value); err != nil {
		return "", err
	}

	visibility, err := model.ParseVisibility(raw.Visibility.Value)
	if err != nil {
		return "", refuseAt(raw.At, "`visibility`: %w. Fix the value.", err)
	}

	return visibility, nil
}

// absolutePaths refuses the first path of sandbox_read or sandbox_deny that is neither absolute nor under ~/.
func absolutePaths(raw rawProject) error {
	for _, key := range []string{"sandbox_read", "sandbox_deny"} {
		list := raw.SandboxRead
		if key == "sandbox_deny" {
			list = raw.SandboxDeny
		}

		for _, path := range list {
			if !filepath.IsAbs(path) && path != "~" && !strings.HasPrefix(path, "~/") {
				return refuseAt(raw.At, "`%s` lists %q. Write an absolute path, or one that starts with ~/.", key, path)
			}
		}
	}

	if err := errors.Join(model.CheckSandboxRead(raw.SandboxRead), model.CheckNetwork(raw.GateNetwork), model.CheckAllowWrite(raw.GateAllowWrite)); err != nil {
		return refuseAt(raw.At, "%w.", err)
	}

	return nil
}

// sandboxOf reads sandbox. An unset key gives the empty mode, which runs the steps with no sandbox. A set key that is empty or unknown is refused.
// The sandbox_read and sandbox_deny paths must be absolute, or start with ~/, so that a path never depends on the folder of a step.
func sandboxOf(raw rawProject) (model.SandboxMode, error) {
	if err := sandboxKeys(raw); err != nil {
		return "", err
	}

	if !raw.Sandbox.Set {
		return "", nil
	}

	if err := requireText(raw.At, "sandbox", raw.Sandbox.Value); err != nil {
		return "", err
	}

	mode, err := model.ParseSandboxMode(raw.Sandbox.Value)
	if err != nil {
		return "", refuseAt(raw.At, "`sandbox`: %w. Fix the value.", err)
	}

	return mode, nil
}

// sandboxKeys refuses a relative sandbox path and a sandbox_enforcer that is set and empty.
func sandboxKeys(raw rawProject) error {
	if err := absolutePaths(raw); err != nil {
		return err
	}

	if raw.SandboxEnforcer.Set {
		return requireText(raw.At, "sandbox_enforcer", raw.SandboxEnforcer.Value)
	}

	return nil
}

// pinFile refuses a pin_file that is set and empty, or set for a gate on a build host. An unset key is allowed.
func pinFile(raw rawProject, host string) error {
	if !raw.PinFile.Set {
		return nil
	}

	if host != LocalHost {
		return refuseAt(raw.At, "`pin_file` is set with `build_host = %q`. The tool versions are read only for a local gate. Remove `pin_file` or set `build_host = %q`.", host, LocalHost)
	}

	if err := requireText(raw.At, "pin_file", raw.PinFile.Value); err != nil {
		return err
	}

	if filepath.IsAbs(raw.PinFile.Value) || slices.Contains(strings.Split(filepath.ToSlash(raw.PinFile.Value), "/"), "..") {
		return refuseAt(raw.At, "`pin_file` is %q. The pin file is read from the base commit, so its path must be inside the work tree. Use a relative path with no `..`.", raw.PinFile.Value)
	}

	return nil
}

// testReport refuses an unset test_report, and one that is set and empty. A gate with no test count would pass when it exits 0 with no test,
// on this machine and on a build host alike.
func testReport(raw rawProject) error {
	if !raw.TestReport.Set {
		return refuseAt(raw.At, "`test_report` is not set. A gate that sets no `test_report` has no test count, so a gate that exits 0 with no test would pass. Set `test_report` to a glob for the JUnit XML files of the gate, for example `test_report = \"reports/*.xml\"`.")
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

// sourceOf checks the source of the project and returns it. A project may have none: its clone URL then comes from its forge, or it has no repository. A path expands a
// leading "~", must be absolute and needs a local build host, because a remote host cannot read a path on this machine.
func sourceOf(raw rawProject, host string) (string, error) {
	if !raw.Source.Set {
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

// GateRefused is true when a gate runs on this machine only under a sandbox enforcer: the project has its sandbox on. The gate is refused when no enforcer can run here.
// A program gate (a gate call with run, or the gate_run of a batch merge) always runs here. The project gate runs here when the project builds here.
func GateRefused(project model.Project, program bool) bool {
	return project.SandboxByDefault() && (program || project.BuildHost == LocalHost)
}
