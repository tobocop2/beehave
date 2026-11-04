// Package gate runs the gate command of a project, on this machine or on its build host, and says whether the tree passed.
package gate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	// reportLimit is the most bytes of output that a Result keeps. The tail holds the findings.
	reportLimit = model.ReportLimit

	// signalBase is the exit code that a shell reports for a command that a signal ended: 128 plus the signal number.
	signalBase = 128

	// plainRune holds the characters of a word in the folder name of a clone.
	plainRune = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-"

	// clonePrefix starts the name of every clone folder of a host gate.
	clonePrefix = "beehave-gate-"

	// cloneMarker is the line that the job writes when the gate has ended, before it deletes the clone.
	cloneMarker = "beehave-gate-done"

	// noTestsMarker is the line that the job writes when the gate command passed and the report glob matched no test case.
	noTestsMarker = "beehave-gate-no-tests"

	// stepMarker starts the line that the job of a gate list writes after each step: the name of the step and its exit code.
	stepMarker = "beehave-gate-step"

	// trackedMarker starts the line that the job writes when the repository tracks a file that the report glob matches. The file name follows.
	trackedMarker = "beehave-gate-tracked-report"
)

// ExitTimedOut is the code that a Shell returns for a command that the time limit of the shell itself killed. It is not an exit code of the command,
// so a command that exits 124 on its own is a failure, and a command that the limit killed is unresolved.
const ExitTimedOut = -2

// ErrTreeChanged is the cause when the tree is not the clean commit that the gate must judge.
var ErrTreeChanged = errors.New("the tree is not the commit the gate judges")

// ErrNoVerdict is the cause when a host job ends in a state that says nothing about the gate: pending, running, canceled, lost or unknown.
// A signal, the time limit, and a host job that died, went stale or ran past its limit give a void result instead.
var ErrNoVerdict = errors.New("the gate job gave no verdict")

// ErrNoHead is the cause when a resumed host job comes without the head it was submitted with.
var ErrNoHead = errors.New("the resumed gate job has no head")

// ErrNoCloneURL is the cause when a host gate has no URL to clone the repository from.
var ErrNoCloneURL = errors.New("the host gate has no clone URL")

// ErrCloneOutsideRoot is the cause when the clone folder of a host gate would not be directly below the work root of the host.
var ErrCloneOutsideRoot = errors.New("the clone folder is not below the work root of the host")

// Shell runs a command line in dir and returns its output and exit code. A signal ends it as 128 plus the signal.
type Shell func(ctx context.Context, dir, command string) (string, int)

// State is the commit of a tree and whether the tree has no uncommitted change.
type State struct {
	Head  string
	Tree  string
	Clean bool
}

// Tree reads the state of the tree in dir.
type Tree func(ctx context.Context, dir string) (State, error)

// Remote is the build host of a project, as far as the gate needs it.
type Remote interface {
	Submit(ctx context.Context, spec model.JobSpec) (model.JobID, error)
	Wait(ctx context.Context, id model.JobID) (model.JobState, error)
	Cancel(ctx context.Context, id model.JobID) error
	WorkRoot(ctx context.Context) (string, error)
}

// Hosts returns the build host of a project.
type Hosts func(project model.Project) (Remote, error)

// Result is the outcome of a gate that ran. Report is the tail of its output. Head is the commit the gate judged.
// Tree is the tree id of that commit, or empty when the run did not read the tree of the commit it judged.
// Steps holds one result for each step. Passed is true only when every step is a pass.
type Result struct {
	Passed bool
	Report string
	Head   string
	Tree   string
	Steps  []Step
}

// Request is one gate run. Resume names a host job that an earlier run submitted; the run reads that job again and submits none.
// ResumeHead is the head that the resumed job was submitted with, and it is the head of the result. A Resume without it is refused.
// Submitted receives the id of a new host job as soon as the host accepts it, so a caller can keep it.
// CloneURL is the URL that a host gate clones the repository from. A host gate without it is refused.
type Request struct {
	Project    model.Project
	Dir        string
	CloneURL   string
	Resume     model.JobID
	ResumeHead string
	// Plan is the plan that the resumed job was submitted with. Run judges the job by it, not by the project of the request:
	// the gate, the list, the test report, the build host and the pin check come from the Plan. A zero Plan leaves the project as it is.
	Plan      Plan
	Submitted func(model.JobID)

	// pins are the pinned tools that the probe in front of each gate command checks. Run and Submit read them from the pin file at the base commit.
	pins []Pin
}

