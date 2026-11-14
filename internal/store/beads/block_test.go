package beads_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store/beads"
)

// loadBeadsProject loads a project file against a registry that holds the beads block as the beads adapter declares it.
func loadBeadsProject(t *testing.T, args string) (config.Project, config.Problems) {
	t.Helper()

	block := beads.Block()
	entry := func() *config.Entry { return &config.Entry{Block: &block, Tools: []string{beads.Tool}} }
	registered := config.Adapters{
		Trackers: map[model.TrackerName]*config.Entry{"local": {}, beads.Tracker: entry()},
		Claims:   map[model.ClaimsName]*config.Entry{"local": {}, beads.Claims: entry()},
	}

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "projects", "p.star"), []byte("project("+args+")\n"), 0o600))

	cfg, problems := config.LoadFolder(dir, nil, registered)

	return cfg.Projects["p"], problems
}

const beadsTail = `source = "/srv/repo", claims = "beads", base_branch = "main", gate = "make check", test_report = "r.xml"`

func TestBlock_DeclaresTheBeadsNameAndTheKeysItReads(t *testing.T) {
	t.Parallel()

	declared := beads.Block()

	assert.Equal(t, "beads", declared.Name)
	assert.Equal(t, []config.Arg{{Key: "prefix"}}, declared.Args)
}

func TestBlock_EveryKeyTheBlockDeclaresRoundTripsToTheStore(t *testing.T) {
	t.Parallel()

	var args strings.Builder

	for _, arg := range beads.Block().Args {
		require.Equal(t, config.TextArg, arg.Kind, "the beads adapter reads text keys only")

		args.WriteString(arg.Key + ` = "value-of-` + arg.Key + `", `)
	}

	project, problems := loadBeadsProject(t, `beads = beads(`+args.String()+`), `+beadsTail)

	require.Empty(t, problems)

	for _, arg := range beads.Block().Args {
		got, given := project.Blocks[beads.BlockName].Text(arg.Key)

		assert.True(t, given, arg.Key)
		assert.Equal(t, "value-of-"+arg.Key, got)
	}

	_, err := beads.New(nil, project.Blocks[beads.BlockName])

	require.NoError(t, err)
}

func TestBlock_AKeyTheBlockDoesNotDeclareIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadBeadsProject(t, `beads = beads(prefix = "p", zone = "x"), `+beadsTail)

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), `unexpected keyword argument "zone"`)
}
