package job_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/job"
)

const (
	noTimeout = "FAIL: the job runs a test command without timeout in front of it: "
	bulkFail  = "FAIL: the job stages in bulk ("
)

func TestJobScriptRefusesATestCommandWithNoTimeout(t *testing.T) {
	t.Parallel()

	refused := []string{
		"cargo test --workspace", "cargo nextest run", "pytest -n 4", "uv run pytest tests", "make test-unit",
		"bats tests/x.bats", "if cargo test; then echo ok; fi", "cd /w && cargo test",
		"(cd x && cargo test)", "bash -c 'cargo test'", `ssh box "cargo test"`, "timeout 5 sleep 1 & cargo test",
		"cargo +nightly test", "FOO=1 uv run pytest", "sudo -E bats x", "bash <<EOF\ncargo test\nEOF",
		"run_t() { timeout 600 \"$@\"; }\nrun_t cargo test",
		"function run_p {\n  timeout 600 \"$@\"\n}\nrun_p pytest -n 4",
		"run_b() {\n  timeout 600 \"$@\"\n}\nrun_b bats tests/x.bats",
		"uv --quiet run pytest", "uv -q run pytest",
		"uv --directory x run pytest", "run_t() { timeout 600 \"$@\"; }\nrun_t bash -c \"cargo test\"",
		"uvx pytest", "uvx --from x pytest -n 4", "uv tool run pytest", "uv -q tool run pytest",
		"run_t() { timeout 600 \"$@\"; }\nrun_t \"cargo test\"", "run_s() {\n  sh -c \"$1\"\n}\nrun_s \"bats tests/x.bats\"",
		"cat > t.sh <<EOF\ncargo test\nEOF\nbash t.sh", "cat <<EOF >> run.sh\npytest -n 4\nEOF\n./run.sh",
		"tee t.sh <<EOF\nmake test-unit\nEOF\nchmod +x t.sh && ./t.sh", "cat > t.sh <<EOF\nbats tests/\nEOF\n. t.sh", "cat > t.sh <<EOF\nbats tests/\nEOF\nsudo bash -x t.sh\nX=1",
		"cat > \"t.sh\" <<EOF\ncargo test\nEOF\nbash t.sh", "log_msg() { echo \"$*\"; }\nlog_msg running cargo test",
		"r() { \"${@}\"; }\nr \"pytest\"", "r() { sh -c \"${1}\"; }\nr \"bats x\"",
		"r() { if true; then \"$@\"; fi; }\nr \"cargo test\"", "r() { for i in 1; do \"$@\"; done; }\nr \"pytest\"",
		"X=\"a b\" pytest", "sudo -E pytest", "env -i pytest", "runner -m pytest", "nohup setsid pytest",
		"if ! pytest; then :; fi", "nice -n 5 pytest", "time pytest", "FOO='cargo test' echo hi",
		"log_msg() { echo \"$*\"; }\nlog_msg bash \"cargo test\"",
		"stdbuf -oL pytest", "stdbuf -o L pytest", "ionice -c 3 pytest", "ionice -n 5 bats tests/x.bats", "xargs pytest", "xargs -n 1 pytest",
		"f() { g; }\ng() { \"$@\"; }\ng pytest", "g() { \"$@\"; }\ng \"bats x\"",
		"r() { exec \"$@\"; }\nr \"bats x\"", "r() { sudo \"$@\"; }\nr \"bats x\"", "r() { env \"$@\"; }\nr \"bats x\"",
		"r() { nohup \"$@\"; }\nr \"bats x\"", "r() { time \"$@\"; }\nr \"bats x\"", "r() { nice -n 5 \"$@\"; }\nr \"bats x\"",
		"r() { gtimeout 5 \"$@\"; }\nr \"bats x\"", "r() { exec \"${@}\"; }\nr \"bats x\"", "r() { exec \"${1}\"; }\nr \"bats x\"",
		"r() { exec $*; }\nr \"bats x\"", "r() { exec \"$*\"; }\nr \"bats x\"", "py.test -x tests", "ssh -m hmac-sha2-256 box pytest", "r() { eval \"$1\"; }\nr \"bats x\"",
		"T=timeout\n$T 600 cargo test", "git log; log_msg() { echo \"$*\"; }; log_msg bats x",
		"uv run --with x pytest", "poetry run pytest", "pipx run pytest", "uv -q run -- pytest", "flock /tmp/l pytest",
		"strings=1\nbash -c 'echo hi; pytest'", "ssh box cargo test", "sh -c 'cargo test' ",
		"pytest -m unit", "sudo pytest -m x", "bash -c 'pytest -m unit'", "bats -m x", "sudo pytest -m 'not slow' tests",
		"cat > ./t.sh <<EOF\ncargo test\nEOF\nbash t.sh", "cat > t.sh <<EOF\ncargo test\nEOF\nbash ./t.sh",
		strings.Repeat("exec ", 21) + "pytest", "r() { sh -c \"$1\"; }\nr 'echo a\ncargo test'",
	}

	for _, script := range refused {
		out := job.Script(script)
		require.NotEmpty(t, out, script)
		assert.Contains(t, out[0], noTimeout, script)
	}
}