// Gate runs gates.
type Gate struct {
	shell Shell
	tree  Tree
	hosts Hosts
}

// New returns a gate that runs a local command with shell, reads trees with tree and finds the build host with hosts.
func New(shell Shell, tree Tree, hosts Hosts) Gate {
	return Gate{shell: shell, tree: tree, hosts: hosts}
}

// Run runs the gate command of the project in dir. A tree with uncommitted changes, or a head or tree that changes during the run, is an error:
// the gate must judge the commit that beehave sends on. A gate on a build host clones the repository there and checks out that commit,
// so the host never judges a tree that beehave did not send. Dir is read for the local gate and for the head only.
func (g Gate) Run(ctx context.Context, request Request) (Result, error) {
	request.Project = request.Plan.apply(request.Project)

	before, err := g.clean(ctx, request.Dir)
	if err != nil {
		return Result{}, err
	}

	result, err := g.execute(ctx, request, before.Head)
	if err != nil {
		return Result{}, err
	}

	if result.Head == before.Head {
		result.Tree = before.Tree
	}

	return result, g.unchanged(ctx, request.Dir, before)
}

// Job is the host job that Submit started, with the head that it judges. A local gate has no host job, so its Job has a Plan and no ID.
// Plan is the plan of the project when Submit made the job text. Run judges the job by it.
type Job struct {
	ID   model.JobID
	Head string
	Plan Plan
}

// Submit starts the host job of a gate and returns without waiting for it. A caller records the Job, then passes it to Run as Resume and ResumeHead:
// a run that starts again from the record reads the same job and submits none. The tree must be clean, as for Run. A local gate starts nothing.
func (g Gate) Submit(ctx context.Context, request Request) (Job, error) {
	before, err := g.clean(ctx, request.Dir)
	if err != nil {
		return Job{}, err
	}

	if request.Project.BuildHost == config.LocalHost {
		_, plan := g.planned(ctx, request)

		return Job{Plan: plan}, nil
	}

	return g.submitHost(ctx, request, before.Head)
}

func (g Gate) submitHost(ctx context.Context, request Request, head string) (Job, error) {
	host, err := g.hosts(request.Project)
	if err != nil {
		return Job{}, fmt.Errorf("project %s builds on %s: %w", request.Project.Name, request.Project.BuildHost, err)
	}

	// A pin file that cannot be used starts no job. The Plan holds the reason, and Run reports the void gate.
	request, plan := g.planned(ctx, request)
	if plan.Pins.Void != "" {
		return Job{Plan: plan}, nil
	}

	id, head, err := job(ctx, host, request, head)

	return Job{ID: id, Head: head, Plan: plan}, err
}

// planned reads the pin file of the project, once, and returns the request with the pinned tools and the Plan that holds the project values and the pin check.
func (g Gate) planned(ctx context.Context, request Request) (Request, Plan) {
	check := g.pinsOf(ctx, request)
	request.pins = check.Tools

	plan := PlanOf(request.Project)
	plan.Pins = &check

	return request, plan
}

// withPins returns the request with the pinned tools of the Plan, and the reason why the pin file cannot be used, or empty.
// A Plan that has no pin check (no Submit made it) reads the pin file now, from the project of the request.
func (g Gate) withPins(ctx context.Context, request Request) (Request, string) {
	check := request.Plan.Pins
	if check == nil {
		read := g.pinsOf(ctx, request)
		check = &read
	}

	request.pins = check.Tools

	return request, check.Void
}

func (g Gate) unchanged(ctx context.Context, dir string, before State) error {
	after, err := g.read(ctx, dir)
	if err == nil && after != before {
		return fmt.Errorf("%w: %s changed while the gate ran. Run the gate again", ErrTreeChanged, dir)
	}

	return err
}

func (g Gate) read(ctx context.Context, dir string) (State, error) {
	state, err := g.tree(ctx, dir)
	if err != nil {
		return State{}, fmt.Errorf("read the tree %s: %w", dir, err)
	}

	return state, nil
}

