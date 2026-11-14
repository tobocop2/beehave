package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/host/slurm"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store/beads"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
)

const hostProjectTail = `repo = "o/n", forge = "github", build_host = "lane", base_branch = "main", gate = "make check", test_report = "r.xml"`

// loadHostProject loads one project file against the registry of the real adapter table.
func loadHostProject(t *testing.T, call string) (config.Project, config.Problems) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "projects", "p.star"), []byte("project("+call+")\n"), 0o600))

	cfg, problems := config.LoadFolder(dir, nil, newAdapters(beadstest.NewBd(t), nil, nil).registry())

	return cfg.Projects["p"], problems
}

// everyKey writes each key that a block declares as a call argument, with a value that the adapter of the block accepts.
func everyKey(declared config.Block, values map[string]string) string {
	var args []string

	for _, arg := range declared.Args {
		if _, given := values[arg.Key]; !given {
			continue
		}

		value := fmt.Sprintf("%q", values[arg.Key])
		if arg.Kind == config.WholeArg {
			value = values[arg.Key]
		}

		args = append(args, arg.Key+" = "+value)
	}

	return strings.Join(args, ", ")
}

func TestHostBlocks_EachHostAdapterBlockRoundTripsEveryKeyItDeclaresAndTheTableBuildsTheHost(t *testing.T) {
	t.Parallel()

	machine := map[string]string{"address": "box.example", "user": "ci", "port": "2222", "identity": "/keys/ci", "work_root": "/home/ci/work", "known_hosts": "accept-new"}

	for name, tc := range map[string]struct {
		provider model.HostProvider
		declared config.Block
		values   map[string]string
		idle     string
	}{
		"skypilot": {skypilot.Name, skypilot.Block(), map[string]string{"cloud": "gcp", "region": "us-central1", "instance_type": "n2-standard-8"}, ", idle_minutes = 30"},
		"ssh":      {ssh.Name, ssh.Block(), machine, ""},
	} {
		keys := everyKey(tc.declared, tc.values)

		project, problems := loadHostProject(t, hostProjectTail+fmt.Sprintf(`, host = host(provider = %q, disk_floor_gb = 50%s, %s = %s(%s))`, tc.provider, tc.idle, tc.declared.Name, tc.declared.Name, keys))

		require.Empty(t, problems, name)
		require.NotNil(t, project.Host.Block, name)

		for _, arg := range tc.declared.Args {
			if arg.Kind == config.WholeArg {
				got, given := project.Host.Block.Whole(arg.Key)
				assert.True(t, given, name+" "+arg.Key)
				assert.Equal(t, 2222, got, name+" "+arg.Key)

				continue
			}

			got, given := project.Host.Block.Text(arg.Key)
			assert.True(t, given, name+" "+arg.Key)
			assert.Equal(t, tc.values[arg.Key], got, name+" "+arg.Key)
		}

		_, err := newAdapters(nil, nil, nil).host(project.Project)

		require.NoError(t, err, name+": the table builds the host from the block the loader held")
	}
}

func TestHostBlocks_TheSlurmBlockRoundTripsItsKeysAndTheHostRefusesTheWorkRootWithAReason(t *testing.T) {
	t.Parallel()

	values := map[string]string{"address": "head.example", "user": "ci", "port": "2222", "identity": "/keys/ci", "work_root": "/home/ci/work", "known_hosts": "accept-new"}

	project, problems := loadHostProject(t, hostProjectTail+fmt.Sprintf(`, host = host(provider = "slurm", disk_floor_gb = 50, slurm = slurm(%s))`, everyKey(slurm.Block(), values)))

	require.Empty(t, problems, "the loader holds every key the block declares")

	for _, arg := range slurm.Block().Args {
		if arg.Kind == config.WholeArg {
			continue
		}

		got, given := project.Host.Block.Text(arg.Key)
		assert.True(t, given, arg.Key)
		assert.Equal(t, values[arg.Key], got, arg.Key)
	}

	_, err := newAdapters(nil, nil, nil).host(project.Project)

	require.ErrorIs(t, err, link.ErrConfig)
	assert.Contains(t, err.Error(), "host.slurm.work_root")

	delete(values, "work_root")

	project, problems = loadHostProject(t, hostProjectTail+fmt.Sprintf(`, host = host(provider = "slurm", disk_floor_gb = 50, slurm = slurm(%s))`, everyKey(slurm.Block(), values)))
	require.Empty(t, problems)

	_, err = newAdapters(nil, nil, nil).host(project.Project)

	require.NoError(t, err)
}