func TestJobScriptAcceptsATimedTestCommandAndALineThatOnlyNamesOne(t *testing.T) {
	t.Parallel()

	accepted := []string{
		"timeout 3600 cargo test --workspace", "timeout 600 uv run pytest -n 4", "cd /w && timeout -k 10 900 bats tests/x.bats",
		`echo "next: cargo test"`, "command -v bats || echo missing", "# cargo test is commented out",
		"die() { echo \"$*\" >&2; exit 1; }\nlog_msg() { echo \"$*\"; }\ntimeout 600 cargo test || die \"cargo test failed\"\nlog_msg 'running pytest -n 4'",
		"pip install pytest", "apt-get install -y bats", "git grep -n pytest", "uv pip install pytest", "uv sync --extra pytest",
		`docker run img sh -c "echo pytest"`, "/usr/bin/timeout 600 cargo test", "bash -c 'timeout 5 cargo test'",
		`ssh box "timeout 9 cargo test"`, "timeout 5 cargo +nightly test", "cat > run.sh <<EOF\ncargo test\nEOF",
		"cat <<-\"EOT\"\n\tbats tests/\n\tEOT", "timeout 600 uvx pytest", "uv tool install pytest", "uvx ruff check",
		"run_t() { timeout 600 \"$@\"; }\ndie() { echo \"$*\" >&2; exit 1; }\nnote() { logger \"$*\"; rm -f \"$1\"; }\ndie \"cargo test failed\"\nnote \"cargo test failed\"\ntimeout 5 run_t \"cargo test\"",
		"cat > t.sh <<EOF\ntimeout 600 cargo test\nEOF\nbash t.sh\ncat > u.sh <<EOF\ncargo test\nEOF\necho wrote u.sh",
		"X=\"a b\" timeout 5 pytest", "sudo -E timeout 5 pytest", "env timeout 5 pytest", "runner -m timeout 5 pytest",
		"nice -n 5 timeout 5 pytest", "gtimeout 5 cargo test", `bash -c "gtimeout 5 pytest"`, "",
		"ssh box timeout 9 cargo test", "stdbuf -oL timeout 5 pytest", "ionice -c 3 timeout 5 pytest", "()0", "L=echo\n$L \"bats failed (rc=$?)\"", "L=echo\n\"$L\" \"make test-unit done\"",
		"\"$ROOT/scripts/build.sh\" --release", "$HOME/bin/run.sh", "$CC -o x x.c", "${TOOL} run", "uv run ruff check", "poetry run ruff check",
		"echo hi; r() { exec \"$@\"; }; r echo hi", "command -v pytest", "command -V cargo test", "xargs timeout 5 pytest",
		"log_msg() { echo \"$*\"; }\nlog_msg \"bats x\"", "bash -c 'echo hi; timeout 5 pytest'", "ssh box echo hi",
	}

	for _, script := range accepted {
		assert.Empty(t, job.Script(script), script)
	}
}

func TestJobScriptNamesTheFileAHeredocWroteAndTheJobRunsLater(t *testing.T) {
	t.Parallel()

	out := job.Script("cat > t.sh <<EOF\ncargo test\nEOF\nbash t.sh")

	assert.Equal(t, []string{noTimeout + "cargo test (written to t.sh by a heredoc and run later). Write timeout <seconds> cargo test"}, out)
}

func TestJobScriptNamesTheLineAndTheFix(t *testing.T) {
	t.Parallel()

	out := job.Script("cd /w && cargo test")

	assert.Equal(t, []string{noTimeout + "cargo test. Write timeout <seconds> cargo test. If the line only names a test tool in a message, quote the message"}, out)
}

func TestJobScriptRefusesBulkStaging(t *testing.T) {
	t.Parallel()

	refused := []string{
		"git add -A", "git add .", "git add -u", "git add --all", "git commit -a -m x", "git commit -am x", "git -C /w add -A",
		"cd /w && git add .", `ssh box "git add -A"`, "ssh -p 22 box git add .", `ssh -o StrictHostKeyChecking=no box "git commit -a -m x"`,
		`gcloud compute ssh vm --command "git add -A"`, "S=ssh\n$S box \"git add -A\"", "run() { ssh box \"$@\"; }\nrun git add -A",
		"sh -c \"ssh box 'git add -A'\"", "bash -lc 'git add .'", `ssh box "git add -A" 2>&1`, "# it's the gate\ngit add -A",
		"xargs -n 1 git add -A", "sshpass -p x git add -A", "time git add -A", "sudo -u me git add -A",
		"git add -- .", "git add --update", "git stage -A", "git commit --all -m x", "git commit -m x -- .",
	}

	for _, script := range refused {
		out := job.Script(script)
		require.NotEmpty(t, out, script)
		assert.Contains(t, out[0], bulkFail+"git ", script)
	}
}

func TestJobScriptNamesTheFormAndTheFixForBulkStaging(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		[]string{"FAIL: the job stages in bulk (git add -A). Stage explicit paths only, or set GIT_INDEX_FILE for a private index"},
		job.Script("git add -A"))
}

