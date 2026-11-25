package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// forgeBase is a project with a forge and a test report and no gate, so a test names the gate.
const forgeBase = `repo = "o/n", tracker = "github", forge = "github", base_branch = "main", test_report = "r.xml"`

// loadedProgram loads the project with the arguments and returns its gate program.
func loadedProgram(t *testing.T, args string) []string {
	t.Helper()

	project, problems := loadProjectText(t, call(args))
	require.Empty(t, problems, args)

	return project.GateProgram
}

func secretScanNames() []string { return []string{".gitleaks.toml", ".gitleaksignore"} }

func TestGateProgramDefault_MakeNames(t *testing.T) {
	t.Parallel()

	for _, gate := range []string{`gate = "make check"`, `gate = "gmake check"`, `gate = "/usr/bin/make -j4 check"`, `gate_steps = {"lint": "go vet ./...", "all": "make all"}`} {
		program := loadedProgram(t, forgeBase+", "+gate)

		require.Subset(t, program, []string{"GNUmakefile", "makefile", "Makefile"}, gate)
		require.NotContains(t, program, ".golangci.yml", gate)
	}
}

func TestGateProgramDefault_GolangciNames(t *testing.T) {
	t.Parallel()

	names := []string{
		".golangci.json", ".golangci.toml", ".golangci.yaml", ".golangci.yml", ".golangci.properties", ".golangci.props",
		".golangci.prop", ".golangci.hcl", ".golangci.tfvars", ".golangci.dotenv", ".golangci.env", ".golangci.ini",
	}

	for _, gate := range []string{`gate = "go vet ./... && golangci-lint run"`, `gate = "true; GOFLAGS=-mod=mod golangci-lint run | tee lint.txt"`, `gate = "go test ./...", test_command = "golangci-lint run"`} {
		program := loadedProgram(t, forgeBase+", "+gate)

		require.Subset(t, program, names, gate)
		require.Len(t, program, len(names)+len(secretScanNames()), gate)
	}
}

func TestGateProgramDefault_GitleaksNames(t *testing.T) {
	t.Parallel()

	for _, args := range []string{forgeBase + `, gate = "make check"`, forgeBase + `, gate = "go test ./..."`, localProject} {
		require.Subset(t, loadedProgram(t, args), secretScanNames(), args)
	}
}

func TestGateProgramDefault_UnknownProgram(t *testing.T) {
	t.Parallel()

	for _, gate := range []string{`gate = "go test ./..."`, `gate = "$MAKE check"`, `gate = "make ("`, `gate = "./make.sh check"`} {
		require.Equal(t, secretScanNames(), loadedProgram(t, forgeBase+", "+gate), gate)
	}
}

func TestGateProgramDefault_OwnerListAdds(t *testing.T) {
	t.Parallel()

	program := loadedProgram(t, forgeBase+`, gate = "make check", gate_program = ["scripts/", "Makefile", ".golangci.yml"]`)

	require.Equal(t, []string{".gitleaks.toml", ".gitleaksignore", ".golangci.yml", "GNUmakefile", "Makefile", "makefile", "scripts/"}, program)
}

func TestGateProgramDefault_NoBase(t *testing.T) {
	t.Parallel()

	require.Empty(t, loadedProgram(t, `gate = "make check", test_report = "r.xml"`))
	require.Empty(t, loadedProgram(t, ``))
}
