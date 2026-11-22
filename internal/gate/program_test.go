package gate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	programCheckScript = `mkdir -p reports
printf '<testsuite tests="1"><testcase name="t"/></testsuite>\n' > reports/j.xml
[ -f code.sh ] && sh code.sh
for f in scripts/extra.d/*.sh scripts/sub/*.sh; do [ -f "$f" ] && . "$f"; done
if grep -q '^strict=1$' gate.cfg; then grep -q GOOD code.txt || { echo "code.txt is not GOOD"; exit 1; }; fi
`
	programWeakMakefile = "check:\n\t@mkdir -p reports; printf '<testsuite tests=\"1\"><testcase name=\"t\"/></testsuite>\\n' > reports/j.xml; exit 0\n"
)

// programCase is one head against the base: the files it writes, the shell it runs in the repository before it commits, a change of the base after the head exists, and the verdict.
type programCase struct {
	name   string
	files  map[string]string
	setup  string
	moved  string
	pass   bool
	reason string
}

func programCases() []programCase {
	bad := "BAD\n"
	setStrict0 := `printf 'strict=0\n' > gate.cfg`

	return []programCase{
		{name: "good_code_and_a_new_test_file_passes", files: map[string]string{"code.txt": "GOOD more\n", "tests_new.txt": "x\n"}, pass: true},
		{name: "bad_code_fails", files: map[string]string{"code.txt": bad}},
		{name: "makefile_exit_zero_with_bad_code_fails", files: map[string]string{"Makefile": programWeakMakefile, "code.txt": bad}, reason: "gate-program"},
		{name: "script_exit_zero_with_bad_code_fails", files: map[string]string{"scripts/check.sh": "mkdir -p reports; printf '<testsuite tests=\"1\"><testcase name=\"t\"/></testsuite>\\n' > reports/j.xml; exit 0\n", "code.txt": bad}},
		{name: "relaxed_config_with_bad_code_fails", files: map[string]string{"gate.cfg": "strict=0\n", "code.txt": bad}},
		{name: "file_added_to_a_listed_directory_fails", files: map[string]string{"scripts/extra.d/z.sh": "exit 0\n", "code.txt": bad}},
		{name: "listed_file_deleted_fails", files: map[string]string{"Makefile": programWeakMakefile, "code.txt": bad}, setup: "git rm -q scripts/check.sh"},
		{name: "weakened_makefile_with_good_code_is_refused_and_names_the_path", files: map[string]string{"Makefile": programWeakMakefile}, reason: "gate-program the head changed the gate program, paths Makefile"},
		{name: "gnumakefile_added_with_bad_code_fails", files: map[string]string{"GNUmakefile": programWeakMakefile, "code.txt": bad}},
		{name: "file_written_in_a_listed_directory_during_the_gate_fails", files: map[string]string{"code.sh": "mkdir -p scripts/extra.d; printf 'exit 0\\n' > scripts/extra.d/late.sh\n", "code.txt": bad}},
		{name: "ignored_file_written_in_a_listed_directory_during_the_gate_fails", files: map[string]string{".gitignore": "reports/\nscripts/extra.d/\n", "code.sh": "mkdir -p scripts/extra.d; printf 'exit 0\\n' > scripts/extra.d/late.sh\n", "code.txt": bad}, reason: "gate-program the gate program changed while the gate ran"},
		{name: "skip_worktree_flag_then_edit_fails", files: map[string]string{"code.sh": "git update-index --skip-worktree gate.cfg; " + setStrict0 + "\n", "code.txt": bad}},
		{name: "textconv_config_then_edit_fails", files: map[string]string{"code.sh": "git config diff.n.textconv true; printf 'gate.cfg diff=n\\n' > .gitattributes; " + setStrict0 + "\n", "code.txt": bad}},
		{name: "file_mode_false_then_mode_change_fails", files: map[string]string{"code.sh": "git config core.fileMode false; chmod 755 gate.cfg\n"}, reason: "gate-program the gate program changed while the gate ran: gate.cfg"},
		{name: "autocrlf_then_crlf_only_change_fails", files: map[string]string{"code.sh": "git config core.autocrlf true; printf 'strict=1\\r\\n' > gate.cfg\n"}, reason: "gate-program the gate program changed while the gate ran: gate.cfg"},
		{name: "replace_ref_then_edit_fails", files: map[string]string{"code.sh": "b=$(git rev-parse HEAD:gate.cfg); n=$(printf 'strict=0\\n' | git hash-object -w --stdin); git replace -f $b $n; " + setStrict0 + "\n", "code.txt": bad}},
		{name: "fifo_in_a_listed_directory_fails_and_does_not_hang", files: map[string]string{"code.sh": "mkfifo scripts/pipe\n"}, reason: "not a regular file"},
		{name: "nested_repository_in_a_listed_directory_fails", files: map[string]string{"code.txt": bad}, setup: "mkdir -p scripts/sub && (cd scripts/sub && git init -q -b main . && printf 'exit 0\\n' > z.sh && git add z.sh && git commit -q -m s)"},
		{name: "base_moved_on_and_head_untouched_runs_the_new_base_program", files: map[string]string{"tests_new.txt": "x\n"}, moved: "printf 'grep -q RULE2 code.txt || { echo no rule2; exit 1; }\\n' >> scripts/check.sh", reason: "no rule2"},
		{name: "base_moved_harmlessly_and_head_untouched_passes", files: map[string]string{"tests_new.txt": "x\n"}, moved: "printf '# a comment\\n' >> scripts/check.sh", pass: true},
	}
}