func (g Gate) clean(ctx context.Context, dir string) (State, error) {
	state, err := g.read(ctx, dir)
	if err == nil && !state.Clean {
		return State{}, fmt.Errorf("%w: %s has changes that are not committed. Commit or discard them, then run the gate again", ErrTreeChanged, dir)
	}

	return state, err
}

func (g Gate) execute(ctx context.Context, request Request, head string) (Result, error) {
	request, reason := g.withPins(ctx, request)
	if reason != "" {
		return voided(request, head, reason), nil
	}

	if request.Project.BuildHost != config.LocalHost {
		return g.remote(ctx, request, head)
	}

	return g.local(ctx, request, head)
}

func (g Gate) local(ctx context.Context, request Request, head string) (Result, error) {
	before := takeSnapshot(request.Dir, request.Project.TestReport)
	ran, abort := g.runSpecs(ctx, request)

	if stopped, result, err := stoppedGate(ctx, request, head, ran, abort); stopped {
		return result, err
	}

	result := Result{Report: tail(ran.output), Head: head, Steps: ran.steps}

	if request.Project.TestReport != "" {
		result = withTestsStep(result, request, ran.first, ran.output, before)
	}

	result.Passed = allPass(result.Steps)

	return result, nil
}

// stoppedGate gives the result of a local gate that did not run to the end: a pinned tool at another version, a signal, or the time limit.
// The first result is false for a gate that ran every step.
func stoppedGate(ctx context.Context, request Request, head string, ran specRun, abort int) (bool, Result, error) {
	switch {
	case ran.void != "":
		return true, voided(request, head, ran.void), nil
	case abort > signalBase:
		result, err := signaled(ctx, request.Project, head, ran.output, abort)

		return true, result, err
	case abort == ExitTimedOut:
		return true, timedOut(request, head, ran.output), nil
	}

	return false, Result{}, nil
}

// specRun is what the steps of a gate list left: the result of each step, their output, and the first exit code that was not 0.
type specRun struct {
	steps  []Step
	output string
	first  int
	// void is the reason when the probe of a pinned tool stopped the step, else empty.
	void string
}

// runSpecs runs the steps of the gate list in order. A failed step does not stop the later steps: the gate counts every red.
// A signal or the time limit stops them, and the code of that stop is the second result, else 0.
func (g Gate) runSpecs(ctx context.Context, request Request) (specRun, int) {
	specs := Specs(request.Project)
	ran := specRun{steps: make([]Step, 0, len(specs)+1)}

	for _, spec := range specs {
		out, code := g.shell(ctx, request.Dir, probed(request.pins, spec.Command))

		if ran.add(spec, len(specs), out, code) {
			return ran, code
		}
	}

	return ran, 0
}

// add keeps the result of one step. It is true when the step stopped the gate: a probe that found a tool at another version, a signal, or the time limit.
func (r *specRun) add(spec model.GateSpec, count int, out string, code int) bool {
	r.output += labeled(spec, count, out)

	if code == voidCode {
		r.void = voidReason(out)
	}

	if r.void != "" || code > signalBase || code == ExitTimedOut {
		return true
	}

	if r.first == 0 {
		r.first = code
	}

	r.steps = append(r.steps, Step{Name: spec.Name, Status: exitStatus(code)})

	return false
}

// labeled is the output of one step. In a list of more than one step it starts with the name of the step, so the report says which step wrote it.
func labeled(spec model.GateSpec, count int, output string) string {
	if count == 1 {
		return output
	}

	return "== " + spec.Name + " ==\n" + output + "\n"
}

// voided is the result of a gate that did not run because a pinned tool is not at its pinned version. Every step is unknown: neither a pass nor a fail.
func voided(request Request, head, reason string) Result {
	return Result{Report: tail("the gate did not run: " + reason + ". Install the pinned version, then run the gate again.\n"), Head: head, Steps: unknownSteps(request.Project)}
}

// timedOut is the result of a gate that the time limit of the shell killed. The command did not finish, so it is neither green nor red:
// every step is unknown, and the report keeps the output up to the kill.
func timedOut(request Request, head, output string) Result {
	note := "the gate command ran past its time limit and was killed. The gate has no result. Run the gate again, or raise the limit.\n"

	return aborted(request.Project, head, output+"\n"+note)
}

