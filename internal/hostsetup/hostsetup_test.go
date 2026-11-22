package hostsetup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
)

const (
	watchdogUnit = "idle-selfdestruct.service"
	pinnedGo     = "go1.27.4"
	done         = "HOST-SETUP-DONE"
	noWatchdog   = "no self-delete watchdog runs on this host"
	fakeMakefile = "GOLANGCI_LINT_VERSION := v2.15.0\nGOTESTSUM_VERSION := v1.13.0\nAI_RULEZ_VERSION := v5.9.2\n" +
		"GITLEAKS_VERSION := v8.30.1\nGREMLINS_VERSION := v0.6.0\nGO_TEST_COVERAGE_VERSION := v2.20.1\nGOVULNCHECK_VERSION := v1.9.0\n" +
		"DEADCODE_VERSION := golang.org/x/tools/cmd/deadcode@v0.51.0\n"
	unitProof = "systemd-run --user --wait --pipe"
	// controllerKey is the public key that the root stage puts in the authorized_keys of the job user.
	controllerKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIControllerKeyOfTheTests controller@laptop\n"
)

// goInstalledTools are the fakes in ~/go/bin, each with the first line of its version output.
var goInstalledTools = map[string]string{
	"golangci-lint":    "golangci-lint has version 2.15.0 built with go1.27.4",
	"ai-rulez":         "ai-rulez version 5.9.2",
	"gotestsum":        "gotestsum version v1.13.0",
	"gitleaks":         "version is set by build process",
	"gremlins":         "gremlins version dev linux/amd64",
	"go-test-coverage": "unused",
	"govulncheck":      "Go: go1.27.4\nScanner: govulncheck@v1.9.0",
	"deadcode":         "unused",
	"dolt":             "dolt version 1.82.4",
	"bd":               "bd version 1.3.2",
	"actionlint":       "v1.7.12",
}

// systemTools are the fakes on the default PATH of the user manager.
var systemTools = map[string]string{
	"uv":         "uv 0.13.6 (x86_64-unknown-linux-gnu)",
	"shellcheck": "ShellCheck - shell script analysis tool\nversion: 0.10.0",
	"bats":       "Bats 1.10.0",
}

// goModules are the module path and the version that the mod line of `go version -m` shows for a tool that go install built at its pin.
var goModules = map[string]string{
	"golangci-lint":    "github.com/golangci/golangci-lint/v2 v2.15.0",
	"gotestsum":        "gotest.tools/gotestsum v1.13.0",
	"gitleaks":         "github.com/zricethezav/gitleaks/v8 v8.30.1",
	"gremlins":         "github.com/go-gremlins/gremlins v0.6.0",
	"actionlint":       "github.com/rhysd/actionlint v1.7.12",
	"go-test-coverage": "github.com/vladopajic/go-test-coverage/v2 v2.20.1",
	"govulncheck":      "golang.org/x/vuln v1.9.0",
	"deadcode":         "golang.org/x/tools v0.51.0",
}

// goInstalledPinned are the tools whose pin the script reads from the mod line.
var goInstalledPinned = []string{"golangci-lint", "gotestsum", "gitleaks", "gremlins", "actionlint", "go-test-coverage", "govulncheck", "deadcode"}

// provedTools are the tools that the script proves through a user unit.
var provedTools = []string{
	"go", "golangci-lint", "ai-rulez", "gotestsum", "gitleaks", "gremlins", "go-test-coverage", "govulncheck",
	"deadcode", "dolt", "bd", "jq", "uv", "shellcheck", "actionlint", "bats",
}

// fakeHost is one machine for the script: a clone, a home, a folder that stands for /, the work root and the fakes.
type fakeHost struct {
	t                                                           *testing.T
	root, scripts, fakes                                        *os.Root
	base, clone, home, sysroot, workRoot, bin, log, down, state string
	jobKey                                                      string
	env                                                         []string
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()

	// The script refuses a work root that is not its canonical path, so the base holds no link (macOS puts /var under /private).
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	h := &fakeHost{
		t: t, root: openRoot(t, base), scripts: openRoot(t, "../../scripts"), fakes: openRoot(t, "testdata"), base: base, clone: filepath.Join(base, "root/src/clone"), home: filepath.Join(base, "home"),
		sysroot: filepath.Join(base, "root"), workRoot: filepath.Join(base, "work"), bin: filepath.Join(base, "bin"), log: filepath.Join(base, "log"),
		down: filepath.Join(base, "dl"), state: filepath.Join(base, "state"),
	}
	tmp := filepath.Join(base, "tmp")

	for _, dir := range []string{h.clone + "/scripts", h.home + "/go/bin", h.sysroot + "/usr/local/go/bin", h.sysroot + "/usr/local/bin", h.workRoot, h.bin, h.down, h.state, tmp} {
		require.NoError(t, h.root.MkdirAll(h.rel(dir), 0o750))
	}

	for _, script := range []string{"host-setup.sh", "host-tools.sh", "metadata-guard.nft", "metadata-guard.service"} {
		h.copy(h.scripts, script, filepath.Join(h.clone, "scripts", script), h.write)
	}

	h.write(filepath.Join(h.clone, "go.mod"), "module example.com/h\n\ngo 1.27\n\ntoolchain "+pinnedGo+"\n")
	h.write(filepath.Join(h.clone, "Makefile"), fakeMakefile)
	h.write(h.log, "")
	h.jobKey = filepath.Join(base, "controller.pub")
	h.write(h.jobKey, controllerKey)
	h.write(filepath.Join(h.state, "linger-builder"), "") // the root stage turned linger on for the job user, who the tests call builder
	h.installFakes()
	h.installUbuntuEnvironmentFiles()

	jq, err := exec.LookPath("jq")
	require.NoError(t, err, "the tests of the host setup need jq")

	h.env = []string{
		"PATH=" + h.bin + ":/usr/bin:/bin", "HOME=" + h.home, "TMPDIR=" + tmp, "HOST_SETUP_SYSROOT=" + h.sysroot,
		"FAKE_LOG=" + h.log, "FAKE_BIN=" + h.bin, "FAKE_STATE=" + h.state, "FAKE_DL=" + h.down, "REAL_JQ=" + jq,
		"REAL_REALPATH=" + gnuRealpath(t), "FAKE_BASE=" + base, "FAKE_LOGIN_UID=" + strconv.Itoa(os.Getuid()),
	}

	return h
}

// gnuRealpath finds a realpath that knows -m: realpath on Linux (GNU or uutils), grealpath on macOS, whose realpath has no -m.
func gnuRealpath(t *testing.T) string {
	t.Helper()

	name := "realpath"
	if runtime.GOOS == "darwin" {
		name = "grealpath"
	}

	path, err := exec.LookPath(name)
	require.NoError(t, err, "the scripts tests need GNU realpath (brew install coreutils)")

	return path
}

// owns makes the fake stat report the user id as the owner of the path. The user id - makes the fake stat fail for the path.
func (h *fakeHost) owns(uid, path string) {
	h.t.Helper()

	owners := filepath.Join(h.state, "owners")
	text := ""
	if _, err := h.root.Stat(h.rel(owners)); err == nil {
		text = h.read(owners)
	}

	h.write(owners, text+uid+" "+path+"\n")
}

// rootActions lists the sudo calls of the log, other than the check that sudo needs no password.
func rootActions(log string) []string {
	var actions []string

	for line := range strings.SplitSeq(log, "\n") {
		if strings.HasPrefix(line, "sudo ") && line != "sudo -n -l" {
			actions = append(actions, line)
		}
	}

	return actions
}