func TestHostBlocks_AKeyTheBlockDoesNotDeclareIsRefused(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ provider, block, words string }{
		"skypilot takes no address": {"skypilot", `skypilot(address = "a")`, `skypilot: unexpected keyword argument "address"`},
		"ssh takes no cloud":        {"ssh", `ssh(cloud = "gcp")`, `ssh: unexpected keyword argument "cloud"`},
		"slurm takes no cloud":      {"slurm", `slurm(cloud = "gcp")`, `slurm: unexpected keyword argument "cloud"`},
	} {
		_, problems := loadHostProject(t, hostProjectTail+fmt.Sprintf(`, host = host(provider = %q, disk_floor_gb = 50, %s = %s)`, tc.provider, tc.provider, tc.block))

		require.Len(t, problems, 1, name)
		assert.Contains(t, problems[0].Error(), tc.words, name)
	}
}

func TestHostBlocks_ABlockOfAHostAdapterTheProjectDidNotChooseIsRefusedNamingTheBlockAndTheChosenAdapter(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ provider, block, held string }{
		"ssh holds a slurm block":     {"ssh", `slurm = slurm()`, "slurm"},
		"slurm holds an ssh block":    {"slurm", `ssh = ssh()`, "ssh"},
		"slurm holds a skypilot":      {"slurm", `skypilot = skypilot()`, "skypilot"},
		"skypilot holds an ssh block": {"skypilot", `ssh = ssh(user = "ci")`, "ssh"},
	} {
		_, problems := loadHostProject(t, hostProjectTail+fmt.Sprintf(`, host = host(provider = %q, disk_floor_gb = 50, %s)`, tc.provider, tc.block))

		require.Len(t, problems, 1, name)
		assert.Contains(t, problems[0].Error(), fmt.Sprintf("the `host` block holds the `%s` block, and `host.provider` %q does not take it", tc.held, tc.provider), name)
	}
}

func TestHostBlocks_AProjectWithABeadsBlockAndAHostBlockHoldsAndReadsBoth(t *testing.T) {
	t.Parallel()

	project, problems := loadHostProject(t, hostProjectTail+`, tracker = "beads", claims = "beads", beads = beads(prefix = "proj"),
  host = host(provider = "ssh", disk_floor_gb = 50, ssh = ssh(user = "ci", port = 2222))`)

	require.Empty(t, problems)

	prefix, given := project.Blocks[beads.BlockName].Text(beads.BlockPrefix)
	require.True(t, given)
	assert.Equal(t, "proj", prefix)

	user, given := project.Host.Block.Text(ssh.BlockUser)
	require.True(t, given)
	assert.Equal(t, "ci", user)

	port, given := project.Host.Block.Whole(ssh.BlockPort)
	require.True(t, given)
	assert.Equal(t, 2222, port)

	_, err := newAdapters(nil, nil, nil).host(project.Project)

	require.NoError(t, err, "the table builds the host from the host block and the tracker from the beads block")

	tracker, err := newAdapters(beadstest.NewBd(t), nil, nil).trackers[beads.Tracker].build(t.Context(), project.Project)

	require.NoError(t, err)
	assert.NotNil(t, tracker)
}

func TestHostBlocks_TheRegistryDeclaresOneBlockForEachHostAdapter(t *testing.T) {
	t.Parallel()

	registered := newAdapters(nil, nil, nil).registry()

	assert.Equal(t, skypilot.Block(), *registered.Hosts[skypilot.Name].Block)
	assert.Equal(t, ssh.Block(), *registered.Hosts[ssh.Name].Block)
	assert.Equal(t, slurm.Block(), *registered.Hosts[slurm.Name].Block)
}
