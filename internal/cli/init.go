package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/proc"
	"github.com/tobocop2/beehest/internal/proc/gitexec"
)

const (
	initGateDefault       = "make check"
	initTestReportDefault = "reports/*.xml"
	initNameFlag          = "name"
	initBranchFlag        = "base-branch"
	initGateFlag          = "gate"
	initTestReportFlag    = "test-report"
	branchPrefix          = "refs/heads/"
	gitNotFound           = 127 // the exit code that the runner reports for a binary that is not on PATH
	dirMode               = 0o700
	fileMode              = 0o600
)

var (
	errNotARepository = errors.New("run beehest init in the top folder of a git repository")
	errGitNotFound    = errors.New("git was not found. Install git and put it on PATH")
)

// projectNamePattern is a project name that is also a file stem: letters, digits, "-", "_" and ".", with no leading dot.
var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

const initLong = "Write the files that a project needs, in the current folder, which must be the top folder of a git repository. " +
	"The project file goes into the config folder as projects/<name>.star. The workflow file " + defaultWorkflowFile + " and the folder prompts go into the repository. " +
	"The command refuses when any of these files exists, and then it writes nothing."

// initOptions are the flags of init.
type initOptions struct {
	folder     string
	name       string
	branch     string
	gate       string
	testReport string
}

// initFile is a file that init creates.
type initFile struct {
	path string
	data []byte
}

func newInitCommand(ctx context.Context, env map[string]string) *cobra.Command {
	var opts initOptions

	command := &cobra.Command{
		Use:   "init",
		Short: "Write a project file, a workflow and its prompts for the current repository",
		Long:  initLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(ctx, cmd.OutOrStdout(), env, opts)
		},
	}
	flags := command.Flags()
	flags.StringVar(&opts.folder, configFlag, "", "the config folder (default: $"+config.ConfigDirVariable+", then ~/.config/beehest)")
	flags.StringVar(&opts.name, initNameFlag, "", "the project name (default: the name of the repository folder)")
	flags.StringVar(&opts.branch, initBranchFlag, "", "the branch that rounds start from (default: the branch that HEAD names)")
	flags.StringVar(&opts.gate, initGateFlag, initGateDefault, "the command that judges a change; set it to the test command of the repository")
	flags.StringVar(&opts.testReport, initTestReportFlag, initTestReportDefault, "the glob of the JUnit XML files that the gate writes")

	return command
}

func runInit(ctx context.Context, out io.Writer, env map[string]string, opts initOptions) error {
	repo, _ := os.Getwd() //nolint:errcheck // a folder that cannot be read leaves repo empty, and git then refuses it as no top folder of a repository

	files, err := planInit(ctx, env, opts, repo)
	if err != nil {
		return err
	}

	if err := refuseExisting(files); err != nil {
		return err
	}

	if err := createFiles(out, files); err != nil {
		return err
	}

	_, err = fmt.Fprintf(out, "The gate is %q. Set it with --%s, or edit the project file, to the command that tests the repository.\n", opts.gate, initGateFlag)

	return err
}

// planInit returns the files that init creates, after it checks every input.
func planInit(ctx context.Context, env map[string]string, opts initOptions, repo string) ([]initFile, error) {
	name, err := checkInputs(ctx, opts, repo)
	if err != nil {
		return nil, err
	}

	branch, err := baseBranch(ctx, repo, opts.branch)
	if err != nil {
		return nil, err
	}

	folder, err := config.FolderPath(env, opts.folder)
	if err != nil {
		return nil, err
	}

	return initTargets(filepath.Join(folder, "projects", name+".star"), projectFile(repo, branch, opts), repo), nil
}

// checkInputs refuses a folder that is not the top folder of a repository, an unusable name and an empty gate or test report. It returns the project name.
func checkInputs(ctx context.Context, opts initOptions, repo string) (string, error) {
	// Git prints "true" and the path below the top of the work tree, so only the top prints "true" alone; a bare repository or a .git folder prints "false".
	answer, err := askGit(ctx, repo, errNotARepository, "rev-parse", "--is-inside-work-tree", "--show-prefix")
	if err != nil {
		return "", err
	}

	if answer != "true" {
		return "", errNotARepository
	}

	name, err := projectName(repo, opts.name)
	if err != nil {
		return "", err
	}

	return name, errors.Join(requireText(initGateFlag, opts.gate), requireText(initTestReportFlag, opts.testReport))
}

