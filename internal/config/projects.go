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
	Blocks      map[string]*model.Block
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
	raw, err := evalProject(path, registry)
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

func projectSpec(blocks []Block) spec {
	args := append([]string{"base_branch?", "gate?", "gate_steps?", "repo?", "source?", "tracker?", "claims?", "forge?", "build_host?", "host?", "test_report?", "pin_file?", "visibility?", "sandbox?", "sandbox_read?", "sandbox_deny?", "gate_network?", "gate_allow_write?", "sandbox_enforcer?", "people?", "epics?", "gate_files?", "conventions?", "brief_rules?", "stacked_rules?", "limits?", "traps?", "token_rules?", "changelog?", "board?", "schedules?"},
		blockArgs(blocks)...)

	return spec{"project", args}
}

func projectSpecs(projectBlocks, hostBlocks []Block) []spec {
	specs := []spec{projectSpec(projectBlocks), hostSpec(hostBlocks), boardSpec(), scheduleSpec()}

	for _, declared := range projectBlocks {
		specs = append(specs, declared.spec())
	}

	for _, declared := range hostBlocks {
		specs = append(specs, declared.spec())
	}

	return specs
}

// evalProject evaluates a project file and returns the one project block that it calls.
func evalProject(path string, registry Adapters) (*rawProject, error) {
	projectBlocks, err := registry.projectBlocks()
	if err != nil {
		return nil, err
	}

	hostBlocks, err := registry.hostBlocks()
	if err != nil {
		return nil, err
	}

	file := newLoader(path, projectSpecs(projectBlocks, hostBlocks))

	var raw *rawProject

	project := file.constructor(projectSpec(projectBlocks), true, func(at syntax.Position, kwargs []starlark.Tuple) error {
		var built error

		raw, built = newProject(at, kwargs, projectBlocks)

		return built
	})

	host := blocks(file, hostSpec(hostBlocks), true, newHost(hostBlocks))
	board := blocks(file, boardSpec(), true, newBoard)
	schedules := blocks(file, scheduleSpec(), false, newSchedule)

	builtins := append([]*starlark.Builtin{project, host, board, schedules}, blockBuiltins(file, projectBlocks, hostBlocks)...)

	err = file.run(builtins...)

	return raw, err
}

// blockBuiltins makes the builtin of every adapter block that the registry declares: the project blocks and the host blocks.
func blockBuiltins(file *loader, blocks, hostBlocks []Block) []*starlark.Builtin {
	builtins := make([]*starlark.Builtin, 0, len(blocks)+len(hostBlocks))

	for _, declared := range blocks {
		builtins = append(builtins, blockBuiltin(file, declared))
	}

	for _, declared := range hostBlocks {
		builtins = append(builtins, blockBuiltin(file, declared))
	}

	return builtins
}

// blockBuiltin makes the builtin of one adapter block: the call gives the typed block, and another block holds it.
func blockBuiltin(file *loader, declared Block) *starlark.Builtin {
	return blocks(file, declared.spec(), true, func(at syntax.Position, kwargs []starlark.Tuple) (*model.Block, error) {
		return blockBuilt(at, declared, kwargs)
	})
}

// newProject is the constructor of the project block. The tracker and the claim store default to local, and the build host to this machine.
func newProject(at syntax.Position, kwargs []starlark.Tuple, blocks []Block) (*rawProject, error) {
	raw := &rawProject{At: at, Tracker: defaultTracker, Claims: defaultClaims, BuildHost: hostNames{LocalHost}, Blocks: map[string]*model.Block{}}

	host := nested[rawHost]{want: hostSpecName}
	board := nested[rawBoard]{want: boardSpec().name}
	blockDests := make([]*nested[model.Block], len(blocks))
	for i, declared := range blocks {
		blockDests[i] = &nested[model.Block]{want: declared.Name}
	}

	dests := []any{&raw.BaseBranch, &raw.Gate, &raw.GateSteps, &raw.Repo, &raw.Source, &raw.Tracker, &raw.Claims, &raw.Forge, &raw.BuildHost, &host, &raw.TestReport, &raw.PinFile, &raw.Visibility, &raw.Sandbox, &raw.SandboxRead, &raw.SandboxDeny, &raw.GateNetwork, &raw.GateAllowWrite, &raw.SandboxEnforcer,
		&raw.People, &raw.Epics, &raw.GateFiles, &raw.Conventions, &raw.BriefRules, &raw.StackedRules, &raw.Limits, &raw.Traps, &raw.TokenRules, &raw.Changelog, &board, &raw.Schedules}
	for _, dest := range blockDests {
		dests = append(dests, dest)
	}

	err := projectSpec(blocks).unpack(at, kwargs, dests...)
	raw.Host, raw.Board = host.block, board.block

	for i, declared := range blocks {
		if blockDests[i].block != nil {
			raw.Blocks[declared.Name] = blockDests[i].block
		}
	}

	return raw, err
}