// signaled is the result of a gate command that a signal killed: every step is unknown, and the report keeps the output up to the kill.
// A stopped run kills the shell too. That kill ends the run, so it is the error of the context and not a gate that gave no result.
func signaled(ctx context.Context, project model.Project, head, output string, code int) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("the gate command was stopped with the run: %w", err)
	}

	note := fmt.Sprintf("the gate command was killed by a signal (exit code %d). A killed gate is neither green nor red. Run the gate again.\n", code)

	return aborted(project, head, output+"\n"+note), nil
}

// aborted is the result of a gate that a signal, the time limit or a host state stopped before it finished. Every step is unknown, so the
// review loop reads a void gate and runs it again, as it does for a gate that wrote no report.
func aborted(project model.Project, head, report string) Result {
	return Result{Report: tail(report), Head: head, Steps: unknownSteps(project)}
}

// unknownSteps is one step of unknown for each step that the gate of the project gives: the command, and the tests when the project sets test_report.
func unknownSteps(project model.Project) []Step {
	return hostSteps(project, StatusUnknown, StatusUnknown)
}

// exitStatus is the result of a command from its exit code.
func exitStatus(code int) Status {
	if code == 0 {
		return StatusPass
	}

	return StatusFail
}

// withTestsStep adds the tests step. The report is read only after a gate that exited 0. Before that, the tests step has no result line.
func withTestsStep(result Result, request Request, code int, output string, before snapshot) Result {
	tests, reason := StatusUnknown, ""
	if code == 0 {
		tests, reason = judge(request.Dir, request.Project.TestReport, before)
	}

	if reason != "" {
		result.Report = tail(output + "\n" + reason + "\n")
	}

	result.Steps = append(result.Steps, Step{Name: StepTests, Status: tests})

	return result
}

// allPass is true when every step is a pass. A list with no step is not a pass.
func allPass(steps []Step) bool {
	for _, s := range steps {
		if s.Status != StatusPass {
			return false
		}
	}

	return len(steps) > 0
}

func (g Gate) remote(ctx context.Context, request Request, head string) (Result, error) {
	project := request.Project

	host, err := g.hosts(project)
	if err != nil {
		return Result{}, fmt.Errorf("project %s builds on %s: %w", project.Name, project.BuildHost, err)
	}

	id, head, err := job(ctx, host, request, head)
	if err != nil {
		return Result{}, err
	}

	return wait(ctx, host, project, id, head)
}

// wait waits for the host job and maps its state to a result for head.
func wait(ctx context.Context, host Remote, project model.Project, id model.JobID, head string) (Result, error) {
	cluster := project.BuildHost

	state, err := host.Wait(ctx, id)
	if err != nil {
		// A wait that ends because the context ended leaves the job running on the host: stop it, so a stopped gate frees the host.
		if ctx.Err() != nil {
			err = errors.Join(err, stopJob(ctx, host, cluster, id))
		}

		return Result{}, fmt.Errorf("wait for the gate on %s: %w", cluster, err)
	}

	result, err := verdict(ctx, project, head, state)

	// The log of every failed job is evidence: it holds the void line of a pin mismatch, which no setting of the project predicts.
	if err == nil && state == model.JobFailed {
		result = explainFailure(ctx, host, project, id, result)
	}

	result.Head = head

	return result, err
}

// LogReader is a Remote that can read the log of a job. A host that does not have it gives a failed job a report that names both causes.
type LogReader interface {
	Log(ctx context.Context, id model.JobID) (string, error)
}

// explainFailure reads the marker lines in the log of a failed job and says which step failed. The job writes a marker when the report check
// refuses (a tracked report file) or fails (no test case), so the log tells those apart from a gate command that failed.
// A log that cannot be read leaves the result as the job state gave it, with a report that names both causes.
func explainFailure(ctx context.Context, host Remote, project model.Project, id model.JobID, result Result) Result {
	reader, ok := host.(LogReader)
	if !ok {
		return withCauses(result, project)
	}

	log, err := reader.Log(ctx, id)
	if err != nil {
		return withCauses(result, project)
	}

	if marked, found := markedResult(log, project); found {
		return marked
	}

	if len(project.GateSteps) > 0 {
		result.Steps = stepMarkerResults(log, project)
	}

	return result
}

