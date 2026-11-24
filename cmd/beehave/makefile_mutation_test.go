package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/tobocop2/beehave/internal/exectest"
)

// makefile returns the absolute path of the Makefile of the repository.
func makefile(t *testing.T) string {
	t.Helper()

	path, err := filepath.Abs(filepath.Join("..", "..", "Makefile"))
	require.NoError(t, err)

	return path
}

func TestMakefile_SetupInstallsGremlinsAtThePinnedVersion(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on the path")
	}

	out, err := exec.CommandContext(t.Context(), "make", "-n", "-C", filepath.Dir(makefile(t)), "setup").CombinedOutput() //nolint:gosec // G204: the folder is the root of this repository
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0\n")
}

func TestMakefile_EveryPhonyNameIsATargetAndTheVersionChecksAreAmongThem(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(makefile(t))
	require.NoError(t, err)

	var phony []string

	continued := false

	for line := range strings.SplitSeq(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, ".PHONY:"); ok {
			line, continued = rest, true
		} else if !continued {
			continue
		}

		continued = strings.HasSuffix(line, "\\")
		for _, name := range strings.Fields(strings.TrimSuffix(line, "\\")) {
			if !strings.Contains(name, "$") {
				phony = append(phony, name)
			}
		}
	}

	for _, name := range phony {
		assert.Regexp(t, "(?m)^"+regexp.QuoteMeta(name)+":", string(raw), "the .PHONY name %q is no target: two names may be joined", name)
	}

	assert.Contains(t, phony, "lint-version")
	assert.Contains(t, phony, "ai-rulez-version")
}

func TestMakefile_MutationLeavesTheWorkerCountToTheGremlinsConfig(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on the path")
	}

	fake := t.TempDir()
	args := filepath.Join(fake, "args")
	require.NoError(t, exectest.Write(filepath.Join(fake, "gremlins"), "#!/bin/sh\necho \"$*\" > "+args+"\n"))

	command := exec.CommandContext(t.Context(), "make", "-s", "-C", filepath.Dir(makefile(t)), "mutation", "PKG=./internal/cli") //nolint:gosec // G204: the arguments are constants of this test
	command.Env = []string{"PATH=" + fake + ":/usr/bin:/bin:/usr/sbin:/sbin"}

	out, err := command.CombinedOutput()
	require.NoError(t, err, string(out))

	got, err := os.ReadFile(args) //nolint:gosec // G304: the path is in the temporary folder of this test
	require.NoError(t, err)
	assert.Equal(t, "unleash ./internal/cli\n", string(got), "the Makefile must leave the worker count to .gremlins.yaml")
}

func TestGremlinsConfig_SetsFourWorkers(t *testing.T) {
	t.Parallel()

	config, err := os.Open(filepath.Join("..", "..", ".gremlins.yaml"))
	require.NoError(t, err)

	defer func() { require.NoError(t, config.Close()) }()

	// Gremlins drops the whole file with no message when it cannot read it, so a line that is not YAML or a repeated key must fail here.
	var parsed struct {
		Unleash struct {
			Workers int `yaml:"workers"`
		} `yaml:"unleash"`
	}

	decoder := yaml.NewDecoder(config)
	decoder.KnownFields(true)
	require.NoError(t, decoder.Decode(&parsed))
	assert.Equal(t, 4, parsed.Unleash.Workers)
}

func TestMakefile_MutationRefusesAPackagePatternWithDots(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on the path")
	}

	fake := t.TempDir()
	ran := filepath.Join(fake, "ran")
	require.NoError(t, exectest.Write(filepath.Join(fake, "gremlins"), "#!/bin/sh\ntouch "+ran+"\n"))

	command := exec.CommandContext(t.Context(), "make", "-s", "-C", t.TempDir(), "-f", makefile(t), "mutation", "PKG=./internal/...") //nolint:gosec // G204: the arguments are constants of this test and its temporary folders
	// The path holds the fake gremlins first, then the system folders for sh and for the core count that the Makefile reads (sysctl is in /usr/sbin on macOS).
	command.Env = []string{"PATH=" + fake + ":/usr/bin:/bin:/usr/sbin:/sbin"}

	out, err := command.CombinedOutput()
	require.Error(t, err, string(out))
	assert.Contains(t, string(out), `PKG=./internal/... holds "...": gremlins reads it as a folder name and mutates nothing.`)
	assert.NoFileExists(t, ran, "gremlins must not start")
}
