package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
)

const blockTail = `base_branch = "main", gate = "make check", test_report = "r.xml"`

// blockRegistry returns the tests registry with the given blocks on the tracker "shelf" and the claim store "ledger".
func blockRegistry(shelf, ledger *config.Block) config.Adapters {
	registered := registry()
	registered.Trackers["shelf"] = &config.Entry{Block: shelf}
	registered.Claims["ledger"] = &config.Entry{Block: ledger}

	return registered
}

func loadWith(t *testing.T, registered config.Adapters, text string) (config.Project, config.Problems) {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, dir+"/projects/p.star", text)
	cfg, problems := config.LoadFolder(dir, nil, registered)

	return cfg.Projects["p"], problems
}

func TestLoadFolder_ABlockOfAnAdapterTheProjectDidNotChooseIsRefusedAndNamesTheChosenAdapter(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(`source = "/srv/repo", beads = beads(prefix = "p"), `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "p.star:1:8: the project holds the `beads` block")
	assert.Contains(t, problems[0].Error(), "tracker `local`, claims `local`")

	_, problems = loadProjectText(t, call(`repo = "o/n", tracker = "github", forge = "github", beads = beads(prefix = "p"), `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "tracker `github`, claims `local`, forge `github`")
}

func TestLoadFolder_TheBlockOfTheChosenAdapterLoadsOnTheProject(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(`source = "/srv/repo", claims = "beads", beads = beads(prefix = "p"), `+blockTail))

	require.Empty(t, problems)

	prefix, given := project.Blocks["beads"].Text("prefix")
	assert.True(t, given)
	assert.Equal(t, "p", prefix)

	project, problems = loadProjectText(t, call(`source = "/srv/repo", `+blockTail))

	require.Empty(t, problems)
	assert.Nil(t, project.Blocks, "a project of adapters that declare no block holds none")
}

func TestLoadFolder_ABlockWhoseArgumentsAllHaveDefaultsMayBeLeftOut(t *testing.T) {
	t.Parallel()

	optional := &config.Block{Name: "shelf", Args: []config.Arg{{Key: "zone", Optional: true}, {Key: "slots", Kind: config.WholeArg, Optional: true}}}
	registered := blockRegistry(optional, nil)

	project, problems := loadWith(t, registered, call(`source = "/srv/repo", tracker = "shelf", `+blockTail))

	require.Empty(t, problems)
	assert.Nil(t, project.Blocks)

	project, problems = loadWith(t, registered, call(`source = "/srv/repo", tracker = "shelf", shelf = shelf(zone = "a", slots = 3), `+blockTail))

	require.Empty(t, problems)

	zone, _ := project.Blocks["shelf"].Text("zone")
	slots, given := project.Blocks["shelf"].Whole("slots")

	assert.Equal(t, "a", zone)
	assert.True(t, given)
	assert.Equal(t, 3, slots)

	_, problems = loadWith(t, registered, call(`source = "/srv/repo", tracker = "shelf", shelf = shelf(slots = "three"), `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "want a whole number")

	_, problems = loadWith(t, registered, call(`source = "/srv/repo", tracker = "shelf", shelf = shelf(slots = 99999999999999999999), `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "does not fit a whole number")
}

func TestLoadFolder_TwoAdaptersThatDeclareOneBlockNameLoadWhenTheSchemasAgreeAndAreRefusedWhenTheyDiffer(t *testing.T) {
	t.Parallel()

	one := &config.Block{Name: "shared", Args: []config.Arg{{Key: "id"}}}
	same := &config.Block{Name: "shared", Args: []config.Arg{{Key: "id"}}}

	project, problems := loadWith(t, blockRegistry(one, same), call(`source = "/srv/repo", tracker = "shelf", claims = "ledger", shared = shared(id = "x"), `+blockTail))

	require.Empty(t, problems)

	id, _ := project.Blocks["shared"].Text("id")
	assert.Equal(t, "x", id)

	other := &config.Block{Name: "shared", Args: []config.Arg{{Key: "id", Kind: config.WholeArg}}}

	_, problems = loadWith(t, blockRegistry(one, other), call(`source = "/srv/repo", `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "two adapters declare the `shared` block with different arguments")

	renamed := &config.Block{Name: "shared", Args: []config.Arg{{Key: "name"}}}

	_, problems = loadWith(t, blockRegistry(one, renamed), call(`source = "/srv/repo", `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "different arguments")
}

func TestLoadFolder_TwoHostAdaptersThatDeclareOneNameMustDeclareOneSchema(t *testing.T) {
	t.Parallel()

	registered := registry()
	registered.Hosts["ssh"] = &config.Entry{Block: &config.Block{Name: "shared", Args: []config.Arg{{Key: "id"}}}}
	registered.Hosts["slurm"] = &config.Entry{Block: &config.Block{Name: "shared", Args: []config.Arg{{Key: "id", Kind: config.WholeArg}}}}

	_, problems := loadWith(t, registered, call(`source = "/srv/repo", `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "two adapters declare the `shared` block with different arguments")
}

func TestLoadFolder_ABlockNoAdapterDeclaresIsRefused(t *testing.T) {
	t.Parallel()

	bare := registry()
	bare.Trackers["beads"], bare.Claims["beads"] = &config.Entry{}, &config.Entry{}

	_, problems := loadWith(t, bare, call(`source = "/srv/repo", claims = "beads", beads = beads(prefix = "p"), `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "p.star:1:57: undefined: beads. Fix the syntax of the file.")
}

func TestLoadFolder_AnAdapterWithARequiredBlockArgumentRefusesAProjectThatLeavesTheBlockOut(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(`source = "/srv/repo", tracker = "beads", `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "p.star:1:8: the tracker `beads` needs the `beads` block. Write beads(prefix = ...).")
}

func TestLoadFolder_ABlockCallRefusesAnArgumentItsAdapterDoesNotDeclare(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(`source = "/srv/repo", claims = "beads", beads = beads(prefix = "p", zone = "x"), `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "zone")

	_, problems = loadProjectText(t, call(`source = "/srv/repo", claims = "beads", beads = beads(), `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "prefix")

	_, problems = loadProjectText(t, call(`source = "/srv/repo", claims = "beads", beads = beads(prefix = 5), `+blockTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "want string")
}

func TestLoadFolder_ABlockCallOutsideTheProjectCallIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, "beads(prefix = \"p\")\n"+call(`source = "/srv/repo", `+blockTail))

	require.NotEmpty(t, problems)
	assert.Contains(t, problems[0].Error(), "beads")
}

func TestLoadFolder_AProjectValueHoldsTheBlockOfItsChosenAdapter(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(`source = "/srv/repo", tracker = "beads", beads = beads(prefix = "p"), `+blockTail))

	require.Empty(t, problems)
	assert.Equal(t, `{prefix="p"}`, project.Blocks["beads"].String())
	assert.Contains(t, project.Blocks["beads"].At(), "p.star:1:", "the block keeps the position of its call")
}
