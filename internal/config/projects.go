package config

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
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
	GateSteps   gateList
	BuildHost   hostNames
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

	Schedules schedules
}

// loadProject reads one project file. The project name is the file name without ".star".
func loadProject(path string, registry Adapters) (Project, error) {
	raw, err := evalProject(path)
	if err != nil {
		return Project{}, err
	}

	if raw == nil {
		return Project{}, refuse("%s: the file does not call `project`. Call project(source = ..., base_branch = ..., gate = ...) once.", path)
	}

	profile, err := profileOf(path, *raw, registry)
	if err != nil {
		return Project{}, err
	}

	return Project{Project: profile, Path: path}, nil
}

func projectSpec() spec {
	return spec{"project", []string{"base_branch?", "gate?", "gate_steps?", "repo?", "source?", "tracker?", "claims?", "forge?", "beads_prefix?", "build_host?", "host?", "test_report?", "pin_file?", "visibility?", "sandbox?", "sandbox_read?", "sandbox_deny?", "gate_network?", "gate_allow_write?", "sandbox_enforcer?", "people?", "epics?", "gate_files?", "conventions?", "brief_rules?", "stacked_rules?", "limits?", "traps?", "token_rules?", "changelog?", "board?", "schedules?"}}
}

func projectSpecs() []spec {
	return []spec{projectSpec(), hostSpec(), skypilotSpec(), sshSpec(), boardSpec(), scheduleSpec()}
}

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
		blocks(file, sshSpec(), true, newSSH),
		blocks(file, boardSpec(), true, newBoard),
		blocks(file, scheduleSpec(), false, newSchedule),
	)

	return raw, err
}

// newProject is the constructor of the project block. The tracker and the claim store default to local, and the build host to this machine.
func newProject(at syntax.Position, kwargs []starlark.Tuple) (*rawProject, error) {
	raw := &rawProject{At: at, Tracker: defaultTracker, Claims: defaultClaims, BuildHost: hostNames{LocalHost}}

	host := nested[rawHost]{want: hostSpec().name}
	board := nested[rawBoard]{want: boardSpec().name}

	err := projectSpec().unpack(at, kwargs, &raw.BaseBranch, &raw.Gate, &raw.GateSteps, &raw.Repo, &raw.Source, &raw.Tracker, &raw.Claims, &raw.Forge, &raw.BeadsPrefix, &raw.BuildHost, &host, &raw.TestReport, &raw.PinFile, &raw.Visibility, &raw.Sandbox, &raw.SandboxRead, &raw.SandboxDeny, &raw.GateNetwork, &raw.GateAllowWrite, &raw.SandboxEnforcer,
		&raw.People, &raw.Epics, &raw.GateFiles, &raw.Conventions, &raw.BriefRules, &raw.StackedRules, &raw.Limits, &raw.Traps, &raw.TokenRules, &raw.Changelog, &board, &raw.Schedules)
	raw.Host, raw.Board = host.block, board.block

	return raw, err
}

// profileOf checks the keys of a project file and returns the profile they describe.
func profileOf(path string, raw rawProject, registry Adapters) (model.Project, error) {
	names, err := adapterNames(raw, registry)
	if err != nil {
		return model.Project{}, err
	}

	host, source, err := checkKeys(raw, names)
	if err != nil {
		return model.Project{}, err
	}

	hostTable, visibility, sandbox, err := tables(host, raw, registry)
	if err != nil {
		return model.Project{}, err
	}

	return withFacts(path, model.Project{
		Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Repo: raw.Repo.Value, Source: source, Tracker: names.tracker, Claims: names.claims,
		Forge: names.forge, BeadsPrefix: raw.BeadsPrefix, Tools: toolsOf(names), BaseBranch: raw.BaseBranch, Gate: raw.Gate, GateSteps: slices.Clone(raw.GateSteps), BuildHost: host, BuildHosts: declaredHosts(raw.BuildHost), Host: hostTable, TestReport: raw.TestReport.Value,
		PinFile: raw.PinFile.Value, Visibility: visibility, Sandbox: sandbox, SandboxRead: slices.Clone(raw.SandboxRead), SandboxDeny: slices.Clone(raw.SandboxDeny), GateNetwork: slices.Clone(raw.GateNetwork), GateAllowWrite: slices.Clone(raw.GateAllowWrite), SandboxEnforcer: raw.SandboxEnforcer.Value,
	}, raw)
}