// workRootActions lists the sudo calls of the log that name the work root.
func workRootActions(log, workRoot string) []string {
	var actions []string

	for _, line := range rootActions(log) {
		if strings.Contains(line, " "+workRoot) {
			actions = append(actions, line)
		}
	}

	return actions
}

func (h *fakeHost) installFakes() {
	h.t.Helper()

	fakes, err := fs.ReadDir(h.fakes.FS(), "fakes")
	require.NoError(h.t, err)

	for _, fake := range fakes {
		h.copy(h.fakes, filepath.Join("fakes", fake.Name()), filepath.Join(h.bin, fake.Name()), h.program)
	}

	for name, line := range goInstalledTools {
		h.tool(filepath.Join(h.home, "go/bin", name), line)
	}

	for name, line := range systemTools {
		h.tool(filepath.Join(h.bin, name), line)
	}

	h.program(filepath.Join(h.sysroot, "usr/local/go/bin/go"), fakeGo(pinnedGo))
}

// installUbuntuEnvironmentFiles writes the two environment.d files of a stock Ubuntu 24.04: the link to /etc/environment, which sets
// an absolute quoted PATH, and the file of snapd, which appends to PATH. Both sort after a file with a number prefix below 990.
func (h *fakeHost) installUbuntuEnvironmentFiles() {
	h.t.Helper()
	h.write(filepath.Join(h.sysroot, "usr/lib/environment.d/99-environment.conf"), "PATH=\""+h.bin+":/usr/bin:/bin\"\n")
	h.write(filepath.Join(h.sysroot, "usr/lib/environment.d/990-snapd.conf"), "PATH=$PATH:/snap/bin\n")
}

// tool writes a fake tool that prints its version text.
func (h *fakeHost) tool(path, text string) {
	h.t.Helper()
	h.program(path, "#!/bin/sh\nprintf '%s\\n' '"+text+"'\n")
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })

	return root
}

// rel turns a path under the temporary folder into a name inside its root.
func (h *fakeHost) rel(path string) string {
	h.t.Helper()

	name, err := filepath.Rel(h.base, path)
	require.NoError(h.t, err)

	return name
}

func (h *fakeHost) write(path, text string) {
	h.t.Helper()
	require.NoError(h.t, h.root.MkdirAll(filepath.Dir(h.rel(path)), 0o750))
	require.NoError(h.t, h.root.WriteFile(h.rel(path), []byte(text), 0o644))
}

// program writes a program that the script starts, through the helper that holds the fork lock.
func (h *fakeHost) program(path, text string) {
	h.t.Helper()
	require.NoError(h.t, h.root.MkdirAll(filepath.Dir(h.rel(path)), 0o750))
	require.NoError(h.t, exectest.Write(path, text))
}

// readSource reads a file of the scripts folder.
func (h *fakeHost) readSource(name string) string {
	h.t.Helper()

	data, err := h.scripts.ReadFile(name)
	require.NoError(h.t, err)

	return string(data)
}

func (h *fakeHost) read(path string) string {
	h.t.Helper()

	data, err := h.root.ReadFile(h.rel(path))
	require.NoError(h.t, err)

	return string(data)
}

// copy copies a file of the source root to a path under the temporary folder, with write or program.
func (h *fakeHost) copy(source *os.Root, from, to string, put func(path, text string)) {
	h.t.Helper()

	data, err := source.ReadFile(from)
	require.NoError(h.t, err)
	put(to, string(data))
}

// run runs the job stage of the script of the clone with the arguments and the extra environment, and returns its output.
func (h *fakeHost) run(extra []string, args ...string) (string, error) {
	h.t.Helper()

	return h.sh(extra, filepath.Join(h.clone, "scripts/host-setup.sh"), append([]string{"--stage", "job"}, args...)...)
}

// rootSetup runs the root stage as root, from a clone that root and the admin user own: the fake id prints 0, and SUDO_UID names the owner of the files.
func (h *fakeHost) rootSetup(extra ...string) (string, error) {
	h.t.Helper()

	env := append([]string{"FAKE_UID=0", "SUDO_UID=" + strconv.Itoa(os.Getuid())}, extra...)

	return h.sh(env, filepath.Join(h.clone, "scripts/host-setup.sh"), "--stage", "root", "--watchdog", watchdogUnit, "--job-key", h.jobKey)
}

// sh runs a shell script of the temporary folder with the arguments and the extra environment, and returns its output.
func (h *fakeHost) sh(extra []string, script string, args ...string) (string, error) {
	h.t.Helper()

	cmd := exec.CommandContext(h.t.Context(), "sh")
	cmd.Args = append(cmd.Args, script)
	cmd.Args = append(cmd.Args, args...)
	cmd.Dir = h.clone
	cmd.Env = append(append([]string{}, h.env...), extra...)
	out, err := cmd.CombinedOutput()

	return string(out), err
}

func (h *fakeHost) setup(extra ...string) (string, error) {
	h.t.Helper()

	return h.run(extra, "--work-root", h.workRoot, "--watchdog", watchdogUnit)
}

func (h *fakeHost) record() string {
	h.t.Helper()

	return h.read(filepath.Join(h.workRoot, ".beehave/host-setup.record"))
}

func (h *fakeHost) envFile() string {
	return filepath.Join(h.home, ".config/environment.d/zz-beehave.conf")
}

// setModule makes the fake go print another module and version, split by a space, on the mod line of a tool. The text "-" prints no mod line.
func (h *fakeHost) setModule(tool, module string) {
	h.t.Helper()
	h.write(filepath.Join(h.state, "mod-"+tool), module+"\n")
}

// fakeGo is a go command of a version. With `version -m` it prints what go prints for a binary: the Go version, the path line,
// the mod line with real tabs and the module of the tool, and a dep line. A file mod-<tool> in FAKE_STATE changes the module and version.
// With `env GOPATH` it prints FAKE_GOPATH, or ~/go when that is not set.
func fakeGo(version string) string {
	var cases strings.Builder

	for _, name := range slices.Sorted(maps.Keys(goModules)) {
		cases.WriteString("\t" + name + ") module='" + goModules[name] + "' ;;\n")
	}

	return "#!/bin/sh\nif [ \"$1 $2\" = 'version -m' ]; then\n" +
		"\t[ -f \"$3\" ] || { echo \"$3: no such file\" >&2; exit 1; }\n" +
		"\tname=${3##*/}\n\tmodule='example.com/tool v0.1.0'\n\tcase $name in\n" + cases.String() + "\tesac\n" +
		"\t[ ! -f \"$FAKE_STATE/mod-$name\" ] || module=$(cat \"$FAKE_STATE/mod-$name\")\n" +
		"\tif [ \"$module\" = - ]; then printf '%s: " + version + "\\n' \"$3\"; exit 0; fi\n" +
		"\tset -- \"$3\" $module\n" +
		"\tprintf '%s: " + version + "\\n\\tpath\\t%s/cmd\\n\\tmod\\t%s\\t%s\\th1:x=\\n\\tdep\\texample.com/dep\\tv1.0.0\\th1:y=\\n' \"$1\" \"$2\" \"$2\" \"$3\"\n" +
		"\texit 0\nfi\n" +
		"if [ \"$1 $2\" = 'env GOPATH' ]; then echo \"${FAKE_GOPATH:-$HOME/go}\"; exit 0; fi\n" +
		"echo 'go version " + version + " linux/amd64'\n"
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	return lines[len(lines)-1]
}

