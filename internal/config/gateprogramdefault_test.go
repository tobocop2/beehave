package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
)

const (
	// forgeBase is a project with a forge and a test report and no gate, so a test names the gate.
	forgeBase = `repo = "o/n", tracker = "github", forge = "github", base_branch = "main", test_report = "r.xml"`
	// ownerList is a gate_program of the owner that holds no default name.
	ownerList = `gate_program = ["scripts/"]`
)

// loadedProgram loads the project with the arguments and returns its gate program.
func loadedProgram(t *testing.T, args string) []string {
	t.Helper()

	project, problems := loadProjectText(t, call(args))
	require.Empty(t, problems, args)

	return project.GateProgram
}

func secretScanNames() []string { return []string{".gitleaks.json", ".gitleaks.toml", ".gitleaksignore"} }

func makeNames() []string { return []string{"GNUmakefile", "makefile", "Makefile"} }

func golangciNames() []string {
	return []string{
		".golangci.json", ".golangci.toml", ".golangci.yaml", ".golangci.yml", ".golangci.properties", ".golangci.props",
		".golangci.prop", ".golangci.hcl", ".golangci.tfvars", ".golangci.dotenv", ".golangci.env", ".golangci.ini",
	}
}

func TestGateProgramDefault_MakeNames(t *testing.T) {
	t.Parallel()

	for _, gate := range []string{`gate = "make check"`, `gate = "gmake check"`, `gate = "/usr/bin/make -j4 check"`, `gate_steps = {"lint": "go vet ./...", "all": "make all"}`} {
		program := loadedProgram(t, forgeBase+", "+ownerList+", "+gate)

		require.Subset(t, program, makeNames(), gate)
		require.NotContains(t, program, ".golangci.yml", gate)
	}
}

func TestGateProgramDefault_GolangciNames(t *testing.T) {
	t.Parallel()

	for _, gate := range []string{`gate = "go vet ./... && golangci-lint run"`, `gate = "go test ./...", test_command = "golangci-lint run"`} {
		program := loadedProgram(t, forgeBase+", "+ownerList+", "+gate)

		require.Subset(t, program, golangciNames(), gate)
		require.Len(t, program, len(golangciNames())+len(secretScanNames())+1, gate)
	}
}

func TestGateProgramDefault_GitleaksNames(t *testing.T) {
	t.Parallel()

	for _, args := range []string{forgeBase + `, gate = "make check"`, forgeBase + `, gate = "go test ./..."`, localProject} {
		require.Subset(t, loadedProgram(t, args+", "+ownerList), secretScanNames(), args)
	}
}

func TestGateProgramDefault_UnknownProgram(t *testing.T) {
	t.Parallel()

	for _, gate := range []string{`gate = "go test ./..."`, `gate = "./make.sh check"`, `gate = "X=1"`} {
		require.Equal(t, append(secretScanNames(), "scripts/"), loadedProgram(t, forgeBase+", "+ownerList+", "+gate), gate)
	}
}

func TestGateProgramDefault_UnreadableCommandAddsEveryName(t *testing.T) {
	t.Parallel()

	for _, gate := range []string{`gate = "$MAKE check"`, `gate = "make ("`, `gate = "go test ./...", test_command = "$(which lint) run"`} {
		program := loadedProgram(t, forgeBase+", "+ownerList+", "+gate)

		require.Subset(t, program, makeNames(), gate)
		require.Subset(t, program, golangciNames(), gate)
	}
}

func TestGateProgramDefault_NoReaderAddsEveryName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "projects", "p.star"), call(forgeBase+", "+ownerList+`, gate = "go test ./..."`))

	adapters := registry()
	adapters.Programs = nil

	cfg, problems := config.LoadFolder(dir, nil, adapters)

	require.Empty(t, problems)
	require.Subset(t, cfg.Projects["p"].GateProgram, append(makeNames(), golangciNames()...))
}

func TestGateProgramDefault_OwnerListAdds(t *testing.T) {
	t.Parallel()

	program := loadedProgram(t, forgeBase+`, gate = "make check", gate_program = ["scripts/", "Makefile", ".golangci.yml"]`)

	require.Equal(t, []string{".gitleaks.json", ".gitleaks.toml", ".gitleaksignore", ".golangci.yml", "GNUmakefile", "Makefile", "makefile", "scripts/"}, program)
}

func TestGateProgramDefault_ADefaultNameThatTestPathsMatchesIsRefusedAndNamesBoth(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(forgeBase+`, test_paths = ["*.yml"], gate = "golangci-lint run", gate_program = ["tools/gate.cfg"]`))

	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), `".golangci.yml"`)
	require.Contains(t, problems[0].Error(), `"*.yml"`)

	require.NotEmpty(t, loadedProgram(t, forgeBase+`, test_paths = ["*.yml"], gate = "make check", gate_program = ["tools/gate.cfg"]`), "no default name of a make gate ends in .yml")
}

func TestGateProgramDefault_AnOwnerFolderThatNamesADefaultFileIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(forgeBase+`, gate = "make check", gate_program = ["Makefile/"]`))

	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), `"Makefile"`)
}

func TestGateProgramDefault_EmptyOwnerListGivesNoDefault(t *testing.T) {
	t.Parallel()

	for _, args := range []string{forgeBase + `, gate = "make check"`, forgeBase + `, gate = "golangci-lint run"`, localProject} {
		require.Empty(t, loadedProgram(t, args), args)
		require.Empty(t, loadedProgram(t, args+", gate_program = []"), args)
	}
}

func TestGateProgramDefault_NoBase(t *testing.T) {
	t.Parallel()

	require.Empty(t, loadedProgram(t, `gate = "make check", test_report = "r.xml"`))
	require.Empty(t, loadedProgram(t, ``))
}
