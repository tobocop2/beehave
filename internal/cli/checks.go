package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/tobocop2/obee/internal/check/gate"
	"github.com/tobocop2/obee/internal/check/papers"
	"github.com/tobocop2/obee/internal/check/shell"
	"github.com/tobocop2/obee/internal/check/transcript"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/scope"
)

// CheckDeps reads what the check commands need outside the process.
type CheckDeps struct {
	// Skills returns the Skill calls of the transcript at a path. A nil value leaves out the check skills command.
	Skills func(path, agentID string) ([]model.Call, error)
	// Bash returns the Bash calls of the transcript at a path. A nil value leaves out the check transcript command.
	Bash func(path string) ([]model.Call, error)
	// Git runs git in the repository under check. A nil value leaves out the check papers command.
	Git func(ctx context.Context, args []string) model.CommandResult
	// Closes and OwnerAdds read the ledger; PRBody reads the forge. A nil value in any of the three leaves out the check scope command.
	Closes    func(key string) ([]string, error)
	OwnerAdds func(key string) ([]string, error)
	PRBody    func(ctx context.Context, repo string, pr int) (string, error)
}

// gitFunc lets a function value stand for the git runner of the papers check.
type gitFunc func(ctx context.Context, args []string) model.CommandResult

func (g gitFunc) Run(ctx context.Context, args []string) model.CommandResult { return g(ctx, args) }

// ledgerFuncs and forgeFunc let function values stand for the reads of the scope check.
type ledgerFuncs struct {
	closes    func(key string) ([]string, error)
	ownerAdds func(key string) ([]string, error)
}

func (l ledgerFuncs) Closes(key string) ([]string, error)    { return l.closes(key) }
func (l ledgerFuncs) OwnerAdds(key string) ([]string, error) { return l.ownerAdds(key) }

type forgeFunc func(ctx context.Context, pr int) (string, error)

func (f forgeFunc) PRBody(ctx context.Context, pr int) (string, error) { return f(ctx, pr) }

// forgeOf reads pull request bodies from one repo.
func forgeOf(read func(ctx context.Context, repo string, pr int) (string, error), repo string) forgeFunc {
	return func(ctx context.Context, pr int) (string, error) { return read(ctx, repo, pr) }
}

// readArg reads the file that the argument at an index names.
func readArg(in input, index int) (string, error) {
	text, err := fileText(in.args[index])
	if err != nil {
		return "", fmt.Errorf("read %s: %w", in.args[index], err)
	}

	return text, nil
}

func checkSpecs(deps CheckDeps) []spec {
	specs := []spec{
		{use: "job", short: "Compare a gate job script with its step list", args: cobra.NoArgs, bind: bindCheckJob},
		{use: "authors <file>", short: "Check that each commit of ours carries our address", args: cobra.ExactArgs(1), run: runCheckAuthors},
		{use: "report <file>", short: "Check a lane report", args: cobra.ExactArgs(1), bind: bindCheckReport},
		{use: "handoff <file>", short: "Check a lane handoff", args: cobra.ExactArgs(1), bind: bindCheckHandoff},
	}

	return append(specs, depSpecs(deps)...)
}

// jobOptions holds the flags of check job.
type jobOptions struct {
	script string
	list   string
}

func bindCheckJob(f *pflag.FlagSet) func(input) error {
	opts := &jobOptions{}
	f.StringVar(&opts.script, "script", "", "the job script")
	f.StringVar(&opts.list, "list", "", "the gate step list")

	return func(in input) error { return runCheckJob(in, opts) }
}

func bindCheckReport(f *pflag.FlagSet) func(input) error {
	var head string

	f.StringVar(&head, "head", "", "the head the report names")

	return func(in input) error { return runCheckReport(in, head) }
}

// handoffOptions holds the flags of check handoff.
type handoffOptions struct {
	transcript string
	capText    string
}

func bindCheckHandoff(f *pflag.FlagSet) func(input) error {
	opts := &handoffOptions{}
	f.StringVar(&opts.transcript, "transcript", "", "the transcript of the lane, when the handoff has no token line")
	f.StringVar(&opts.capText, "cap", "", "the context cap in tokens")

	return func(in input) error { return runCheckHandoff(in, opts) }
}

// depSpecs builds the check commands that need a dependency. A command whose dependency is nil is left out.
func depSpecs(deps CheckDeps) []spec {
	var specs []spec

	for _, build := range []func(CheckDeps) []spec{skillsSpecs, papersSpecs, scopeSpecs, transcriptSpecs} {
		specs = append(specs, build(deps)...)
	}

	return specs
}

func skillsSpecs(deps CheckDeps) []spec {
	if deps.Skills == nil {
		return nil
	}

	return []spec{{
		use: "skills <transcript>", short: "Check that a lane called each required skill", args: cobra.ExactArgs(1),
		bind: func(f *pflag.FlagSet) func(input) error {
			var required []string

			f.StringSliceVar(&required, "skill", nil, "a required skill (repeat)")

			return func(in input) error { return runCheckSkills(deps, in, required) }
		},
	}}
}

func papersSpecs(deps CheckDeps) []spec {
	if deps.Git == nil {
		return nil
	}

	return []spec{{
		use: "papers <base>..<head>", short: "Check that a revision range changes no source path", args: cobra.ExactArgs(1),
		bind: func(f *pflag.FlagSet) func(input) error {
			var source string

			var project projectFlags

			f.StringVar(&source, "source", "", "the source path patterns, space separated")
			project.bind(f)

			return func(in input) error { return runCheckPapers(deps, in, source, &project) }
		},
	}}
}