func TestHostSetup_RefusesToFinishWithoutAnActiveWatchdog(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		env        string
		installsAt bool
	}{
		{name: "an_inactive_unit", env: "FAKE_WD_ACTIVE=inactive"},
		{name: "a_disabled_unit", env: "FAKE_WD_ENABLED=disabled"},
		{name: "a_unit_that_restarts_only_on_failure", env: "FAKE_WD_RESTART=on-failure"},
		{name: "a_watchdog_that_stops_during_the_run", env: "FAKE_STOP_WATCHDOG=1", installsAt: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.setup(tc.env)

			require.Error(t, err)
			assert.Contains(t, out, noWatchdog)
			assert.NotContains(t, out, done)
			assert.Equal(t, tc.installsAt, strings.Contains(h.read(h.log), "make setup"), "the installs run only after a first watchdog check passes")
		})
	}
}

func TestHostSetup_RefusesAWorkRootThatSystemdWouldExpand(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, workRoot, message string }{
		{name: "a_percent_sign", workRoot: "/srv/jobs%h", message: "must not hold a percent or dollar sign"},
		{name: "a_dollar_sign", workRoot: "/srv/$USER", message: "must not hold a percent or dollar sign"},
		{name: "a_control_character", workRoot: "/srv/a\tb", message: "must hold no control character"},
		{name: "a_newline", workRoot: "/srv/a\nb", message: "must hold no control character"},
		{name: "a_single_quote", workRoot: "/srv/a';x;'", message: "must not hold a single quote"},
		{name: "a_relative_path", workRoot: "srv/jobs", message: "must be an absolute path"},
		{name: "no_work_root", workRoot: "", message: "--work-root is required"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.run(nil, "--work-root", tc.workRoot, "--watchdog", watchdogUnit)

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.NotContains(t, h.read(h.log), "make setup", "the refusal comes before any change")
		})
	}
}

func TestHostSetup_RefusesBeforeAnyChange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, env, message, remove string
		args                       []string
	}{
		{name: "the_root_user", env: "FAKE_UID=0", message: "not as root"},
		{name: "an_arm_machine", env: "FAKE_ARCH=aarch64", message: "linux amd64 only"},
		{name: "a_clone_without_go_mod", remove: "go.mod", message: "holds no go.mod and Makefile"},
		{name: "a_clone_without_a_makefile", remove: "Makefile", message: "holds no go.mod and Makefile"},
		{name: "no_watchdog_named", message: "--watchdog is required", args: []string{"--work-root", "<work>"}},
		{name: "a_watchdog_that_is_no_unit_name", message: "is not a systemd unit name", args: []string{"--work-root", "<work>", "--watchdog", "a b"}},
		{name: "a_watchdog_that_starts_with_a_dash", message: "is not a systemd unit name", args: []string{"--work-root", "<work>", "--watchdog", "--user"}},
		{name: "an_unknown_argument", message: "unknown argument --cloud", args: []string{"--cloud", "gcp"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			if tc.remove != "" {
				require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.clone, tc.remove))))
			}

			args := []string{"--work-root", h.workRoot, "--watchdog", watchdogUnit}
			if tc.args != nil {
				args = nil
				for _, arg := range tc.args {
					args = append(args, strings.ReplaceAll(arg, "<work>", h.workRoot))
				}
			}

			out, err := h.run([]string{tc.env}, args...)

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.NotContains(t, h.read(h.log), "make setup", "the refusal comes before any change")
		})
	}
}

func TestHostSetup_RefusesAWorkRootThatIsNotTheLoginUsers(t *testing.T) {
	t.Parallel()

	const (
		notCanonical = "is not a canonical path"
		byHand       = "is not owned by the login user, so the script does not make the work root. The owner of <parent> makes it by hand: " +
			"sudo mkdir -m 0755 -- '<root>' && sudo chown builder:builder -- '<root>'\n"
	)

	// A work root of <base> stands for the folder of the test, which exists and holds no link.
	cases := []struct{ name, workRoot, message string }{
		{name: "the_root_folder", workRoot: "/", message: "is a system folder"},
		{name: "a_system_folder", workRoot: "/usr", message: "is a system folder"},
		{name: "a_link_to_the_root_folder", workRoot: "<base>/link", message: notCanonical},
		{name: "a_folder_of_another_user", workRoot: "<base>/work", message: "is not owned by the login user"},
		{name: "a_folder_the_login_user_cannot_write", workRoot: "<base>/work", message: "cannot write to the work root"},
		{name: "a_file", workRoot: "<base>/file", message: "is not a folder"},
		{name: "dot_dot_after_a_missing_folder", workRoot: "<base>/x/..", message: notCanonical},
		{name: "dot_dot_to_the_root_folder", workRoot: "/x/..", message: notCanonical},
		{name: "a_dot_part", workRoot: "<base>/a/./b", message: notCanonical},
		{name: "a_doubled_slash", workRoot: "<base>//b", message: notCanonical},
		{name: "a_trailing_slash", workRoot: "<base>/b/", message: notCanonical},
		{name: "a_trailing_slash_on_an_existing_folder", workRoot: "<base>/work/", message: notCanonical},
		{name: "a_missing_folder_whose_parent_is_missing", workRoot: "<base>/no/jobs", message: "Make the parent folder first"},
		{name: "a_missing_folder_whose_parent_is_a_file", workRoot: "<base>/file/jobs", message: "Make the parent folder first"},
		{name: "a_missing_folder_in_a_system_folder", workRoot: "/usr/beehave-missing-work-root", message: byHand},
		{name: "a_missing_folder_whose_parent_is_owned_by_root", workRoot: "<base>/theirs/jobs", message: byHand},
		{name: "a_missing_folder_whose_parent_is_owned_by_another_user", workRoot: "<base>/theirs/jobs", message: byHand},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			workRoot := strings.ReplaceAll(tc.workRoot, "<base>", h.base)

			switch tc.name {
			case "a_link_to_the_root_folder":
				require.NoError(t, h.root.Symlink("/", "link"))
			case "a_file", "a_missing_folder_whose_parent_is_a_file":
				h.write(filepath.Join(h.base, "file"), "")
			case "a_folder_of_another_user":
				h.owns("0", h.workRoot)
			case "a_folder_the_login_user_cannot_write":
				require.NoError(t, exectest.ChmodFolder(h.workRoot, 0o500))
			case "a_missing_folder_whose_parent_is_owned_by_root":
				require.NoError(t, h.root.Mkdir("theirs", 0o755))
				h.owns("0", filepath.Dir(workRoot))
			case "a_missing_folder_whose_parent_is_owned_by_another_user":
				require.NoError(t, h.root.Mkdir("theirs", 0o755))
				h.owns(strconv.Itoa(os.Getuid()+1), filepath.Dir(workRoot))
			}

			out, err := h.run(nil, "--work-root", workRoot, "--watchdog", watchdogUnit)

			require.Error(t, err)
			assert.Contains(t, out, strings.NewReplacer("<parent>", filepath.Dir(workRoot), "<root>", workRoot).Replace(tc.message))

			log := h.read(h.log)
			assert.NotContains(t, log, "make setup", "the refusal comes before any change")
			assert.Empty(t, rootActions(log), "the refusal comes before any root action")
			assert.NotContains(t, log, "mkdir ", "the refusal makes no folder")
			assert.NotContains(t, log, "\nchown ", "no folder changes owner")
		})
	}
}