// markedResult is the result that the first marker line in the log gives, and whether the log holds one.
func markedResult(log string, project model.Project) (Result, bool) {
	if reason := voidReason(log); reason != "" {
		return Result{Report: tail("the gate did not run: " + reason + ". Install the pinned version on " + project.BuildHost + ", then run the gate again.\n"), Steps: unknownSteps(project)}, true
	}

	for _, line := range strings.Split(log, "\n") {
		if file, found := strings.CutPrefix(line, trackedMarker+" "); found {
			return Result{Report: fmt.Sprintf("the repository tracks %s, which matches test_report %q. A tracked file cannot show that the tests ran. Stop tracking the file, then run the gate again.", file, project.TestReport), Steps: hostSteps(project, StatusUnknown, StatusFail)}, true
		}

		if line == noTestsMarker {
			return Result{Report: fmt.Sprintf("the gate command passed on %s, but no test case was found in the files that test_report %q matches. The tests step failed.", project.BuildHost, project.TestReport), Steps: hostSteps(project, StatusPass, StatusFail)}, true
		}
	}

	return Result{}, false
}

// withCauses adds the two causes of a failed host job to the report, for a host whose log beehave cannot read.
func withCauses(result Result, project model.Project) Result {
	if project.TestReport == "" {
		return result
	}

	result.Report += fmt.Sprintf(" The gate command failed, or the job found no test case in the files that test_report %q matches.", project.TestReport)

	return result
}

// stopJob stops the host job under a context that the end of ctx does not reach. A job that cannot be stopped keeps its slot for at most max_run,
// and the error names it.
func stopJob(ctx context.Context, host Remote, cluster string, id model.JobID) error {
	if err := host.Cancel(context.WithoutCancel(ctx), id); err != nil {
		return fmt.Errorf("stop job %s on %s: %w", id, cluster, err)
	}

	return nil
}

// job returns the host job to wait for and the head that it judges: the resumed job with its own head, or a new job for head.
func job(ctx context.Context, host Remote, request Request, head string) (model.JobID, string, error) {
	if request.Resume == "" {
		if request.CloneURL == "" {
			return "", "", fmt.Errorf("%w: project %s has no repository URL to clone on %s", ErrNoCloneURL, request.Project.Name, request.Project.BuildHost)
		}

		id, err := submit(ctx, host, request, head)

		return id, head, err
	}

	if request.ResumeHead == "" {
		return "", "", fmt.Errorf("%w: job %s needs the head that it was submitted with", ErrNoHead, request.Resume)
	}

	return request.Resume, request.ResumeHead, nil
}

func submit(ctx context.Context, host Remote, request Request, head string) (model.JobID, error) {
	project := request.Project

	clone, err := cloneOn(ctx, host, project, head)
	if err != nil {
		return "", err
	}

	id, err := host.Submit(ctx, model.JobSpec{Name: "gate-" + project.Name, Command: hostCommand(project, request.pins, request.CloneURL, head, clone)})
	if err != nil {
		return "", fmt.Errorf("start the gate on %s: %w", project.BuildHost, err)
	}

	if request.Submitted != nil {
		request.Submitted(id)
	}

	return id, nil
}

// cloneOn reads the work root of the host and returns the clone folder of the project for head below it.
func cloneOn(ctx context.Context, host Remote, project model.Project, head string) (string, error) {
	root, err := host.WorkRoot(ctx)
	if err != nil {
		return "", fmt.Errorf("read the work root of %s: %w", project.BuildHost, err)
	}

	return clonePath(root, project.Name, head)
}

// clonePath returns the folder that a host gate clones into: a name of the project and the head under the work root of the host.
// The work root must be one absolute path in its clean form, and the name and the head must be plain words, so the folder is always
// below the work root and the command can delete it by that literal path. Anything else is refused.
func clonePath(root, name, head string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return "", fmt.Errorf("%w: the work root %q is not one absolute path below the root folder", ErrCloneOutsideRoot, root)
	}

	if !plainWord(name) || !plainWord(head) {
		return "", fmt.Errorf("%w: the project %q or the head %q has a character that a folder name cannot hold", ErrCloneOutsideRoot, name, head)
	}

	return root + "/" + clonePrefix + name + "-" + head, nil
}

// plainWord is true for a word of letters, digits, dot, underscore and hyphen that is not a dot word.
func plainWord(word string) bool {
	return word != "" && !strings.HasPrefix(word, ".") && strings.Trim(word, plainRune) == ""
}

