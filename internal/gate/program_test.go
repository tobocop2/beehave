package gate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/model"
)

const (
	programCheckScript = `mkdir -p reports
printf '<testsuite tests="1"><testcase name="t"/></testsuite>\n' > reports/j.xml
[ -x scripts/run.sh ] || { echo "scripts/run.sh is not executable"; exit 1; }
[ -L scripts/link ] || { echo "scripts/link is not a link"; exit 1; }
[ -f code.sh ] && sh code.sh
for f in scripts/extra.d/*.sh scripts/sub/*.sh; do [ -f "$f" ] && . "$f"; done
if grep -q '^strict=1$' gate.cfg; then grep -q GOOD code.txt || { echo "code.txt is not GOOD"; exit 1; }; fi
`
	// programFakeDigest is head code that writes a digest tool first on the PATH. The tool prints the digest of the base gate.cfg for gate.cfg and runs the real tool for any other file.
	programFakeDigest = `d=${PATH%%:*}; rs=$(command -v sha256sum || command -v shasum)
case $rs in */shasum) ra='-a 256' ;; *) ra= ;; esac
h=$($rs $ra gate.cfg | { read -r x y; printf %s "$x"; })
printf '#!/bin/sh\nfor a; do f=$a; done\n[ "$f" = gate.cfg ] && { echo "%s  gate.cfg"; exit 0; }\nexec %s "$@"\n' "$h" "$rs" > "$d/${rs##*/}"
chmod 755 "$d/${rs##*/}"
`
	programWeakMakefile = "check:\n\t@mkdir -p reports; printf '<testsuite tests=\"1\"><testcase name=\"t\"/></testsuite>\\n' > reports/j.xml; exit 0\n"
)

// programLateFile is head code that adds a file to a listed directory during the gate.
const programLateFile = "mkdir -p scripts/extra.d; printf 'exit 0\\n' > scripts/extra.d/late.sh\n"

// programFakeTool is head code that writes a tool first on the PATH. The tool runs the shell text body.
func programFakeTool(name, body string) string {
	return `d=${PATH%%:*}; printf '#!/bin/sh\n` + body + `\n' > "$d/` + name + `"; chmod 755 "$d/` + name + "\"\n"
}

// programFakeFilter is head code that writes a tool first on the PATH. The tool runs the real tool and hides the file late.sh from its output.
func programFakeFilter(name string) string {
	return `d=${PATH%%:*}; real=$(command -v ` + name + `); printf '#!/bin/sh\n%s "$@" | grep -v late.sh\n' "$real" > "$d/` + name + `"; chmod 755 "$d/` + name + "\"\n"
}

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
		{name: "good_head_passes_with_an_executable_script_and_a_link_in_the_base", files: map[string]string{"tests_new.txt": "x\n"}, pass: true},
		{name: "link_retargeted_during_the_gate_fails", files: map[string]string{"code.sh": "ln -sfn gate.cfg scripts/link\n"}, reason: "gate-program the gate program changed while the gate ran: scripts/link"},
		{name: "link_replaced_by_a_regular_copy_during_the_gate_fails", files: map[string]string{"code.sh": "rm scripts/link; cp scripts/check.sh scripts/link\n"}, reason: "gate-program the gate program changed while the gate ran: scripts/link"},
		{name: "execute_bit_removed_during_the_gate_fails", files: map[string]string{"code.sh": "chmod 644 scripts/run.sh\n"}, reason: "gate-program the gate program changed while the gate ran: scripts/run.sh"},
		{name: "snapshot_folder_rewritten_by_head_code_still_fails", files: map[string]string{"code.sh": "mkdir -p ../snap/files; printf 'strict=0\\n' > ../snap/files/gate.cfg; " + setStrict0 + "\n", "code.txt": bad}, reason: "gate-program the gate program changed while the gate ran: gate.cfg"},
		{name: "fake_digest_tool_first_on_the_path_still_fails", files: map[string]string{"code.sh": programFakeDigest + setStrict0 + "\n", "code.txt": bad}, reason: "gate-program the gate program changed while the gate ran: gate.cfg"},
		{name: "fake_readlink_first_on_the_path_still_fails", files: map[string]string{"code.sh": programFakeTool("readlink", "echo check.sh") + "ln -sfn gate.cfg scripts/link\n"}, reason: "gate-program the gate program changed while the gate ran: scripts/link"},
		{name: "fake_find_first_on_the_path_still_fails", files: map[string]string{"code.sh": programFakeFilter("find") + programLateFile}, reason: "gate-program the gate program changed while the gate ran: scripts/extra.d/late.sh"},
		{name: "fake_sort_first_on_the_path_still_fails", files: map[string]string{"code.sh": programFakeFilter("sort") + programLateFile}, reason: "gate-program the gate program changed while the gate ran: scripts/extra.d/late.sh"},
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
	writeIn(t, repo, "scripts/run.sh", "exit 0\n")
	shIn(t, repo, "chmod 755 scripts/run.sh && ln -s check.sh scripts/link")
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

	command := hostCommand(project, nil, repo, head, root+"/beehest-gate-demo-"+head, base, tip)
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