func TestHostSetup_MakesOnlyAMissingWorkRoot(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	workRoot := filepath.Join(h.base, "jobs")

	out, err := h.run(nil, "--work-root", workRoot, "--watchdog", watchdogUnit)
	require.NoError(t, err, out)

	log := h.read(h.log)
	assert.Contains(t, log, "\nmkdir -m 0755 -- "+workRoot+"\n", "the login user makes the last folder only, without -p")
	assert.Empty(t, workRootActions(log, workRoot), "no root action names the work root")
	assert.NotContains(t, log, "chown ", "no folder changes owner")
	assert.NotContains(t, log, "sudo install -d", "install -d sets the owner and the mode of a folder that exists")

	info, err := h.root.Stat(h.rel(workRoot))
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "the mkdir makes the work root")

	h.write(h.log, "")
	out, err = h.run(nil, "--work-root", workRoot, "--watchdog", watchdogUnit)
	require.NoError(t, err, out)
	assert.NotContains(t, h.read(h.log), "mkdir -m 0755 -- "+workRoot+"\n", "an existing work root is left as it is")
}

func TestHostSetup_TheFakesRefuseAPathOutsideTheTestFolder(t *testing.T) {
	t.Parallel()

	// No parent of an outside path exists, so a fake without the guard fails in the real tool and writes nothing.
	const outside = "/beehave-no-such-folder/x"

	// More .. parts than the temporary folder has parts, so the path leaves it.
	up := strings.Repeat("/..", 32)

	cases := []struct {
		name, fake, path string
		args             []string
	}{
		{name: "mkdir", fake: "mkdir", path: outside, args: []string{"-m", "0755", "--", outside}},
		{name: "mkdir_through_dot_dot", fake: "mkdir", path: "<base>" + up + outside, args: []string{"--", "<base>" + up + outside}},
		{name: "mv_to_an_outside_path", fake: "mv", path: outside, args: []string{"<base>/file", outside}},
		{name: "mv_from_an_outside_path", fake: "mv", path: outside, args: []string{outside, "<base>/x"}},
		{name: "tar_into_an_outside_folder", fake: "tar", path: outside, args: []string{"-C", outside, "-xzf", "<base>/file"}},
		{name: "curl_to_an_outside_path", fake: "curl", path: outside, args: []string{"-fsSL", "--retry", "3", "-o", outside, "https://example.com/file"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			h.write(filepath.Join(h.base, "file"), "")
			h.write(filepath.Join(h.down, "file"), "")

			args := make([]string, 0, len(tc.args))
			for _, arg := range tc.args {
				args = append(args, strings.ReplaceAll(arg, "<base>", h.base))
			}

			out, err := h.sh(nil, filepath.Join(h.bin, tc.fake), args...)

			require.Error(t, err)
			assert.Equal(t, "fake "+tc.fake+": refused "+strings.ReplaceAll(tc.path, "<base>", h.base)+", which is outside the folder of the test\n", out)
		})
	}
}

func TestHostSetup_TheFakesRefuseEveryPathWithoutTheTestFolder(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  func(env []string) []string
	}{
		{name: "FAKE_BASE_is_not_set", env: func(env []string) []string {
			return slices.DeleteFunc(env, func(pair string) bool { return strings.HasPrefix(pair, "FAKE_BASE=") })
		}},
		{name: "FAKE_BASE_is_empty", env: func(env []string) []string { return append(env, "FAKE_BASE=") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// The path is inside the folder of the test, so a guard that passes every path makes only this folder.
			h := newFakeHost(t)
			inside := filepath.Join(h.base, "made")
			h.env = tc.env(h.env)

			out, err := h.sh(nil, filepath.Join(h.bin, "mkdir"), "--", inside)

			require.Error(t, err)
			assert.Equal(t, "fake mkdir: refused every path, because FAKE_BASE, the folder of the test, is not set\n", out)

			_, err = h.root.Stat(h.rel(inside))
			assert.ErrorIs(t, err, fs.ErrNotExist, "the fake makes no folder")
		})
	}
}

func TestHostSetup_TheFakeStatReportsNoOwnerWithoutTheTestFolder(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  func(env []string) []string
	}{
		{name: "FAKE_BASE_is_not_set", env: func(env []string) []string {
			return slices.DeleteFunc(env, func(pair string) bool { return strings.HasPrefix(pair, "FAKE_BASE=") })
		}},
		{name: "FAKE_BASE_is_empty", env: func(env []string) []string { return append(env, "FAKE_BASE=") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// The path exists and is outside no folder, so a fake that matches an empty FAKE_BASE reports the login user.
			h := newFakeHost(t)
			h.env = tc.env(h.env)

			out, err := h.sh(nil, filepath.Join(h.bin, "stat"), "-c", "%u", "--", h.base)

			require.Error(t, err)
			assert.Equal(t, "fake stat: refused every path, because FAKE_BASE, the folder of the test, is not set\n", out)
		})
	}
}

func TestHostSetup_PutsAStrayGoBackBeforeItReadsTheGoVersion(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	goRoot := h.sysroot + "/usr/local/go"
	h.write(filepath.Join(h.clone, "go.mod"), "module example.com/h\n\ngo 1.27\n")
	require.NoError(t, h.root.Rename(h.rel(goRoot), h.rel(goRoot+".old")))

	out, err := h.rootSetup()

	require.Error(t, err)
	assert.Contains(t, out, "has no toolchain line")
	assert.Contains(t, h.read(goRoot+"/bin/go"), pinnedGo, "the stray Go is back in place")

	_, err = h.root.Stat(h.rel(goRoot + ".old"))
	assert.ErrorIs(t, err, fs.ErrNotExist, "no backup is left")
}

func TestHostSetup_LeavesAnExistingWorkRootAsItIs(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	log := h.read(h.log)
	assert.Empty(t, workRootActions(log, h.workRoot), "no root action names an existing work root")
	assert.NotContains(t, log, "\nchown ", "no folder changes owner")
	assert.NotContains(t, out, "making the work root")
}

func TestHostSetup_RefusesAUvArchiveWhoseChecksumDiffers(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.bin, "uv"))))
	h.write(filepath.Join(h.down, "uv-x86_64-unknown-linux-gnu.tar.gz"), "not the pinned uv")

	out, err := h.rootSetup()

	require.Error(t, err)
	assert.Contains(t, out, "the checksum of uv-x86_64-unknown-linux-gnu.tar.gz does not match the pin")
	assert.NotContains(t, h.read(h.log), "sudo install -m 0755", "no uv is installed")
	assert.NotContains(t, out, rootDone)
}

func TestHostSetup_ReadsTheGoVersionFromGoMod(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, toolchain, sha, message string
		broken, installs              bool
	}{
		{name: "installs_the_toolchain_version", toolchain: "toolchain go1.27.9\n", installs: true},
		{name: "refuses_a_go_mod_without_a_toolchain_line", message: "has no toolchain line"},
		{name: "refuses_an_archive_whose_checksum_differs", toolchain: "toolchain go1.27.9\n", sha: strings.Repeat("0", 64), message: "does not match the pin"},
		{name: "keeps_the_old_go_when_the_archive_does_not_unpack", toolchain: "toolchain go1.27.9\n", broken: true, message: "is unchanged"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			goRoot := h.sysroot + "/usr/local/go"
			h.write(filepath.Join(h.clone, "go.mod"), "module example.com/h\n\ngo 1.27\n\n"+tc.toolchain)
			h.serveGo("go1.27.9", tc.sha, tc.broken)

			out, err := h.rootSetup()
			log := h.read(h.log)

			if !tc.installs {
				require.Error(t, err)
				assert.Contains(t, out, tc.message)
				assert.NotContains(t, log, "sudo rm -rf "+goRoot+"\n")
				assert.NotContains(t, log, "sudo mv ")
				assert.Contains(t, h.read(goRoot+"/bin/go"), pinnedGo, "the old Go stays")

				return
			}

			require.NoError(t, err, out)
			assert.Contains(t, log, "https://go.dev/dl/go1.27.9.linux-amd64.tar.gz")
			assert.Contains(t, log, "sudo tar -C "+goRoot+".new -xzf ")
			assert.Less(t, strings.Index(log, "sudo tar -C "+goRoot+".new"), strings.Index(log, "sudo mv "+goRoot+" "+goRoot+".old\n"),
				"the old Go moves out only after the new one unpacks")
			assert.Contains(t, log, "sudo mv "+goRoot+".new/go "+goRoot+"\n")
			assert.Contains(t, h.read(goRoot+"/bin/go"), "go1.27.9", "the pinned Go is in place")
			assert.Equal(t, rootDone, lastLine(out))
		})
	}
}