// hostCommand is the command line that makes the host gate the commit that beehave sends: a clone folder at the literal path clone,
// a clone of the repository at cloneURL, a detached checkout of head, a check that the checkout is head, and then the gate command.
// A trap on exit, hang-up, interrupt and terminate writes the marker line and then deletes the folder by its literal path, so the result
// is written first and the folder goes whether the gate passes, fails or is stopped. The exit status of the gate is kept.
// When the host has a run limit (`host.max_run`), the gate command runs under `timeout` with that limit, so a hung step ends the job
// with exit status 124 and the trap still writes the marker and deletes the clone.
// A clone or checkout that fails, as it does for a commit that is not pushed, ends the command with a failure before the gate runs.
func hostCommand(project model.Project, pins []Pin, cloneURL, head, clone string) string {
	path := model.ShellQuote(clone)
	words := []string{
		"trap " + model.ShellQuote("echo "+cloneMarker+"; rm -rf "+path) + " EXIT",
		"trap 'exit 143' HUP INT TERM",
		"rm -rf " + path,
		"mkdir " + path,
		"cd " + path,
		"git clone -- " + model.ShellQuote(cloneURL) + " tree",
		"cd tree",
		"git checkout --detach " + model.ShellQuote(head),
		`[ "$(git rev-parse HEAD)" = ` + model.ShellQuote(head) + ` ]`,
	}

	before, after := reportChecks(project.TestReport)

	words = append(words, before...)
	words = append(words, gateWords(project, pins)...)

	return strings.Join(append(words, after...), " && ")
}

// gateWords are the words that run the gate command. A project with a gate string has one word. A gate list has one word for each step:
// the word runs the step, writes the marker line "beehave-gate-step <name> rc=<code>" and records a failure, and it never stops the later steps.
// The last word fails when any step failed, so the job fails with the list.
func gateWords(project model.Project, pins []Pin) []string {
	if len(project.GateSteps) == 0 {
		return []string{limited(project, "sh -c "+model.ShellQuote(probed(pins, project.Gate)))}
	}

	var words []string

	for _, spec := range project.GateSteps {
		words = append(words, "{ "+limited(project, "sh -c "+model.ShellQuote(probed(pins, spec.Command)))+`; rc=$?; echo; echo "`+stepMarker+" "+spec.Name+` rc=$rc"; [ "$rc" -eq 0 ] || bad=1; }`)
	}

	return append(words, `[ -z "$bad" ]`)
}

// stepMarkerResults reads the marker lines of a gate list from the log of a job. A step with no marker is unknown, and a code other than 0 is a failure.
// A step that the log marks twice is unknown: a later line, which any step can print, then cannot turn the real code into another one.
// The marker is a line in the log that the step commands write to as well, so it is a reading and not proof.
func stepMarkerResults(log string, project model.Project) []Step {
	codes := markerCodes(log)
	steps := hostSteps(project, StatusUnknown, StatusUnknown)

	for i := range steps {
		if got := codes[steps[i].Name]; len(got) == 1 {
			steps[i].Status = exitStatus(atoiOr(got[0], 1))
		}
	}

	return steps
}

// markerCodes lists the codes that the marker lines of the log give for each step name, in log order.
func markerCodes(log string) map[string][]string {
	codes := map[string][]string{}

	for _, line := range strings.Split(log, "\n") {
		if rest, found := strings.CutPrefix(line, stepMarker+" "); found {
			if name, code, cut := strings.Cut(rest, " rc="); cut {
				codes[name] = append(codes[name], strings.TrimSpace(code))
			}
		}
	}

	return codes
}

// atoiOr reads a whole number, and gives fallback for text that is not one. A code that cannot be read is a failure.
func atoiOr(text string, fallback int) int {
	n, err := strconv.Atoi(text)
	if err != nil {
		return fallback
	}

	return n
}

// reportChecks returns the words of the report checks: the word before the gate refuses a report file that the repository tracks, as such a file
// would satisfy the count with no test run, and the word after the gate fails when the files that the glob matches hold no test case.
// Each word writes a marker line first, so the log tells the failed check from a failed gate command. A project with no glob has no check.
func reportChecks(glob string) (before, after []string) {
	if glob == "" {
		return nil, nil
	}

	tracked := "git ls-files -- " + model.ShellQuote(glob) + " | head -n 1"

	before = []string{`{ [ -z "$(` + tracked + `)" ] || { echo "` + trackedMarker + ` $(` + tracked + `)"; false; }; }`}
	after = []string{`{ [ "$(cat ` + globWord(glob) + ` 2>/dev/null | grep -c '<testcase')" -gt 0 ] || { echo ` + noTestsMarker + `; false; }; }`}

	return before, after
}