func TestProgram_TheSnapshotGitCallsReadNoReplaceRefsAndNoConfigOfTheMachine(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	shIn(t, repo, "git init -q -b main . && printf 'one\\n' > a.txt && git add a.txt && git commit -q -m one")
	blob := shIn(t, repo, "git rev-parse HEAD:a.txt")
	other := shIn(t, repo, "printf 'two\\n' | git hash-object -w --stdin")
	shIn(t, repo, "git replace "+blob+" "+other)

	machine := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(machine, []byte("[core]\n\tabbrev = 12\n"), 0o600))

	script := "gpbin=$(command -v git); gpenv=$(command -v env); " + programGit(false) + "; " + programShell + "\ngpgit cat-file blob " + blob + "; gpgit rev-parse --short HEAD"
	cmd := exec.CommandContext(t.Context(), "sh", "-c", script) //nolint:gosec // the job text under test
	cmd.Dir = repo
	cmd.Env = []string{"PATH=/usr/bin:/bin:/opt/homebrew/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=" + machine}
	out, err := cmd.CombinedOutput()

	require.NoError(t, err, string(out))
	require.Equal(t, []string{"one", strings.TrimSpace(shIn(t, repo, "git rev-parse --short=7 HEAD"))}, strings.Fields(string(out)))
}

func TestProgram_AFakeCatFirstOnThePathDoesNotHideTheReasonOfTheFailedCandidateJob(t *testing.T) {
	t.Parallel()

	c := programCase{files: map[string]string{"code.sh": programFakeTool("cat", "exit 1") + "printf 'strict=0\\n' > gate.cfg\n", "code.txt": "BAD\n"}}
	repo, head, tip := programRepo(t, c)

	code, out := runProgramJob(t, programProjectOf(), repo, head, tip, "main")

	require.NotZero(t, code, out)
	require.Contains(t, out, programMarker+" the gate program changed while the gate ran: gate.cfg")
}

func TestProgram_ABaseCommitThatOnlyTheSourceHoldsIsFetchedFromTheCloneURL(t *testing.T) {
	t.Parallel()

	repo, head, _ := programRepo(t, programCase{files: map[string]string{"code.txt": "GOOD more\n"}})
	shIn(t, repo, "git checkout -q --detach main && printf '# a comment\\n' >> scripts/check.sh && git commit -q -a -m onlyref && git update-ref refs/pull/1/head HEAD && git checkout -q main")
	tip := shIn(t, repo, "git rev-parse refs/pull/1/head")

	// A file URL sends only the objects that a ref reaches, so the clone lacks the base commit and the job fetches it from the URL.
	code, out := runProgramJob(t, programProjectOf(), "file://"+repo, head, tip, "")

	require.Zero(t, code, out)
}

func TestProgram_ABaseCommitThatTheSourceDoesNotHoldFailsTheJobWithTheMarker(t *testing.T) {
	t.Parallel()

	repo, head, _ := programRepo(t, programCase{files: map[string]string{"code.txt": "GOOD more\n"}})

	code, out := runProgramJob(t, programProjectOf(), repo, head, strings.Repeat("0", 40), "")

	require.NotZero(t, code, out)
	require.Contains(t, out, programMarker+" the base commit "+strings.Repeat("0", 40)+" cannot be read")
}