// serveGo puts a Go archive of the version and the go.dev list that names it in the downloads. An empty sha means the true one.
// A broken archive has the true sha but does not unpack.
func (h *fakeHost) serveGo(version, sha string, broken bool) {
	h.t.Helper()

	var archive bytes.Buffer

	zw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(zw)
	body := fakeGo(version)
	// The Go archive of go.dev holds its folders with mode 0755, so the folders do not take the umask of the test.
	for _, dir := range []string{"go/", "go/bin/"} {
		require.NoError(h.t, tw.WriteHeader(&tar.Header{Name: dir, Mode: 0o755, Typeflag: tar.TypeDir}))
	}

	require.NoError(h.t, tw.WriteHeader(&tar.Header{Name: "go/bin/go", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(body))
	require.NoError(h.t, err)
	require.NoError(h.t, tw.Close())
	require.NoError(h.t, zw.Close())

	if broken {
		archive.Truncate(archive.Len() / 2)
	}

	if sha == "" {
		sum := sha256.Sum256(archive.Bytes())
		sha = hex.EncodeToString(sum[:])
	}

	file := version + ".linux-amd64.tar.gz"
	h.write(filepath.Join(h.down, file), archive.String())
	h.write(filepath.Join(h.down, "go.json"), `[{"version":"`+version+`","files":[{"filename":"`+file+`","sha256":"`+sha+`"}]}]`)
}

func TestHostSetup_KeepsTheOldGoWhenTheSwapFails(t *testing.T) {
	t.Parallel()

	t.Run("the_new_go_does_not_move_in", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		goRoot := h.sysroot + "/usr/local/go"
		h.write(filepath.Join(h.clone, "go.mod"), "module example.com/h\n\ngo 1.27\n\ntoolchain go1.27.9\n")
		h.serveGo("go1.27.9", "", false)

		for run := 1; run <= 2; run++ {
			out, err := h.rootSetup("FAKE_MV_FAIL=" + goRoot + ".new/go")

			require.Error(t, err, "run %d", run)
			assert.Contains(t, out, "the new Go did not move into "+goRoot+". The old Go is back in place", "run %d", run)
			assert.Contains(t, h.read(goRoot+"/bin/go"), pinnedGo, "run %d: the old Go is in place", run)
			assert.Contains(t, h.read(h.log), "sudo mv "+goRoot+".old "+goRoot+"\n", "run %d: the old Go moves back", run)

			// The fake sudo runs no rm, so the test removes the go.new folder that the next run clears first.
			assert.Contains(t, h.read(h.log), "sudo rm -rf "+goRoot+".new\n")
			require.NoError(t, h.root.RemoveAll(h.rel(goRoot+".new")))
		}
	})

	t.Run("the_old_go_does_not_move_out", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		goRoot := h.sysroot + "/usr/local/go"
		h.write(filepath.Join(h.clone, "go.mod"), "module example.com/h\n\ngo 1.27\n\ntoolchain go1.27.9\n")
		h.serveGo("go1.27.9", "", false)

		for run := 1; run <= 2; run++ {
			out, err := h.rootSetup("FAKE_MV_FAIL=" + goRoot)

			require.Error(t, err, "run %d", run)
			assert.Contains(t, out, "the old Go did not move from "+goRoot+" to "+goRoot+".old. The old Go is kept in "+goRoot, "run %d", run)
			assert.Contains(t, h.read(goRoot+"/bin/go"), pinnedGo, "run %d: the old Go is in place", run)
			assert.NotContains(t, h.read(h.log), "sudo mv "+goRoot+".new/go", "run %d: the new Go does not move in", run)

			// The fake sudo runs no rm, so the test removes the go.new folder that the next run clears first.
			require.NoError(t, h.root.RemoveAll(h.rel(goRoot+".new")))
		}
	})

	t.Run("the_new_go_is_checked_before_the_backup_is_deleted", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		goRoot := h.sysroot + "/usr/local/go"
		h.write(filepath.Join(h.clone, "go.mod"), "module example.com/h\n\ngo 1.27\n\ntoolchain go1.27.9\n")
		h.serveGo("go1.27.9", "", false)

		for run := 1; run <= 2; run++ {
			h.write(h.log, "")
			out, err := h.rootSetup("FAKE_MV_SPOIL=" + goRoot + ".new/go")

			require.Error(t, err, "run %d", run)
			assert.Contains(t, out, "the Go in "+goRoot+" was not go1.27.9 after the swap. The old Go is back in place", "run %d", run)
			assert.Contains(t, h.read(goRoot+"/bin/go"), pinnedGo, "run %d: the old Go is back", run)

			log := h.read(h.log)
			assert.Contains(t, log, "sudo mv "+goRoot+".old "+goRoot+"\n", "run %d: the old Go moves back", run)
			assert.NotContains(t, log, "sudo rm -rf "+goRoot+".old", "run %d: the backup is deleted only after the new Go passes its check", run)
			assert.NotContains(t, log, "sudo rm -rf "+goRoot+".new "+goRoot+".old", "run %d: the backup is deleted only after the new Go passes its check", run)

			_, err = h.root.Stat(h.rel(goRoot + ".old"))
			require.ErrorIs(t, err, fs.ErrNotExist, "run %d: no backup is left", run)

			// The fake sudo runs no rm, so the test removes the go.new folder that the next run clears first.
			require.NoError(t, h.root.RemoveAll(h.rel(goRoot+".new")))
		}
	})

	t.Run("a_second_run_keeps_the_backup_when_the_go_in_place_failed_its_check", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		goRoot := h.sysroot + "/usr/local/go"
		h.write(filepath.Join(h.clone, "go.mod"), "module example.com/h\n\ngo 1.27\n\ntoolchain go1.27.9\n")
		h.serveGo("go1.27.9", "", false)

		// The new Go fails its check after the swap, and it does not move out again: the old Go stays in the backup.
		out, err := h.rootSetup("FAKE_MV_SPOIL="+goRoot+".new/go", "FAKE_MV_FAIL_TO="+goRoot+".new/go")

		require.Error(t, err)
		assert.Contains(t, out, "it did not move out. The old Go is in "+goRoot+".old. Move it back by hand")
		assert.Contains(t, h.read(goRoot+".old/bin/go"), pinnedGo, "the old Go is in the backup")
		assert.Contains(t, h.read(goRoot+"/bin/go"), "spoiled", "the Go that failed its check is in place")

		h.write(h.log, "")
		out, err = h.rootSetup()

		require.Error(t, err)
		assert.Contains(t, out, "host-tools: the Go in "+goRoot+" is not go1.27.9, and an old Go is in "+goRoot+".old. This script deletes neither folder.")

		log := h.read(h.log)
		assert.NotContains(t, log, "sudo rm ", "a run deletes no Go when the Go in place is not the pinned one")
		assert.NotContains(t, log, "sudo mv ", "a run moves no Go when both folders hold one")
		assert.NotContains(t, log, "\ncurl ", "the run stops before the download")
		assert.Contains(t, h.read(goRoot+".old/bin/go"), pinnedGo, "the old Go is still in the backup")

		// The user does what the message says: the Go that runs goes back in place. The next run then installs the pinned Go.
		require.NoError(t, h.root.RemoveAll(h.rel(goRoot)))
		require.NoError(t, h.root.Rename(h.rel(goRoot+".old"), h.rel(goRoot)))
		require.NoError(t, h.root.RemoveAll(h.rel(goRoot+".new")))

		out, err = h.rootSetup()

		require.NoError(t, err, out)
		assert.Contains(t, h.read(goRoot+"/bin/go"), "go1.27.9", "the pinned Go is in place")
	})

	t.Run("a_backup_beside_the_pinned_go_is_deleted", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		goRoot := h.sysroot + "/usr/local/go"
		h.program(goRoot+".old/bin/go", fakeGo("go1.27.0"))

		out, err := h.rootSetup()

		require.NoError(t, err, out)
		assert.Contains(t, out, "host-tools: Go "+pinnedGo+" is installed")
		assert.Contains(t, h.read(h.log), "sudo rm -rf "+goRoot+".old\n", "the backup goes when the pinned Go is in place")
	})

	t.Run("a_backup_without_a_go_is_put_back_first", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		goRoot := h.sysroot + "/usr/local/go"
		h.serveGo(pinnedGo, "", false)
		require.NoError(t, h.root.RemoveAll(h.rel(goRoot)))
		h.program(goRoot+".old/bin/go", fakeGo(pinnedGo))

		out, err := h.rootSetup()

		require.NoError(t, err, out)
		assert.Contains(t, h.read(goRoot+"/bin/go"), pinnedGo, "the old Go is back")

		log := h.read(h.log)
		assert.Contains(t, log, "sudo mv "+goRoot+".old "+goRoot+"\n")
		assert.NotContains(t, log, "sudo rm -rf "+goRoot+".old", "the only copy of Go is never deleted")
		assert.NotContains(t, log, "go.dev/dl/"+pinnedGo, "the Go that comes back is at the pin")
	})
}