// globWord returns the glob as one shell word that still expands: each literal run is quoted and each wildcard (`*`, `?`, `[`, `]`) stays bare.
func globWord(glob string) string {
	return literalRun.ReplaceAllStringFunc(glob, model.ShellQuote)
}

// literalRun matches a run of glob text with no wildcard.
var literalRun = regexp.MustCompile(`[^*?\[\]]+`)

// limited puts command under `timeout` with the run limit of the host, when the project has a host block.
func limited(project model.Project, command string) string {
	if project.Host == nil || project.Host.MaxRun < time.Second {
		return command
	}

	return "timeout " + strconv.FormatInt(int64(project.Host.MaxRun/time.Second), 10) + " " + command
}

// verdict maps the final state of a host job to a result. Only a job that succeeded passes, and only one that failed is a failed gate.
// A job that died, went stale or ran past its limit gave no result: every step is unknown. Any other state is an error.
// The host command ends with the test count check, so a job that succeeded found a test case: both steps pass. A job that failed gives no
// result line for the tests step, so that step is unknown.
func verdict(ctx context.Context, project model.Project, head string, state model.JobState) (Result, error) {
	cluster := project.BuildHost

	// A stopped run ends the wait in one of these states too. That is the end of the run, not a gate that gave no result.
	if err := ctx.Err(); err != nil && slices.Contains([]model.JobState{model.JobDied, model.JobStale, model.JobTimedOut}, state) {
		return Result{}, fmt.Errorf("wait for the gate on %s: %w", cluster, err)
	}

	switch state {
	case model.JobSucceeded:
		return Result{Passed: true, Steps: hostSteps(project, StatusPass, StatusPass)}, nil
	case model.JobFailed:
		return Result{Report: "the gate job failed on " + cluster + ". Read its log on the host.", Steps: failedSteps(project)}, nil
	case model.JobDied, model.JobStale, model.JobTimedOut:
		return aborted(project, head, abortNote(cluster, state)), nil
	case model.JobPending, model.JobRunning, model.JobCanceled, model.JobUnknown, model.JobLost:
	}

	return Result{}, fmt.Errorf("%w: its state on %s is %s. Read the job in the scheduler of the host", ErrNoVerdict, cluster, state)
}

// failedSteps is the steps of a failed job before its log is read. A gate string failed. A gate list has no step that is known to have failed,
// because the job fails for a clone that failed too, so each step is unknown until its marker line says otherwise.
func failedSteps(project model.Project) []Step {
	if len(project.GateSteps) > 0 {
		return hostSteps(project, StatusUnknown, StatusUnknown)
	}

	return hostSteps(project, StatusFail, StatusUnknown)
}

// abortNote is the report of a host job that gave no result, with the action for a person.
func abortNote(cluster string, state model.JobState) string {
	if state == model.JobStale {
		return "the job on " + cluster + " was stopped because its log did not grow for `host.stale_minutes`. Read the log on the host, raise `host.stale_minutes` if the gate is slow, then run the gate again.\n"
	}

	if state == model.JobTimedOut {
		return "the gate on " + cluster + " was stopped by the run limit `host.max_run` and did not finish: it is not run. Raise `host.max_run` if the gate is slow, then run the gate again.\n"
	}

	return "the job died on " + cluster + ": the scheduler holds no record of it. Run the gate again.\n"
}

// hostSteps is the steps of a gate that all have one result: each spec of the list with the status command, and the tests when the project sets test_report.
func hostSteps(project model.Project, command, tests Status) []Step {
	var steps []Step

	for _, spec := range Specs(project) {
		steps = append(steps, Step{Name: spec.Name, Status: command})
	}

	if project.TestReport != "" {
		steps = append(steps, Step{Name: StepTests, Status: tests})
	}

	return steps
}

// tail returns the last reportLimit bytes of the output, without a cut character at the start.
func tail(output string) string {
	if len(output) <= reportLimit {
		return output
	}

	return strings.ToValidUTF8(output[len(output)-reportLimit:], "")
}