// requireText refuses an empty or blank flag value, with the words of check config for the same key.
func requireText(flag, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("`%s` must be a non-empty string. Set it with --%s", strings.ReplaceAll(flag, "-", "_"), flag)
	}

	return nil
}

// initTargets returns the project file and each file of the template, with the paths that they get.
func initTargets(projectPath, project, repo string) []initFile {
	files := []initFile{{path: projectPath, data: []byte(project)}}

	for _, file := range readTemplate() {
		files = append(files, initFile{path: filepath.Join(repo, filepath.FromSlash(file.path)), data: file.data})
	}

	return files
}

// projectName returns the named project, else the name of the repository folder. The name is a file stem, so it must be a plain name.
func projectName(repo, named string) (string, error) {
	name := named
	if name == "" {
		name = filepath.Base(repo)
	}

	if !projectNamePattern.MatchString(name) {
		return "", fmt.Errorf("%q is not a usable project name: use letters, digits, \"-\", \"_\" and \".\", and no leading dot. Pass a name with --%s", name, initNameFlag)
	}

	return name, nil
}

// projectFile returns the text of a project file. starlark.String quotes each value, so a quote or a newline in a flag cannot break the file.
func projectFile(repo, branch string, opts initOptions) string {
	return fmt.Sprintf("project(\n    source = %s,\n    base_branch = %s,\n    sandbox = \"on\",\n    gate = %s,\n    test_report = %s,\n)\n",
		starlark.String(repo).String(), starlark.String(branch).String(), starlark.String(opts.gate).String(), starlark.String(opts.testReport).String())
}

// baseBranch returns the named branch, else the branch that HEAD of the repository names. Git reads the ref, so any ref storage works.
func baseBranch(ctx context.Context, repo, named string) (string, error) {
	if named != "" {
		return named, nil
	}

	refused := fmt.Errorf("HEAD of the repository names no branch. Pass the branch that rounds start from with --%s", initBranchFlag)

	head, err := askGit(ctx, repo, refused, "symbolic-ref", "HEAD")
	if err != nil {
		return "", err
	}

	branch, isBranch := strings.CutPrefix(head, branchPrefix)
	if !isBranch || branch == "" {
		return "", refused
	}

	return branch, nil
}

// askGit runs git in repo and returns its trimmed output. A git that is not on PATH gives errGitNotFound; any other failure gives refused.
func askGit(ctx context.Context, repo string, refused error, args ...string) (string, error) {
	result := gitexec.New(repo, proc.DefaultTimeout).Run(ctx, args)

	switch {
	case result.ExitCode == gitNotFound:
		return "", errGitNotFound
	case result.ExitCode != 0 || result.SetupErr != nil:
		return "", refused
	}

	return strings.TrimSpace(result.Stdout), nil
}

// refuseExisting fails when any file exists, and names each one. A symbolic link counts as a file, also a dangling one, because Lstat does not follow it.
func refuseExisting(files []initFile) error {
	var existing []string

	for _, file := range files {
		if _, err := os.Lstat(file.path); err == nil {
			existing = append(existing, file.path)
		}
	}

	if len(existing) > 0 {
		return fmt.Errorf("these files exist: %s. Init never replaces a file, so no file was written. Move them away, or edit them by hand", strings.Join(existing, ", "))
	}

	return nil
}

// createFiles writes each file, never over an existing one, and says which files it wrote. A failure names the files already written; nothing is rolled back.
func createFiles(out io.Writer, files []initFile) error {
	var written []string

	for _, file := range files {
		err := createFile(file)
		if err != nil && len(written) > 0 {
			return fmt.Errorf("%w. These files are already written: %s", err, strings.Join(written, ", "))
		}

		if err != nil {
			return err
		}

		written = append(written, file.path)
	}

	_, err := fmt.Fprintf(out, "Wrote:\n  %s\n", strings.Join(written, "\n  "))

	return err
}

// createFile makes the folders of a file, then creates the file with O_EXCL so it never replaces one. Folders and files are private to the user.
func createFile(file initFile) error {
	if err := os.MkdirAll(filepath.Dir(file.path), dirMode); err != nil {
		return fmt.Errorf("make the folder of %s: %w", file.path, err)
	}

	handle, err := os.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return fmt.Errorf("create %s: %w", file.path, err)
	}

	_, err = handle.Write(file.data)

	return errors.Join(err, handle.Close())
}