func TestHostSetup_ProvesEachToolThroughAUserUnit(t *testing.T) {
	t.Parallel()

	t.Run("every_tool_runs", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		out, err := h.setup()

		require.NoError(t, err, out)
		assert.Equal(t, done, lastLine(out))
		// One unit for each tool, and one for each of the four metadata probes: ports 80 and 443 over IPv4 and IPv6.
		assert.Equal(t, len(provedTools)+4, strings.Count(h.read(h.log), unitProof+" --quiet --collect -p WorkingDirectory="+h.clone+" sh -c "))

		record := h.record()
		for _, name := range provedTools {
			assert.Contains(t, record, "\ntool "+name+" ", name)
		}

		for _, name := range goInstalledPinned {
			assert.Contains(t, record, "\ntool "+name+" "+goModules[name]+"\n", "the record holds the module and the version of "+name)
		}

		assert.Contains(t, record, "tool shellcheck version: 0.10.0\n")
		assert.Contains(t, out, record, "the script prints the record")
	})

	t.Run("a_tool_that_reads_its_input_does_not_end_the_list", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		h.program(filepath.Join(h.home, "go/bin/dolt"), "#!/bin/sh\ncat >/dev/null\necho 'dolt version 1.82.4'\n")

		out, err := h.setup()

		require.NoError(t, err, out)

		record := h.record()
		for _, name := range provedTools {
			assert.Contains(t, record, "\ntool "+name+" ", name)
		}
	})

	t.Run("a_missing_tool_fails", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.home, "go/bin/gremlins"))))

		out, err := h.setup()

		require.Error(t, err)
		assert.Contains(t, out, "gremlins does not run in a user unit")
		assert.NotContains(t, out, done)
	})
}

func TestHostSetup_FailsWhenAToolIsNotAtItsPin(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, tool, text, message string }{
		{name: "an_old_gotestsum", tool: "gotestsum", text: "gotest.tools/gotestsum v1.12.0", message: "gotestsum is not at its pin 1.13.0"},
		{name: "a_longer_version", tool: "gremlins", text: "github.com/go-gremlins/gremlins v0.6.01", message: "gremlins is not at its pin 0.6.0"},
		{name: "a_pseudo_version", tool: "gremlins", text: "github.com/go-gremlins/gremlins v0.6.0-0.20260101000000-abcdef123456", message: "gremlins is not at its pin 0.6.0"},
		{name: "a_build_from_a_checkout", tool: "gremlins", text: "github.com/go-gremlins/gremlins (devel)", message: "gremlins is not at its pin 0.6.0"},
		{name: "an_old_actionlint", tool: "actionlint", text: "github.com/rhysd/actionlint v1.7.11", message: "actionlint is not at its pin 1.7.12"},
		{name: "an_old_go_test_coverage", tool: "go-test-coverage", text: "github.com/vladopajic/go-test-coverage/v2 v2.20.0", message: "go-test-coverage is not at its pin 2.20.1"},
		{name: "an_old_govulncheck", tool: "govulncheck", text: "golang.org/x/vuln v1.8.0", message: "govulncheck is not at its pin 1.9.0"},
		{name: "a_newer_deadcode", tool: "deadcode", text: "golang.org/x/tools v0.52.0", message: "deadcode is not at its pin 0.51.0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			h.setModule(tc.tool, tc.text)

			out, err := h.setup()

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.Contains(t, out, tc.text, "the failure names the version found")
			assert.NotContains(t, out, done)
		})
	}
}

func TestHostSetup_FailsWhenABinaryHasNoModuleLine(t *testing.T) {
	t.Parallel()

	for _, tool := range []string{"gremlins", "deadcode"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			h.setModule(tool, "-")

			out, err := h.setup()

			require.Error(t, err)
			assert.Contains(t, out, tool+" does not run in a user unit: no mod line")
			assert.NotContains(t, out, done)
		})
	}
}

func TestHostSetup_PutsTheToolFoldersOnThePathOfTheUserManager(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	h.write(h.envFile(), "PATH=/stale\n")

	out, err := h.setup()

	require.NoError(t, err, out)
	assert.Equal(t, "PATH="+h.sysroot+"/usr/local/go/bin:"+h.home+"/go/bin:${PATH}\nBEEHAVE_BD_132="+h.home+"/go/bin/bd\n", h.read(h.envFile()))
	assert.Contains(t, h.unitEnvironment(), "PATH="+h.sysroot+"/usr/local/go/bin:"+h.home+"/go/bin:"+h.bin+":/usr/bin:/bin:/snap/bin\n")

	log := h.read(h.log)
	assert.Contains(t, log, "systemctl --user daemon-reload\n")
	assert.Less(t, strings.Index(log, "systemctl --user daemon-reload"), strings.Index(log, unitProof), "the reload comes before the proof")
	assert.Contains(t, log, "make setup PATH="+h.sysroot+"/usr/local/go/bin:"+h.home+"/go/bin:")
	assert.Contains(t, log, "make contract-tools-install PATH=")
}