func programProjectOf() model.Project {
	return model.Project{
		Name: "demo", Gate: "make check", TestReport: "reports/*.xml",
		GateProgram: []string{"Makefile", "GNUmakefile", "makefile", "scripts/", "gate.cfg"},
	}
}

// gitIn runs git or sh text in dir and fails the test when it fails.
func shIn(t *testing.T, dir, text string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "sh", "-c", text) //nolint:gosec // test helper
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin:/opt/homebrew/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

func writeIn(t *testing.T, dir, name, text string) {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
}

// programRepo makes the source repository at the base commit, then the head of the case. It returns the repository, the head and the tip of the base.
func programRepo(t *testing.T, c programCase) (repo, head, tip string) {
	t.Helper()

	repo = t.TempDir()
	shIn(t, repo, "git init -q -b main .")
	writeIn(t, repo, ".gitignore", "reports/\n")
	writeIn(t, repo, "Makefile", "check:\n\tsh scripts/check.sh\n")
	writeIn(t, repo, "scripts/check.sh", programCheckScript)
	writeIn(t, repo, "gate.cfg", "strict=1\n")
	writeIn(t, repo, "code.txt", "GOOD\n")
	shIn(t, repo, "git add .gitignore Makefile scripts gate.cfg code.txt && git commit -q -m base")
	shIn(t, repo, "git checkout -q -b feature")

	for name, text := range c.files {
		writeIn(t, repo, name, text)
	}

	if c.setup != "" {
		shIn(t, repo, c.setup)
	}

	shIn(t, repo, "git add . && git commit -q -m head")
	head = shIn(t, repo, "git rev-parse HEAD")
	shIn(t, repo, "git checkout -q main")

	if c.moved != "" {
		shIn(t, repo, c.moved+" && git commit -q -a -m moved")
	}

	return repo, head, shIn(t, repo, "git rev-parse main")
}

// runProgramJob runs the host command of the case in a real shell and returns the exit status and the log. The gate command is the real make.
func runProgramJob(t *testing.T, project model.Project, repo, head, tip, base string) (int, string) {
	t.Helper()

	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o750))
	require.NoError(t, exectest.Write(filepath.Join(bin, "gitleaks"), "#!/bin/sh\nexit 0\n"))

	command := hostCommand(project, nil, repo, head, root+"/beehave-gate-demo-"+head, base, tip)
	job := exec.CommandContext(t.Context(), "sh", "-c", command) //nolint:gosec // the job text under test
	job.Env = []string{"PATH=" + bin + ":/usr/bin:/bin:/opt/homebrew/bin", "HOME=" + t.TempDir()}
	out, err := job.CombinedOutput()

	if err == nil {
		return 0, string(out)
	}

	var exit *exec.ExitError

	require.ErrorAs(t, err, &exit, string(out))

	return exit.ExitCode(), string(out)
}

func TestProgram_TheHostJobJudgesTheHeadWithTheGateProgramOfTheBase(t *testing.T) {
	t.Parallel()

	for _, c := range programCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			repo, head, tip := programRepo(t, c)
			code, out := runProgramJob(t, programProjectOf(), repo, head, tip, "")

			if c.pass {
				require.Zero(t, code, out)
			} else {
				require.NotZero(t, code, out)
			}

			require.Contains(t, out, c.reason, out)
		})
	}
}

func TestProgram_TheCandidateJobJudgesTheHeadWithTheGateProgramOfTheBase(t *testing.T) {
	t.Parallel()

	for _, c := range programCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			repo, head, tip := programRepo(t, c)
			code, out := runProgramJob(t, programProjectOf(), repo, head, tip, "main")

			if c.pass {
				require.Zero(t, code, out)
			} else {
				require.NotZero(t, code, out)
			}

			require.Contains(t, out, c.reason, out)
		})
	}
}

func TestProgram_AJobWithNoGateProgramRunsTheHeadProgramAsBefore(t *testing.T) {
	t.Parallel()

	repo, head, tip := programRepo(t, programCase{files: map[string]string{"Makefile": programWeakMakefile, "code.txt": "BAD\n"}})
	project := programProjectOf()
	project.GateProgram = nil

	code, out := runProgramJob(t, project, repo, head, tip, "")

	require.Zero(t, code, out)
}

func TestProgram_ABaseCommitThatTheSourceDoesNotHoldFailsTheJobWithTheMarker(t *testing.T) {
	t.Parallel()

	repo, head, _ := programRepo(t, programCase{files: map[string]string{"code.txt": "GOOD more\n"}})

	code, out := runProgramJob(t, programProjectOf(), repo, head, strings.Repeat("0", 40), "")

	require.NotZero(t, code, out)
	require.Contains(t, out, programMarker+" the base commit "+strings.Repeat("0", 40)+" cannot be read")
}
