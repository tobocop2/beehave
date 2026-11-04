package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rogpeppe/go-internal/testscript"
)

// maintFakes are the programs that the maintenance scripts call: the file in testdata and the name in the folder fake of the work folder.
// go, npm and gh stand for the toolchain and the forge of the three repositories, which do not exist. beehave runs the real command, as the scheduler would.
var maintFakes = map[string]string{
	"maint-claude.sh":    "claude",
	"maint-go.sh":        "go",
	"maint-gotestsum.sh": "gotestsum",
	"maint-npm.sh":       "npm",
	"maint-gh.sh":        "gh",
	"maint-beehave.sh":   "beehave",
}

// maintRepos are the repositories of the example and the manifest file that each one starts with.
// The gate of each one is the command of its row in repos.tsv, which writes the JUnit file that the project gate counts.
var maintRepos = map[string]struct{ file, text string }{
	"api":   {"go.mod", "module acme/api\n\nrequire example.com/lib v1.2.0\n"},
	"tools": {"go.mod", "module acme/tools\n\nrequire example.com/lib v1.2.0\n"},
	"web":   {"package.json", "{\"name\": \"web\", \"dependencies\": {\"left-pad\": \"^1.0.0\"}}\n"},
}

// maintGit runs git in dir with a fixed identity.
func maintGit(ts *testscript.TestScript, dir string, args ...string) {
	command := exec.CommandContext(context.Background(), "git", append([]string{"-c", "user.name=e2e", "-c", "user.email=e2e@example.com"}, args...)...) //nolint:gosec // a test runs git on its own temporary folder
	command.Dir = dir

	if out, err := command.CombinedOutput(); err != nil {
		ts.Fatalf("maintinit: git %s: %v: %s", args[0], err, out)
	}
}

// cmdMaintInit is "maintinit": it copies examples/maintenance to home/.upkeep in the work folder and commits it there.
// It makes a bare repository remotes/acme/<name> for each repository of the example. The global git config maps the GitHub address of the repository to it.
// The project files are the output of mkprojects.sh: repo and forge, as a user has them. beehave clones and pushes through the mapped address.
func cmdMaintInit(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: maintinit")
	}

	work := ts.Getenv("WORK")
	upkeep := filepath.Join(work, "home", ".upkeep")

	if err := os.CopyFS(upkeep, os.DirFS(filepath.Join("..", "..", "examples", "maintenance"))); err != nil {
		ts.Fatalf("maintinit: %v", err)
	}

	// The user of the example has a global git identity. The commits of the update step need it in the worktree, which has no config of the clone.
	gitconfig := filepath.Join(work, "home", ".gitconfig")
	maintGit(ts, upkeep, "config", "--file", gitconfig, "user.name", "e2e")
	maintGit(ts, upkeep, "config", "--file", gitconfig, "user.email", "e2e@example.com")

	maintGit(ts, upkeep, "init", "-q", "-b", "main")
	maintGit(ts, upkeep, "add", "scripts", "bump.star", "report.star", "repos.tsv", "prompts")
	maintGit(ts, upkeep, "commit", "-q", "-m", "upkeep")

	for name, repo := range maintRepos {
		seed := filepath.Join(work, "seed", name)
		if err := os.MkdirAll(seed, e2eFolderMode); err != nil {
			ts.Fatalf("maintinit: %v", err)
		}

		if err := os.WriteFile(filepath.Join(seed, repo.file), []byte(repo.text), e2eFileMode); err != nil {
			ts.Fatalf("maintinit: %v", err)
		}

		// A repository of the example ignores the folder of the test report: an untracked report makes the tree unclean, and the gate refuses it.
		if err := os.WriteFile(filepath.Join(seed, ".gitignore"), []byte("reports/\n"), e2eFileMode); err != nil {
			ts.Fatalf("maintinit: %v", err)
		}

		remote := filepath.Join(work, "remotes", "acme", name)
		if err := os.MkdirAll(filepath.Dir(remote), e2eFolderMode); err != nil {
			ts.Fatalf("maintinit: %v", err)
		}

		maintGit(ts, seed, "init", "-q", "-b", "main")
		maintGit(ts, seed, "add", repo.file, ".gitignore")
		maintGit(ts, seed, "commit", "-q", "-m", "seed")
		maintGit(ts, work, "clone", "-q", "--bare", seed, remote)

		maintGit(ts, work, "config", "--file", gitconfig, "url."+remote+".insteadOf", "https://github.com/acme/"+name+".git")
	}

	mkprojects := exec.CommandContext(context.Background(), "sh", filepath.Join(upkeep, "scripts", "mkprojects.sh"), filepath.Join(work, "config")) //nolint:gosec // a test runs the script of the example
	mkprojects.Env = []string{"PATH=" + ts.Getenv("PATH"), "HOME=" + filepath.Join(work, "home"), "UPKEEP_HOME=" + upkeep}

	if out, err := mkprojects.CombinedOutput(); err != nil {
		ts.Fatalf("maintinit: mkprojects: %v: %s", err, out)
	}
}

// cmdMaintFakes is "maintfakes": it writes the fake claude, go, npm and gh of the maintenance scripts to the folder fake in the work folder.
// Each program holds the work folder in place of @WORK@.
func cmdMaintFakes(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: maintfakes")
	}

	work := openRoot(ts, ts.Getenv("WORK"), "")
	defer work.Close()

	if err := work.MkdirAll("fake", e2eFolderMode); err != nil {
		ts.Fatalf("maintfakes: %v", err)
	}

	testdata, err := os.OpenRoot("testdata")
	if err != nil {
		ts.Fatalf("maintfakes: %v", err)
	}
	defer testdata.Close()

	for source, name := range maintFakes {
		text, err := testdata.ReadFile(source)
		if err != nil {
			ts.Fatalf("maintfakes: %v", err)
		}

		if err := work.WriteFile(filepath.Join("fake", name), []byte(strings.ReplaceAll(string(text), e2eWorkMarker, ts.Getenv("WORK"))), e2eToolMode); err != nil {
			ts.Fatalf("maintfakes: %v", err)
		}
	}
}