func TestHostSetup_ReadsTheGoPathFromGo(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	gopath := filepath.Join(h.base, "gopath")
	for name, line := range goInstalledTools {
		h.tool(filepath.Join(gopath, "bin", name), line)
		require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.home, "go/bin", name))))
	}

	out, err := h.setup("FAKE_GOPATH=" + gopath + ":" + filepath.Join(h.base, "second"))

	require.NoError(t, err, out)
	assert.Equal(t, "PATH="+h.sysroot+"/usr/local/go/bin:"+gopath+"/bin:${PATH}\nBEEHAVE_BD_132="+gopath+"/bin/bd\n", h.read(h.envFile()))
	assert.Contains(t, h.read(h.log), "make setup PATH="+h.sysroot+"/usr/local/go/bin:"+gopath+"/bin:")
	assert.Contains(t, out, "host-tools: actionlint v1.7.12 is installed", "host-tools finds actionlint in the GOPATH")
	assert.Equal(t, done, lastLine(out))
}

func TestHostSetup_ASecondRunChangesNothing(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup()
	require.NoError(t, err, out)

	past := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, h.root.Chtimes(h.rel(h.envFile()), past, past))
	first := withoutTime(h.record())
	firstLog := h.read(h.log)
	assert.Contains(t, firstLog, "\nsystemctl --user daemon-reload\n", "the first run writes the environment file")
	h.write(h.log, "")

	out, err = h.setup()
	require.NoError(t, err, out)

	info, err := h.root.Stat(h.rel(h.envFile()))
	require.NoError(t, err)
	assert.True(t, info.ModTime().Equal(past), "the environment file is not written again")
	assert.Equal(t, first, withoutTime(h.record()))

	log := h.read(h.log)
	for _, change := range []string{"\nsystemctl --user daemon-reload", "\ncurl -fsSL"} {
		assert.NotContains(t, log, change)
	}
}

func withoutTime(record string) string {
	var kept []string

	for line := range strings.SplitSeq(record, "\n") {
		if !strings.HasPrefix(line, "at=") {
			kept = append(kept, line)
		}
	}

	return strings.Join(kept, "\n")
}

func TestHostSetup_AKernelWithoutLandlockIsAWarning(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, lsm, kernel, line string }{
		{name: "no_landlock_module", lsm: "lockdown,capability,yama,apparmor", kernel: "6.8.0-1015-gcp", line: "landlock warning lockdown,capability,yama,apparmor 6.8.0-1015-gcp"},
		{name: "a_kernel_older_than_6_7", lsm: "lockdown,landlock,apparmor", kernel: "6.5.0-1", line: "landlock warning lockdown,landlock,apparmor 6.5.0-1"},
		{name: "no_lsm_file", kernel: "6.8.0-1015-gcp", line: "landlock warning none 6.8.0-1015-gcp"},
		{name: "a_kernel_with_landlock", lsm: "lockdown,capability,landlock,yama,apparmor", kernel: "6.8.0-1015-gcp", line: "landlock pass"},
		{name: "a_major_version_above_6", lsm: "landlock", kernel: "7.0.1", line: "landlock pass"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			if tc.lsm != "" {
				h.write(filepath.Join(h.sysroot, "sys/kernel/security/lsm"), tc.lsm+"\n")
			}

			out, err := h.setup("FAKE_KERNEL=" + tc.kernel)

			require.NoError(t, err, out)
			assert.Equal(t, done, lastLine(out))
			assert.Contains(t, h.record(), "\n"+tc.line+"\n")
		})
	}
}

// unitEnvironment is the environment that the fake user manager builds now, one NAME=value line each.
func (h *fakeHost) unitEnvironment() string {
	h.t.Helper()

	out, err := h.sh(nil, filepath.Join(h.bin, "fake-unit-env"))
	require.NoError(h.t, err, out)

	return out
}

func TestHostSetup_TheFakeUserManagerReadsTheEnvironmentFilesInNameOrder(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	for _, name := range []string{"99-environment.conf", "990-snapd.conf"} {
		require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.sysroot, "usr/lib/environment.d", name))))
	}

	h.write(filepath.Join(h.sysroot, "etc/environment.d/20-a.conf"), "A=1\nPATH=/one\n")
	h.write(filepath.Join(h.sysroot, "usr/lib/environment.d/30-b.conf"), "PATH=\"/two:${PATH}\"\n")
	h.write(filepath.Join(h.sysroot, "usr/lib/environment.d/40-c.conf"), "C=system\n")
	h.write(filepath.Join(h.home, ".config/environment.d/40-c.conf"), "C=home\n")
	h.write(filepath.Join(h.home, ".config/environment.d/50-d.conf"), "PATH=$PATH:/dollar\n")

	assert.Equal(t, "A=1\nC=home\nPATH=/two:/one:/dollar\n", h.unitEnvironment(),
		"files apply in name order across folders, a home file hides the system file of its name, and both forms of $PATH expand")
}

func TestHostSetup_ThePathSurvivesTheSystemEnvironmentFiles(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup()

	require.NoError(t, err, out)
	assert.Equal(t, done, lastLine(out))
	assert.Contains(t, h.unitEnvironment(), "PATH="+h.sysroot+"/usr/local/go/bin:"+h.home+"/go/bin:"+h.bin+":/usr/bin:/bin:/snap/bin\n",
		"the Go folders come first, and the files of the system still apply")
	assert.Contains(t, h.unitEnvironment(), "BEEHAVE_BD_132="+h.home+"/go/bin/bd\n")
}

func TestHostSetup_FailsWhenALaterFileReplacesThePath(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	h.write(filepath.Join(h.sysroot, "etc/environment.d/zzz-site.conf"), "PATH=/opt/site/bin:/usr/bin:/bin\n")

	out, err := h.setup()

	require.Error(t, err)
	assert.Contains(t, out, "the PATH of the user manager does not start with "+h.sysroot+"/usr/local/go/bin:"+h.home+"/go/bin")
	assert.Contains(t, out, "sorts after zz-beehave.conf")

	for _, folder := range []string{"~/.config/environment.d", "/etc/environment.d", "/run/environment.d", "/usr/local/lib/environment.d", "/usr/lib/environment.d"} {
		assert.Contains(t, out, folder, "the refusal names the folder that environment.d(5) reads for a user manager")
	}

	assert.NotContains(t, out, done)
	assert.NotContains(t, h.read(h.log), "make setup", "the check comes before the installs of the project")
}

func TestHostSetup_FailsWhenALaterFilePrependsToThePath(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	h.write(filepath.Join(h.sysroot, "etc/environment.d/zzz-site.conf"), "PATH=/opt/site/bin:${PATH}\n")

	out, err := h.setup()

	require.Error(t, err)
	assert.Contains(t, out, "the PATH of the user manager does not start with "+h.sysroot+"/usr/local/go/bin:"+h.home+"/go/bin")
	assert.NotContains(t, out, done)
}

func TestHostSetup_ChecksThePathAgainOnASecondRun(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup()
	require.NoError(t, err, out)
	require.Equal(t, done, lastLine(out))

	h.write(filepath.Join(h.sysroot, "etc/environment.d/zzz-site.conf"), "PATH=/opt/site/bin:/usr/bin:/bin\n")

	out, err = h.setup()

	require.Error(t, err)
	assert.Contains(t, out, h.envFile()+" is up to date", "the file is not written again")
	assert.Contains(t, out, "the PATH of the user manager does not start with "+h.sysroot+"/usr/local/go/bin:"+h.home+"/go/bin")
	assert.NotContains(t, out, done)
}

