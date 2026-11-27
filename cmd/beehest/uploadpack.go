package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc"
	"github.com/tobocop2/beehest/internal/sandbox"
	"github.com/tobocop2/beehest/internal/sandbox/enforcers"
)

// uploadPackArg is the first argument that makes beehest run git upload-pack under a sandbox enforcer, for the copy of a head into the push repository.
const uploadPackArg = "--beehest-upload-pack"

// uploadPackWords is the number of words of a helper call: beehest, uploadPackArg, the enforcer, the folder that upload-pack may read, and the path
// that git appends.
const uploadPackWords = 5

// uploadPackFailed is the exit code of the helper when upload-pack could not start under the enforcer. Upload-pack then ran no code.
const uploadPackFailed = 125

// noSystemConfig makes git read no system config file, which lies outside the read set of the copy.
const noSystemConfig = "GIT_CONFIG_NOSYSTEM=1"

// chooseEnforcer returns the enforcer that the sandbox_enforcer value name asks for, or an error that wraps sandbox.ErrUnavailable and says why none runs.
func chooseEnforcer(name string) (sandbox.Enforcer, error) { //nolint:ireturn // the table holds each enforcer by the interface
	table := enforcers.Table()

	key, err := sandbox.Choose(table, enforcers.Order(), name)
	if err != nil {
		return nil, err
	}

	return table[key], nil
}

// gitProgram is the git that upload-pack runs as: the program with its links resolved, and each path that it reads to start and to run pack-objects.
type gitProgram struct {
	binary string
	reads  []string
}

// findGit returns the git that the search path paths finds, with the program, its exec path and the libraries that it loads as the paths it reads. Any
// of them that cannot be found is an error, so the copy never runs with a read set that is unknown.
func findGit(ctx context.Context, paths string) (gitProgram, error) {
	binary, err := proc.LookPath(model.ToolGit, paths)
	if err == nil {
		binary, err = filepath.EvalSymlinks(binary)
	}

	if err != nil {
		return gitProgram{}, fmt.Errorf("find git: %w", err)
	}

	execPath, err := gitExecPath(ctx, binary, paths)
	if err != nil {
		return gitProgram{}, fmt.Errorf("find the exec path of %s: %w", binary, err)
	}

	libraries, err := gitLibraries(ctx, binary)
	if err != nil {
		return gitProgram{}, fmt.Errorf("find the libraries of %s: %w", binary, err)
	}

	return gitProgram{binary: binary, reads: append([]string{binary, execPath}, libraries...)}, nil
}

// gitExecPath returns the folder of the git programs that binary runs, such as pack-objects, with its links resolved.
func gitExecPath(ctx context.Context, binary, paths string) (string, error) {
	command := exec.CommandContext(ctx, binary, "--exec-path")
	command.Env = []string{"PATH=" + paths, noSystemConfig}

	out, err := command.Output()
	if err != nil {
		return "", err
	}

	return filepath.EvalSymlinks(strings.TrimSpace(string(out)))
}

// copyTools are what the helper needs: the enforcer that a sandbox_enforcer value names, and the git on a search path with what it reads.
type copyTools struct {
	choose func(name string) (sandbox.Enforcer, error)
	git    func(ctx context.Context, paths string) (gitProgram, error)
}

// machineTools returns the enforcers of this machine and the git that findGit finds.
func machineTools() copyTools { return copyTools{choose: chooseEnforcer, git: findGit} }

// uploadPackProgram returns the --upload-pack value of the copy: beehest in the helper mode, with the enforcer and the folder that upload-pack may read,
// each word quoted for the shell that git starts it with.
func uploadPackProgram(self, enforcer, folder string) string {
	words := []string{self, uploadPackArg, enforcer, folder}
	for i, word := range words {
		words[i] = model.ShellQuote(word)
	}

	return strings.Join(words, " ")
}

// uploadPackPlan is the plan of upload-pack: read on the folder and on the paths that git reads, write on the null device only, which git opens at start,
// and no network. No system folder is read whole, so a path that a project denies to the agent stays out of reach.
func uploadPackPlan(git gitProgram, folder string) sandbox.Plan {
	return sandbox.Plan{Read: append(append([]string{}, git.reads...), folder), Write: []string{os.DevNull}}
}

// helperIO is the standard input and output of the helper, which upload-pack gets as they are.
type helperIO struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

// runUploadPack is called first in main. When args are a helper call, it runs git upload-pack on the path that git appended, under the enforcer that
// tools choose for the name in args and the plan of the folder in args, and returns the exit code and true. Git is the one that the PATH of env finds.
// When args are not a helper call, it returns 0 and false at once.
func runUploadPack(ctx context.Context, args []string, env map[string]string, tools copyTools, std helperIO) (int, bool) {
	if len(args) != uploadPackWords || args[1] != uploadPackArg {
		return 0, false
	}

	code, err := uploadPack(ctx, args[2], args[3], args[4], env, tools, std)
	if err != nil {
		fmt.Fprintf(std.stderr, "beehest upload-pack: %v. The copy of the head did not start.\n", err)

		return uploadPackFailed, true
	}

	return code, true
}

// uploadPack runs git upload-pack on path under the enforcer that tools choose for name, with the plan of folder, and returns its exit code.
// Any wanted object is served: the plan, not a ref, limits what upload-pack can read.
func uploadPack(ctx context.Context, name, folder, path string, env map[string]string, tools copyTools, std helperIO) (int, error) {
	enforcer, err := tools.choose(name)
	if err != nil {
		return 0, err
	}

	git, err := tools.git(ctx, env["PATH"])
	if err != nil {
		return 0, err
	}

	return sandbox.Execute(ctx, enforcer, uploadPackPlan(git, folder), sandbox.Command{
		Argv: []string{git.binary, "-c", "uploadpack.allowAnySHA1InWant=true", "upload-pack", path}, Dir: folder, Env: []string{"PATH=" + env["PATH"], noSystemConfig},
		Stdin: std.stdin, Stdout: std.stdout, Stderr: std.stderr,
	})
}
