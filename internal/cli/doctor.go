package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// ErrDoctorIncomplete is the cause when the Doctor has no value for a field. Every field is required.
var ErrDoctorIncomplete = errors.New("the doctor is missing a reader")

// Doctor holds the checks that need a program outside the process. Every field is required: a check that is left out would read as a pass.
type Doctor struct {
	// TokenReadable says whether the Keychain entry holds a token. It never returns the token.
	TokenReadable func(ctx context.Context, entry string) bool
	// BdVersion returns why the installed bd is too old to use for claims.
	BdVersion func(ctx context.Context) error
	// ReadUsage reads the usage of a runner. Its reading says whether the login of the runner has plan limits.
	ReadUsage func(ctx context.Context, runner model.Runner) model.Reading
}

// complete returns an error that names each field that has no value.
func (d Doctor) complete() error {
	var missing []string

	if d.TokenReadable == nil {
		missing = append(missing, "TokenReadable")
	}

	if d.BdVersion == nil {
		missing = append(missing, "BdVersion")
	}

	if d.ReadUsage == nil {
		missing = append(missing, "ReadUsage")
	}

	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf("%w: %s", ErrDoctorIncomplete, strings.Join(missing, ", "))
}

// fix is the text that tells the user how to make a failed check pass. It is the error of the check.
type fix string

func (f fix) Error() string { return string(f) }

// check is one line of the report: a name and a run that returns nil when the check passes, and the fix when it fails.
type check struct {
	name string
	run  func() error
}

func newDoctorCommand(ctx context.Context, env map[string]string, doctor Doctor) *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "doctor",
		Short: "Check that this machine can run beehave, and name the fix for each failure",
		Long:  "Check the config folder, the login folder and the token of each runner, and the programs that the projects need: git always, bd for a project with beads, gh for a project with GitHub. Exit 0 when every check passes, 1 when one fails.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(ctx, cmd.OutOrStdout(), env, doctor, flag)
		},
	}
	addConfigFlag(command, &flag)

	return command
}

func runDoctor(ctx context.Context, stdout io.Writer, env map[string]string, doctor Doctor, flag string) error {
	if err := doctor.complete(); err != nil {
		return err
	}

	dir, err := config.FolderPath(env, flag)
	if err != nil {
		return err
	}

	failed := false

	for _, item := range doctorChecks(ctx, env, doctor, dir) {
		failed = report(stdout, item) || failed
	}

	if failed {
		return &ExitError{Code: exitError}
	}

	return nil
}

// report prints the line of one check and says whether it failed.
func report(stdout io.Writer, item check) bool {
	err := item.run()

	// A failed write to stdout has no recovery.
	if err == nil {
		_, _ = fmt.Fprintf(stdout, "ok   %s\n", item.name)

		return false
	}

	_, _ = fmt.Fprintf(stdout, "FAIL %s\n     fix: %s\n", item.name, strings.TrimSpace(err.Error()))

	return true
}

// doctorChecks returns the checks in the order of the report: the config, the programs, then the runners.
// The runners come from the config folder, so a folder that does not load has none.
func doctorChecks(ctx context.Context, env map[string]string, doctor Doctor, dir string) []check {
	cfg, problems := config.LoadFolder(dir, env)

	checks := []check{{"config readable", func() error {
		if len(problems) == 0 {
			return nil
		}

		return fix(problems.Error() + " Fix the file, or create it.")
	}}}

	checks = append(checks, toolChecks(ctx, env, doctor, requiredTools(cfg))...)

	for _, runner := range cfg.Runners {
		checks = append(checks, runnerChecks(ctx, doctor, runner)...)
	}

	return checks
}

// runnerChecks returns the checks of the login of one runner.
func runnerChecks(ctx context.Context, doctor Doctor, runner model.Runner) []check {
	var checks []check

	if runner.ConfigDir != "" {
		checks = append(checks, configDirCheck(runner))

		if runner.Agent == model.AgentClaude {
			checks = append(checks, loginCheck(ctx, doctor.ReadUsage, runner))
		}
	}

	if runner.Keychain != "" {
		checks = append(checks, tokenCheck(ctx, doctor.TokenReadable, runner))
	}

	return checks
}

func configDirCheck(runner model.Runner) check {
	return check{"config_dir of " + runner.Name + " exists", func() error {
		if info, err := os.Stat(runner.ConfigDir); err == nil && info.IsDir() {
			return nil
		}

		return fix("Log in to it: CLAUDE_CONFIG_DIR=" + runner.ConfigDir + " claude")
	}}
}

// loginCheck fails when the usage of the runner cannot be read. The reason of the reading is the fix.
func loginCheck(ctx context.Context, read func(context.Context, model.Runner) model.Reading, runner model.Runner) check {
	return check{"login of " + runner.Name + " has plan limits", func() error {
		reading := read(ctx, runner)
		if _, known := reading.Usage(); known {
			return nil
		}

		return fix(reading.Reason())
	}}
}

func tokenCheck(ctx context.Context, readable func(context.Context, string) bool, runner model.Runner) check {
	return check{"token of " + runner.Name + " readable", func() error {
		if readable(ctx, runner.Keychain) {
			return nil
		}

		return fix("Run `claude setup-token`, then: security add-generic-password -s " + runner.Keychain + " -a $USER -w")
	}}
}

// requiredTools returns the programs that the projects of the config need, in the order of the report: git always, then bd and gh when a project names them.
func requiredTools(cfg config.Config) []string {
	needed := map[string]bool{model.ToolGit: true}

	for _, project := range cfg.Projects {
		for _, tool := range project.Tools() {
			needed[tool] = true
		}
	}

	var tools []string

	for _, tool := range []string{model.ToolGit, model.ToolBd, model.ToolGh} {
		if needed[tool] {
			tools = append(tools, tool)
		}
	}

	return tools
}

// toolChecks returns the checks of the programs on the path, and of the bd version when bd is needed and there.
func toolChecks(ctx context.Context, env map[string]string, doctor Doctor, tools []string) []check {
	var checks []check

	found := map[string]bool{}

	for _, tool := range tools {
		_, err := proc.LookPath(tool, env["PATH"])
		found[tool] = err == nil
		checks = append(checks, pathCheck(tool, found[tool]))
	}

	if found[model.ToolBd] {
		checks = append(checks, check{"bd version", func() error { return doctor.BdVersion(ctx) }})
	}

	return checks
}

func pathCheck(tool string, found bool) check {
	return check{tool + " on the path", func() error {
		if found {
			return nil
		}

		return fix("Install " + tool + ".")
	}}
}