// profileOf checks the keys of a project file and returns the profile they describe.
func profileOf(path string, raw rawProject, registry Adapters) (model.Project, error) {
	names, err := adapterNames(raw, registry)
	if err != nil {
		return model.Project{}, err
	}

	if err := blockRules(raw, names); err != nil {
		return model.Project{}, err
	}

	host, source, err := checkKeys(raw, names)
	if err != nil {
		return model.Project{}, err
	}

	hostTable, err := hostConfig(host, raw, registry)
	if err != nil {
		return model.Project{}, err
	}

	visibility, sandbox, err := tables(raw)
	if err != nil {
		return model.Project{}, err
	}

	return withFacts(path, model.Project{
		Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Repo: raw.Repo.Value, Source: source, Tracker: names.tracker, Claims: names.claims,
		Forge: names.forge, Block: blockOf(raw, names), Tools: toolsOf(names), BaseBranch: raw.BaseBranch, Gate: raw.Gate, GateSteps: slices.Clone(raw.GateSteps), BuildHost: host, BuildHosts: declaredHosts(raw.BuildHost), Host: hostTable, TestReport: raw.TestReport.Value,
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

// tables returns the visibility and the sandbox mode of a project.
func tables(raw rawProject) (model.Visibility, model.SandboxMode, error) {
	visibility, err := visibilityOf(raw)
	if err != nil {
		return "", "", err
	}

	sandbox, err := sandboxOf(raw)

	return visibility, sandbox, err
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
	// entries are the registry entries of the named adapters, in the order tracker, claims, forge. A nil entry is a kind the project names none of.
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

	entries := []*Entry{registry.Trackers[tracker], registry.Claims[claims]}
	if forge != "" {
		entries = append(entries, registry.Forges[forge])
	}

	return adapters{tracker: tracker, claims: claims, forge: forge, entries: entries}, nil
}

func forgeName(raw rawProject, registry Adapters) (model.ForgeName, error) {
	if !raw.Forge.Set {
		return "", nil
	}

	return parseKey(raw.At, "forge", raw.Forge.Value, registry.parseForge)
}

// requiredKeys checks the project keys that the named adapters require. A repo that the call gives is checked even when no adapter needs it.
func requiredKeys(raw rawProject, names adapters) error {
	needsRepo := false

	for _, entry := range names.entries {
		for _, key := range entry.Keys {
			if key == repoKey {
				needsRepo = true

				continue
			}

			if err := projectKey(raw, key); err != nil {
				return err
			}
		}
	}

	if needsRepo || raw.Repo.Set {
		return requireRepo(raw)
	}

	return nil
}

// repoKey is the project key that names the repository of the forge and of the GitHub tracker.
const repoKey = "repo"

// projectKey refuses a project key that a named adapter needs and the config does not read.
func projectKey(raw rawProject, key string) error {
	return refuseAt(raw.At, "an adapter of the project needs the key `%s`, and no check reads it. Report the adapter.", key)
}

// blockRules checks the adapter blocks of the project: a named adapter whose block has a required argument must hold its block,
// and a held block must belong to a named adapter.
func blockRules(raw rawProject, names adapters) error {
	for _, entry := range names.entries {
		if entry.Block == nil || !entry.Block.required() {
			continue
		}

		if _, held := raw.Blocks[entry.Block.Name]; !held {
			return refuseAt(raw.At, "the project names an adapter whose `%s` block it must hold. Write %s(...).", entry.Block.Name, entry.Block.Name)
		}
	}

	for _, name := range heldBlocks(raw) {
		if !names.takeBlock(name) {
			return refuseAt(raw.At, "the project holds the `%s` block, and no adapter it names takes that block. Remove the block, or name the adapter that takes it.", name)
		}
	}

	return nil
}

// heldBlocks returns the names of the blocks the project holds, in name order.
func heldBlocks(raw rawProject) []string {
	return slices.Sorted(maps.Keys(raw.Blocks))
}

// takeBlock says whether one of the named adapters declares the block.
func (a adapters) takeBlock(name string) bool {
	for _, entry := range a.entries {
		if entry.Block != nil && entry.Block.Name == name {
			return true
		}
	}

	return false
}

// blockOf returns the adapter block that the project holds for a named adapter that declares one.
func blockOf(raw rawProject, names adapters) *model.Block {
	for _, entry := range names.entries {
		if entry.Block == nil {
			continue
		}

		if held, ok := raw.Blocks[entry.Block.Name]; ok {
			return held
		}
	}

	return nil
}

// toolsOf returns the programs that the named adapters need on the path: git first, then each adapter tool once, in name order.
func toolsOf(names adapters) []string {
	needed := map[string]bool{}

	for _, entry := range names.entries {
		for _, tool := range entry.Tools {
			needed[tool] = true
		}
	}

	tools := []string{model.ToolGit}

	return append(tools, slices.Sorted(maps.Keys(needed))...)
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