func scopeSpecs(deps CheckDeps) []spec {
	if deps.Closes == nil || deps.OwnerAdds == nil || deps.PRBody == nil {
		return nil
	}

	return []spec{{
		use: "scope <pr>", short: "Check that a pull request closes no issue added after its first review", args: cobra.ExactArgs(1),
		bind: func(f *pflag.FlagSet) func(input) error {
			var key string

			var project projectFlags

			f.StringVar(&key, "key", "", "the ledger key of the item, the pull request number when empty")
			project.bind(f)

			return func(in input) error { return runCheckScope(deps, in, key, &project) }
		},
	}}
}

func transcriptSpecs(deps CheckDeps) []spec {
	if deps.Bash == nil {
		return nil
	}

	return []spec{{
		use: "transcript <path>", short: "Check that a lane ran no forbidden command", args: cobra.ExactArgs(1),
		run: func(in input) error { return runCheckTranscript(deps, in) },
	}}
}

// revisionRange returns the range to check. With a project, a head with no ".." is checked against the trunk of the project.
func revisionRange(in input, project *projectFlags) (string, error) {
	if !project.given() || strings.Contains(in.args[0], "..") {
		return in.args[0], nil
	}

	chosen, err := project.load(in.env)
	if err != nil {
		return "", err
	}

	return "origin/" + chosen.BaseBranch + ".." + in.args[0], nil
}

func runCheckPapers(deps CheckDeps, in input, source string, project *projectFlags) error {
	revisions, err := revisionRange(in, project)
	if err != nil {
		return err
	}

	lines, err := papers.SourcePaths(in.ctx, gitFunc(deps.Git), revisions, source)
	if err != nil {
		return err
	}

	say(in.stdout, lines...)

	if strings.HasPrefix(lines[0], "REFUSED") {
		return &ExitError{Code: exitProblems}
	}

	return nil
}

// repoOf returns the repo of the project the flags choose, or the repo of the environment when the flags name none.
func repoOf(in input, project *projectFlags) (string, error) {
	if !project.given() {
		return config.NewSettings(in.env).Repo(), nil
	}

	chosen, err := project.load(in.env)
	if err != nil {
		return "", err
	}

	return chosen.Repo, nil
}

func runCheckScope(deps CheckDeps, in input, key string, project *projectFlags) error {
	repo, err := repoOf(in, project)
	if err != nil {
		return err
	}

	result, err := scope.Check(in.ctx, ledgerFuncs{closes: deps.Closes, ownerAdds: deps.OwnerAdds}, forgeOf(deps.PRBody, repo),
		scope.Request{PR: in.args[0], Key: key, Repo: repo})
	if err != nil {
		return err
	}

	say(in.stdout, result.Lines...)

	if result.Code != 0 {
		return &ExitError{Code: result.Code}
	}

	return nil
}

func runCheckTranscript(deps CheckDeps, in input) error {
	hits, err := transcript.Bash(deps.Bash, in.args[0], shell.Scan)
	if err != nil {
		return err
	}

	return verdict(in.stdout, len(hits) == 0, hits...)
}

// sourceOf reads a file for the job check and reports whether it could.
func sourceOf(path string) (string, bool) {
	text, err := fileText(path)

	return text, err == nil
}

// read refuses a missing flag, then reads the job script and the gate list.
func (o *jobOptions) read() (script, list string, err error) {
	switch {
	case o.script == "":
		return "", "", errors.New("give the --script flag")
	case o.list == "":
		return "", "", errors.New("give the --list flag")
	}

	if script, err = fileText(o.script); err != nil {
		return "", "", fmt.Errorf("read %s: %w", o.script, err)
	}

	if list, err = fileText(o.list); err != nil {
		return "", "", fmt.Errorf("read %s: %w", o.list, err)
	}

	return script, list, nil
}

func runCheckJob(in input, opts *jobOptions) error {
	script, list, err := opts.read()
	if err != nil {
		return err
	}

	result, err := gate.Job(script, list, opts.list, filepath.Dir(opts.script), sourceOf)
	if err != nil {
		return err
	}

	if result.OK() {
		return verdict(in.stdout, true, append(result.Lines, result.Summary())...)
	}

	return verdict(in.stdout, false, result.Lines...)
}

func runCheckAuthors(in input) error {
	text, err := readArg(in, 0)
	if err != nil {
		return err
	}

	settings := config.NewSettings(in.env)
	result := papers.Authors(text, settings.MeEmail(), settings.MeLogin())
	say(in.stdout, result.Out...)
	say(in.stderr, result.Err...)

	if result.Code != 0 {
		return &ExitError{Code: result.Code}
	}

	return nil
}

func runCheckReport(in input, head string) error {
	text, err := readArg(in, 0)
	if err != nil {
		return err
	}

	result := gate.Report(text, head)

	if result.OK() {
		return verdict(in.stdout, true, result.Summary(head))
	}

	return verdict(in.stdout, false, result.Failures...)
}

func runCheckHandoff(in input, opts *handoffOptions) error {
	text, err := readArg(in, 0)
	if err != nil {
		return err
	}

	handoff := papers.HandoffOptions{Transcript: opts.transcript}
	if opts.capText != "" {
		handoff.Cap = &opts.capText
	}

	result, err := papers.Handoff(text, filepath.Base(in.args[0]), handoff)
	if err != nil {
		return err
	}

	return verdict(in.stdout, !result.Refused, result.Lines...)
}

func runCheckSkills(deps CheckDeps, in input, required []string) error {
	lines, ok, err := transcript.Skills(deps.Skills, in.args[0], required, "")
	if err != nil {
		return err
	}

	return verdict(in.stdout, ok, lines...)
}