// checkKeys checks the keys that the named adapters need and returns the build host and the source.
func checkKeys(raw rawProject, names adapters) (string, string, error) {
	if err := requiredKeys(raw, names); err != nil {
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

	return host, source, errors.Join(requireText(raw.At, "base_branch", raw.BaseBranch), gateKeys(raw), testReport(raw), pinFile(raw))
}

// noRepositoryKeys checks a project with no source and no forge. Its items run in plain folders, so base_branch, gate and test_report are optional unless a gate is set.
// A pin_file is read from the base commit, so it needs a repository. A gate that is set needs a test_report: a gate with no test count passes with no test run.
func noRepositoryKeys(raw rawProject) error {
	if raw.PinFile.Set {
		return refuseAt(raw.At, "`pin_file` is set, and the project has no `source` and no `forge`. The pin file is read from the base commit. Set `source` or `forge`, or remove `pin_file`.")
	}

	if raw.Gate != "" || len(raw.GateSteps) > 0 {
		return errors.Join(gateKeys(raw), testReport(raw))
	}

	return nil
}

// tables returns the host block, the visibility and the sandbox mode of a project.
func tables(host string, raw rawProject, registry Adapters) (*model.HostConfig, model.Visibility, model.SandboxMode, error) {
	hostTable, err := hostConfig(host, raw, registry)
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

	return parseKey(raw.At, "visibility", raw.Visibility.Value, model.ParseVisibility)
}

// absolutePaths refuses the first path of sandbox_read or sandbox_deny that is neither absolute nor under ~/.
func absolutePaths(raw rawProject) error {
	for _, key := range []string{"sandbox_read", "sandbox_deny"} {
		list := raw.SandboxRead
		if key == "sandbox_deny" {
			list = raw.SandboxDeny
		}

		for _, path := range list {
			if !filepath.IsAbs(path) && !model.IsHomePath(path) {
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

	return parseKey(raw.At, "sandbox", raw.Sandbox.Value, model.ParseSandboxMode)
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

// pinFile refuses a pin_file that is set and empty, absolute or outside the work tree. An unset key is allowed. A gate on a build host checks the pins on the host.
func pinFile(raw rawProject) error {
	if !raw.PinFile.Set {
		return nil
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

// hostNames is the build_host argument: one name, or a list of names that a run may choose. The first name is the default of a run.
type hostNames []string

// Unpack stores a text as one name, or a list or a tuple of texts as the names. It refuses any other value.
func (h *hostNames) Unpack(v starlark.Value) error {
	if text, isText := starlark.AsString(v); isText {
		*h = hostNames{text}

		return nil
	}

	var names paths
	if err := names.Unpack(v); err != nil {
		return fmt.Errorf("got %s, want a name or a list of names", v.Type())
	}

	*h = hostNames(names)

	return nil
}

// declaredHosts returns the hosts of a project that names more than one. A project with one host has none to choose from: its BuildHost is the only one.
func declaredHosts(names hostNames) []string {
	if len(names) <= 1 {
		return nil
	}

	return slices.Clone(names)
}

// hostName checks one name of build_host against the names before it.
func hostName(raw rawProject, name string, earlier []string) error {
	if err := requireText(raw.At, "build_host", name); err != nil {
		return err
	}

	if !clusterName.MatchString(name) {
		return refuseAt(raw.At, "`build_host` must be %q or the name of a build host (a letter, then letters, digits, dots and hyphens); got `%s`. Fix the value.", LocalHost, name)
	}

	if slices.Contains(earlier, name) {
		return refuseAt(raw.At, "`build_host` lists `%s` twice. Remove one.", name)
	}

	return nil
}

// buildHost checks build_host and returns the default build host: its first name. A project that builds on this machine declares only that name.
// Every other name is a cluster that shares the host block, and is no repeat.
func buildHost(raw rawProject) (string, error) {
	if len(raw.BuildHost) == 0 {
		return "", refuseAt(raw.At, "`build_host` is an empty list. Name at least one build host.")
	}

	for i, name := range raw.BuildHost {
		if err := hostName(raw, name, raw.BuildHost[:i]); err != nil {
			return "", err
		}
	}

	if len(raw.BuildHost) > 1 && slices.Contains(raw.BuildHost, LocalHost) {
		return "", refuseAt(raw.At, "`build_host` lists %q with other hosts. A project that builds on this machine names only %q.", LocalHost, LocalHost)
	}

	return raw.BuildHost[0], nil
}

// adapters holds the adapters that a project names: their names and their registry entries.
type adapters struct {
	tracker model.TrackerName
	claims  model.ClaimsName
	forge   model.ForgeName
	// entries are the registry entries of the named adapters, in the order tracker, claims, forge. A kind that the project names none of has no entry.
	entries []*Entry
}

// adapterNames returns the tracker, the claim store and the forge that the project names, checked against the registry.
// A key that the call leaves out gives the local tracker, the local claim store and no forge.
func adapterNames(raw rawProject, registry Adapters) (adapters, error) {
	tracker, err := parseKey(raw.At, "tracker", raw.Tracker, registry.parseTracker)
	if err != nil {
		return adapters{}, err
	}

	claims, err := parseKey(raw.At, "claims", raw.Claims, registry.parseClaims)
	if err != nil {
		return adapters{}, err
	}

	forge, err := forgeName(raw, registry)
	if err != nil {
		return adapters{}, err
	}

	return adapters{tracker: tracker, claims: claims, forge: forge, entries: registry.entriesOf(tracker, claims, forge)}, nil
}

func forgeName(raw rawProject, registry Adapters) (model.ForgeName, error) {
	if !raw.Forge.Set {
		return "", nil
	}

	return parseKey(raw.At, "forge", raw.Forge.Value, registry.parseForge)
}

// The project keys that an adapter entry can require.
const (
	repoKey        = "repo"
	beadsPrefixKey = "beads_prefix"
)

// requiredKeys checks the project keys that the named adapters require, repo first. A repo that the call gives is checked even when no adapter needs it.
// A key that an entry requires and no check reads is refused, so that a registration cannot name a key that nothing enforces.
func requiredKeys(raw rawProject, names adapters) error {
	required := names.union(func(entry *Entry) []string { return entry.Keys })

	if required[repoKey] || raw.Repo.Set {
		if err := requireRepo(raw); err != nil {
			return err
		}
	}

	delete(required, repoKey)

	return requireOtherKeys(raw, required)
}

// requireOtherKeys checks the required keys besides repo. The beads prefix is the only one that a check reads.
func requireOtherKeys(raw rawProject, required map[string]bool) error {
	for _, key := range slices.Sorted(maps.Keys(required)) {
		if key != beadsPrefixKey {
			return refuseAt(raw.At, "an adapter of the project needs the key `%s`, and no check reads it. Report the adapter.", key)
		}

		if err := requireText(raw.At, beadsPrefixKey, raw.BeadsPrefix); err != nil {
			return err
		}
	}

	return nil
}

// union returns the set of the values that the entries of the named adapters give.
func (a adapters) union(values func(*Entry) []string) map[string]bool {
	set := map[string]bool{}

	for _, entry := range a.entries {
		for _, value := range values(entry) {
			set[value] = true
		}
	}

	return set
}

// toolsOf returns the programs that the named adapters need on the path: git first, then each adapter tool once, in name order.
func toolsOf(names adapters) []string {
	needed := names.union(func(entry *Entry) []string { return entry.Tools })

	delete(needed, model.ToolGit)

	return append([]string{model.ToolGit}, slices.Sorted(maps.Keys(needed))...)
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
