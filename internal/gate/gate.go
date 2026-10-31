// Package gate runs the gate command of a project, on this machine or on its build host, and says whether the tree passed.
package gate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
)

const (
	// reportLimit is the most bytes of output that a Result keeps. The tail holds the findings.
	reportLimit = model.ReportLimit

	// signalBase is the exit code that a shell reports for a command that a signal ended: 128 plus the signal number.
	signalBase = 128

	// safeWordRune holds the characters that a shell word may hold without a quote.
	safeWordRune = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

	// plainRune holds the characters of a word in the folder name of a clone.
	plainRune = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-"

	// clonePrefix starts the name of every clone folder of a host gate.
	clonePrefix = "obee-gate-"

	// cloneMarker is the line that the job writes when the gate has ended, before it deletes the clone.
	cloneMarker = "obee-gate-done"
)

// ErrTreeChanged is the cause when the tree is not the clean commit that the gate must judge.
var ErrTreeChanged = errors.New("the tree is not the commit the gate judges")

// ErrNoVerdict is the cause when a host job ends in a state that is neither passed nor failed, or a signal kills a local gate command.
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
	Submitted  func(model.JobID)
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
// the gate must judge the commit that obee sends on. A gate on a build host clones the repository there and checks out that commit,
// so the host never judges a tree that obee did not send. Dir is read for the local gate and for the head only.
func (g Gate) Run(ctx context.Context, request Request) (Result, error) {
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
	if request.Project.BuildHost != config.LocalHost {
		return g.remote(ctx, request, head)
	}

	if reason := g.pinProblem(ctx, request); reason != "" {
		return voided(request, head, reason), nil
	}

	return g.local(ctx, request, head)
}

func (g Gate) local(ctx context.Context, request Request, head string) (Result, error) {
	before := takeSnapshot(request.Dir, request.Project.TestReport)
	output, code := g.shell(ctx, request.Dir, request.Project.Gate)
	if code > signalBase {
		return Result{}, fmt.Errorf("%w: the gate command was killed by a signal (exit code %d). A killed gate is neither green nor red. Run the gate again", ErrNoVerdict, code)
	}

	result := Result{Report: tail(output), Head: head, Steps: []Step{{Name: StepCommand, Status: exitStatus(code)}}}

	if request.Project.TestReport != "" {
		result = withTestsStep(result, request, code, output, before)
	}

	result.Passed = allPass(result.Steps)

	return result, nil
}

// voided is the result of a gate that did not run because a pinned tool is not at its pinned version. Every step is unknown: neither a pass nor a fail.
func voided(request Request, head, reason string) Result {
	steps := []Step{{Name: StepCommand, Status: StatusUnknown}}
	if request.Project.TestReport != "" {
		steps = append(steps, Step{Name: StepTests, Status: StatusUnknown})
	}

	return Result{Report: tail("the gate did not run: " + reason + ". Install the pinned version, then run the gate again.\n"), Head: head, Steps: steps}
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

	return wait(ctx, host, project.BuildHost, id, head)
}

// wait waits for the host job and maps its state to a result for head.
func wait(ctx context.Context, host Remote, cluster string, id model.JobID, head string) (Result, error) {
	state, err := host.Wait(ctx, id)
	if err != nil {
		// A wait that ends because the context ended leaves the job running on the host: stop it, so a stopped gate frees the host.
		if ctx.Err() != nil {
			err = errors.Join(err, stopJob(ctx, host, cluster, id))
		}

		return Result{}, fmt.Errorf("wait for the gate on %s: %w", cluster, err)
	}

	result, err := verdict(cluster, state)
	result.Head = head

	return result, err
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
	project := request.Project

	if request.CloneURL == "" {
		return "", "", fmt.Errorf("%w: project %s has no repository URL to clone on %s", ErrNoCloneURL, project.Name, project.BuildHost)
	}

	if request.Resume == "" {
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

	id, err := host.Submit(ctx, model.JobSpec{Name: "gate-" + project.Name, Command: hostCommand(project, request.CloneURL, head, clone)})
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

// hostCommand is the command line that makes the host gate the commit that obee sends: a clone folder at the literal path clone,
// a clone of the repository at cloneURL, a detached checkout of head, a check that the checkout is head, and then the gate command.
// A trap on exit, hang-up, interrupt and terminate writes the marker line and then deletes the folder by its literal path, so the result
// is written first and the folder goes whether the gate passes, fails or is stopped. The exit status of the gate is kept.
// A clone or checkout that fails, as it does for a commit that is not pushed, ends the command with a failure before the gate runs.
func hostCommand(project model.Project, cloneURL, head, clone string) string {
	path := quote(clone)
	words := []string{
		"trap " + quote("echo "+cloneMarker+"; rm -rf "+path) + " EXIT",
		"trap 'exit 143' HUP INT TERM",
		"rm -rf " + path,
		"mkdir " + path,
		"cd " + path,
		"git clone -- " + quote(cloneURL) + " tree",
		"cd tree",
		"git checkout --detach " + quote(head),
		`[ "$(git rev-parse HEAD)" = ` + quote(head) + ` ]`,
		"sh -c " + quote(project.Gate),
	}

	return strings.Join(words, " && ")
}

// quote returns the word as one word for a POSIX shell.
func quote(word string) string {
	if word != "" && strings.Trim(word, safeWordRune) == "" {
		return word
	}

	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// verdict maps the final state of a host job to a result. Only a job that succeeded passes, and only one that failed is a failed gate.
func verdict(cluster string, state model.JobState) (Result, error) {
	switch state {
	case model.JobSucceeded:
		return Result{Passed: true, Steps: []Step{{Name: StepCommand, Status: StatusPass}}}, nil
	case model.JobFailed:
		return Result{Report: "the gate job failed on " + cluster + ". Read its log on the host.", Steps: []Step{{Name: StepCommand, Status: StatusFail}}}, nil
	case model.JobPending, model.JobRunning, model.JobCanceled, model.JobUnknown:
	}

	return Result{}, fmt.Errorf("%w: its state on %s is %s. Read the job in the scheduler of the host", ErrNoVerdict, cluster, state)
}

// tail returns the last reportLimit bytes of the output, without a cut character at the start.
func tail(output string) string {
	if len(output) <= reportLimit {
		return output
	}

	return strings.ToValidUTF8(output[len(output)-reportLimit:], "")
}