func TestHostSetup_ReadsThePinOfAGoInstalledToolFromItsModuleLine(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup()

	require.NoError(t, err, out)
	assert.Equal(t, done, lastLine(out))

	for _, name := range goInstalledPinned {
		assert.Contains(t, h.record(), "\ntool "+name+" "+goModules[name]+"\n", name)
	}

	assert.Contains(t, h.record(), "\ntool gitleaks github.com/zricethezav/gitleaks/v8 v8.30.1\n", "its own version text says only: version is set by build process")
}

func (h *fakeHost) guardFiles() (rules, unit string) {
	return filepath.Join(h.sysroot, "etc/beehave/metadata-guard.nft"), filepath.Join(h.sysroot, "etc/systemd/system/metadata-guard.service")
}

func TestHostSetup_RestartsTheMetadataGuardOnlyWhenItsFilesOrItsStateDiffer(t *testing.T) {
	t.Parallel()

	const restart = "\nsudo systemctl restart metadata-guard.service\n"

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	rules, unit := h.guardFiles()
	cases := []struct {
		name   string
		change func()
		want   bool
	}{
		{name: "nothing_differs", change: func() {}},
		{name: "the_rules_file_differs", change: func() { h.write(rules, "stale\n") }, want: true},
		{name: "the_unit_file_differs", change: func() { h.write(unit, "stale\n") }, want: true},
		{name: "the_unit_is_inactive", change: func() { require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.state, "guard-active")))) }, want: true},
	}

	for _, tc := range cases {
		h.write(h.log, "")
		tc.change()

		out, err = h.rootSetup()
		require.NoError(t, err, tc.name+": "+out)
		assert.Equal(t, tc.want, strings.Contains(h.read(h.log), restart), tc.name)
		assert.Equal(t, h.readSource("metadata-guard.nft"), h.read(rules), tc.name+": the rules file is the source")
		assert.Equal(t, h.readSource("metadata-guard.service"), h.read(unit), tc.name+": the unit file is the source")
	}
}

const (
	metadataURL4 = "http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token"
	metadataURL6 = "http://[fd20:ce::254]/computeMetadata/v1/instance/service-accounts/default/token"
)

// guardRuleset is the metadata-guard.nft file, read as data: the live lines of the chain and its hook.
type guardRuleset struct {
	tables []string
	chain  string
	hook   string
	rules  []string
}

// tableLines are the three table lines of the nft file, each with the step that it does.
var tableLines = []struct {
	pattern *regexp.Regexp
	step    string
}{
	{regexp.MustCompile(`^table inet (\S+)$`), "make "},
	{regexp.MustCompile(`^delete table inet (\S+)$`), "delete "},
	{regexp.MustCompile(`^table inet (\S+) \{$`), "define "},
}

// tableStep names what a table line of the nft file does: make, delete or define the table. It returns "" for any other line.
func tableStep(fields []string) string {
	line := strings.Join(fields, " ")

	for _, table := range tableLines {
		if match := table.pattern.FindStringSubmatch(line); match != nil {
			return table.step + match[1]
		}
	}

	return ""
}

// parseGuardRuleset reads the nft file. A line that starts with # is a comment and holds no rule. The chain header line gives the hook.
func parseGuardRuleset(text string) guardRuleset {
	var set guardRuleset

	for raw := range strings.SplitSeq(text, "\n") {
		line := strings.TrimSpace(raw)
		fields := strings.Fields(line)

		if step := tableStep(fields); step != "" {
			set.tables = append(set.tables, step)
			continue
		}

		set.readLine(line, fields)
	}

	return set
}

// readLine reads a chain, a hook or a rule line. Any other line, and a comment, changes nothing.
func (set *guardRuleset) readLine(line string, fields []string) {
	switch {
	case len(fields) == 3 && fields[0] == "chain" && fields[2] == "{":
		set.chain = fields[1]
	case strings.HasPrefix(line, "type "):
		set.hook = line
	case strings.HasPrefix(line, "meta skuid"):
		set.rules = append(set.rules, line)
	}
}

func TestMetadataGuard_RulesRejectNonRootTCPOnEveryPortButDNSToTheMetadataAddressesOnTheOutputHook(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	set := parseGuardRuleset(h.readSource("metadata-guard.nft"))

	assert.Equal(t, []string{"make beehave_metadata_guard", "delete beehave_metadata_guard", "define beehave_metadata_guard"}, set.tables, "the file makes the table, deletes it and defines it again in one load")
	assert.Equal(t, "output", set.chain)
	assert.Equal(t, "type filter hook output priority 0; policy accept;", set.hook)
	assert.Equal(t, []string{
		"meta skuid != 0 ip daddr 169.254.169.254 tcp dport != 53 reject with tcp reset",
		"meta skuid != 0 ip6 daddr fd20:ce::254 tcp dport != 53 reject with tcp reset",
	}, set.rules, "one live rule for each address, for every TCP port but 53, so the HTTPS endpoint on 443 is closed and DNS stays open")
}

// guardUnit is the metadata-guard.service file as sections of key and values.
func parseGuardUnit(text string) map[string]map[string][]string {
	unit := map[string]map[string][]string{}
	section := ""

	for raw := range strings.SplitSeq(text, "\n") {
		line := strings.TrimSpace(raw)

		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "["):
			section = strings.Trim(line, "[]")
			unit[section] = map[string][]string{}
		default:
			key, value, _ := strings.Cut(line, "=")
			unit[section][key] = append(unit[section][key], strings.Fields(value)...)
		}
	}

	return unit
}

func TestMetadataGuard_UnitLoadsAfterNftablesAndBeforeTheNetworkAndUserSessions(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	unit := parseGuardUnit(h.readSource("metadata-guard.service"))

	assert.Contains(t, unit["Unit"]["After"], "nftables.service", "the flush of the default ruleset runs first, so it cannot drop the table")
	assert.Contains(t, unit["Unit"]["Wants"], "network-pre.target", "network-pre.target is passive: only a Wants pulls it in")
	assert.Equal(t, []string{"nftables.service"}, unit["Unit"]["PartOf"], "a stop or restart of nftables stops or restarts the guard, so the table is made again")
	assert.Contains(t, unit["Unit"]["Before"], "network-pre.target")
	assert.Contains(t, unit["Unit"]["Before"], "systemd-user-sessions.service")
	assert.Equal(t, []string{"multi-user.target"}, unit["Install"]["WantedBy"], "the rules load at every boot")
	assert.Equal(t, []string{"/usr/sbin/nft", "-f", "/etc/beehave/metadata-guard.nft"}, unit["Service"]["ExecStart"])
	assert.Equal(t, []string{"oneshot"}, unit["Service"]["Type"])
	assert.Equal(t, []string{"yes"}, unit["Service"]["RemainAfterExit"])
	assert.NotContains(t, unit["Service"], "ExecStop", "a stop that deletes the table opens a window with no rule during a restart")
}

func TestMetadataGuard_UnitLoadsTheTableAgainWhenNftablesReloads(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	unit := parseGuardUnit(h.readSource("metadata-guard.service"))

	assert.Equal(t, []string{"nftables.service"}, unit["Unit"]["ReloadPropagatedFrom"], "a reload of nftables runs flush ruleset, so it must reload the guard too")
	assert.Equal(t, unit["Service"]["ExecStart"], unit["Service"]["ExecReload"], "a reload loads the same file as a start, so the table is made again")
	assert.Contains(t, unit["Unit"]["After"], "nftables.service", "the reload of the guard runs after the reload of nftables, so the flush cannot come last")
}