func TestJobScriptAcceptsStagingThatIsNotBulk(t *testing.T) {
	t.Parallel()

	accepted := []string{
		"git add src/a.rs tests/b.rs", "git add -N .", "git commit -m x src/a.rs", "git status", "cat > note.txt <<'NOTE'\ngit add -A\nNOTE",
		"rm -rf build\nssh h \"rm -rf /x\"\ngit add src/a.rs", "GIT_INDEX_FILE=/tmp/idx git add -A", `ssh box "GIT_INDEX_FILE=/tmp/idx git add -A"`,
		"cat <<EOF | ssh box bash\ngit add -A\nEOF", "git --version", "git add -n .", "git add --dry-run -A", "git add --intent-to-add .",
		"git commit --message x -- src/a.rs", "git commit --verbose -m x src/a.rs", "git commit -m x", "echo b <<< c",
	}

	for _, script := range accepted {
		assert.Empty(t, job.Script(script), script)
	}
}

func TestJobScriptRefusesALineThatDoesNotParse(t *testing.T) {
	t.Parallel()

	for _, script := range []string{"pytest tests )", "if true; then\ngit add -A", `git add -A "x`, "echo a <<", "cat > t.sh <<EOF\nfi )\nEOF\nbash t.sh"} {
		out := job.Script(script)
		require.NotEmpty(t, out, script)
		assert.Contains(t, strings.Join(out, "\n"), "FAIL: the job has a line that does not parse as shell: ", script)
	}
}

func TestJobScriptReadsCommandSubstitutions(t *testing.T) {
	t.Parallel()

	assert.Contains(t, job.Script("x=$(git add -A)")[0], bulkFail)
	assert.Contains(t, job.Script("x=$(pytest tests)")[0], noTimeout)
	assert.Contains(t, job.Script("echo $(cargo test)")[0], noTimeout)
}

func TestJobScriptRefusesACommandItCannotRead(t *testing.T) {
	t.Parallel()

	out := job.Script("g=git\n$g add -A")

	assert.Equal(t, []string{"FAIL: the job runs a command the check cannot read ($g). Write the command name out, with no variable or wrapper that hides it"}, out)
}

func TestJobScriptRefusesATestCommandBehindAVariable(t *testing.T) {
	t.Parallel()

	for _, script := range []string{"T=timeout\n$T 600 cargo test", "T=timeout\n${T} 600 pytest", "R=timeout\n\"${R}\" cargo test"} {
		assert.NotEmpty(t, job.Script(script), script)
	}
}

func TestJobScriptStaysLinearInTheNumberOfHeredocFiles(t *testing.T) { //nolint:paralleltest // AllocsPerRun panics in a parallel test

	script := func(files int) string {
		var text strings.Builder

		for i := range files {
			fmt.Fprintf(&text, "cat > f%d.sh <<EOF\ntimeout 5 pytest\nEOF\nbash f%d.sh\n", i, i)
		}

		return text.String()
	}

	small, large := script(200), script(400)
	smallAllocs := testing.AllocsPerRun(1, func() { job.Script(small) })
	largeAllocs := testing.AllocsPerRun(1, func() { job.Script(large) })

	assert.Less(t, largeAllocs, 2.5*smallAllocs)
}

func TestJobScriptReadsTheWordsOfAStringThatDoesNotParse(t *testing.T) {
	t.Parallel()

	out := strings.Join(job.Script("bash -c 'cargo test )'"), "\n")

	assert.Contains(t, out, noTimeout+"cargo test ).")
}

func TestAWrapperFunctionWithNoCommandAndAHeredocWithNoFileAreNotRefused(t *testing.T) {
	t.Parallel()

	assert.Empty(t, job.Script("f() { timeout 5; }\nf() { sudo -E \"$@\"; }\nf() { tee -a <<EOF\nx\nEOF\n}"))
}

func TestAnyNumberOfWrappersIsDropped(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, job.Script(strings.Repeat("exec ", 21)+"pytest"))
	assert.NotEmpty(t, job.Script(strings.Repeat("nice ", 200)+"pytest"))
}

// seeds are scripts that cover the forms the job check reads.
var seeds = []string{
	"()0", "R='ssh h; $R '; $R rm x", "git add -A", "cd /w && cargo test", "cat > t.sh <<EOF\ncargo test\nEOF\nbash t.sh",
	"bash -c 'cargo test )'", "g=git\n$g add -A", "x=$(pytest tests)", "timeout 5 pytest", `echo "DONE-x rc=0"`,
}

func TestJobScriptReadsEveryCutOfAScriptWithoutPanicking(t *testing.T) {
	t.Parallel()

	for _, script := range seeds {
		for end := range len(script) + 1 {
			assert.NotPanics(t, func() { job.Script(script[:end]) }, "%q cut at %d", script, end)
		}
	}
}

func FuzzJobScript(f *testing.F) {
	for _, script := range seeds {
		f.Add(script)
	}

	f.Fuzz(func(_ *testing.T, text string) { job.Script(text) })
}
