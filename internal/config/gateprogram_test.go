package config_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// refusedProgram loads the project with the gate_program line and returns the one refusal text.
func refusedProgram(t *testing.T, line string) string {
	t.Helper()

	_, problems := loadProjectText(t, call(goodProject+", "+line))

	require.Len(t, problems, 1, line)

	return problems[0].Error()
}

func TestLoadFolder_AValidGateProgramIsLoadedAndAnUnsetOrEmptyOneIsEmpty(t *testing.T) {
	t.Parallel()

	defaults := []string{".gitleaks.json", ".gitleaks.toml", ".gitleaksignore", "GNUmakefile", "Makefile", "makefile"}

	project, problems := loadProjectText(t, call(goodProject+`, gate_program = ["Makefile", "GNUmakefile", "scripts/", "tools/gate.cfg"]`))
	require.Empty(t, problems)
	require.Equal(t, append(slices.Clone(defaults), "scripts/", "tools/gate.cfg"), project.GateProgram)

	project, problems = loadProjectText(t, call(goodProject))
	require.Empty(t, problems)
	require.Empty(t, project.GateProgram)

	project, problems = loadProjectText(t, call(goodProject+`, gate_program = []`))
	require.Empty(t, problems)
	require.Empty(t, project.GateProgram)
}

func TestLoadFolder_AGateProgramEntryThatIsEmptyIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	require.Contains(t, refusedProgram(t, `gate_program = ["Makefile", " "]`), "`gate_program` holds an empty entry")
}

func TestLoadFolder_AGateProgramEntryThatStartsWithAColonIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	message := refusedProgram(t, `gate_program = [":(glob)Makefile"]`)

	require.Contains(t, message, `":(glob)Makefile"`)
	require.Contains(t, message, "starts with a colon")
}

func TestLoadFolder_AGateProgramEntryWithAGlobCharacterIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{"scripts/*.sh", "make?ile", "a[b]", "a{b,c}", `a\\b`} {
		message := refusedProgram(t, `gate_program = ["`+entry+`"]`)

		require.Contains(t, message, "holds a glob character", entry)
	}
}

func TestLoadFolder_AGateProgramEntryThatIsAbsoluteOrLeavesTheWorkTreeIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{"/etc/Makefile", "../Makefile", "a/../../b", "scripts/.."} {
		message := refusedProgram(t, `gate_program = ["`+entry+`"]`)

		require.Contains(t, message, `"`+entry+`"`)
		require.Contains(t, message, "absolute or leaves the work tree", entry)
	}
}

func TestLoadFolder_AGateProgramEntryThatNamesTheRootIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{".", "./"} {
		require.Contains(t, refusedProgram(t, `gate_program = ["`+entry+`"]`), "names the whole repository", entry)
	}
}

func TestLoadFolder_AGateProgramEntryInTheGitFolderIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{".git", ".git/", ".git/hooks/", "a/.git/x"} {
		message := refusedProgram(t, `gate_program = ["`+entry+`"]`)

		require.Contains(t, message, `"`+entry+`"`)
		require.Contains(t, message, "`.git` component", entry)
	}
}

func TestLoadFolder_AGateProgramEntryWithAGitComponentInAnyCaseIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{".GIT/hooks", ".Git", "a/.gIT/"} {
		message := refusedProgram(t, `gate_program = ["`+entry+`"]`)

		require.Contains(t, message, `"`+entry+`"`)
		require.Contains(t, message, "`.git` component", entry)
	}

	project, problems := loadProjectText(t, call(goodProject+`, gate_program = [".gitignore", ".github/", "git/"]`))

	require.Empty(t, problems, "a name that only starts with .git is no git folder")
	require.Subset(t, project.GateProgram, []string{".gitignore", ".github/", "git/"})
}

func TestLoadFolder_AGateProgramEntryWithASpaceIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{"Makefile ", " Makefile", "scripts/ ", "my file"} {
		message := refusedProgram(t, `gate_program = ["`+entry+`"]`)

		require.Contains(t, message, `"`+entry+`"`)
		require.Contains(t, message, "outside A-Z", entry)
	}
}

func TestLoadFolder_AGateProgramEntryWithAControlCharacterIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{`a\n`, `a\t`, `a\x00b`} {
		message := refusedProgram(t, `gate_program = ["`+entry+`"]`)

		require.Contains(t, message, "outside A-Z", entry)
	}
}

func TestLoadFolder_AGateProgramEntryWithANonASCIICharacterIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	const (
		cyrillicA     = rune(0x430)
		divisionSlash = rune(0x2215)
		zeroWidth     = rune(0x200b)
		acuteE        = rune(0xe9)
	)

	for _, entry := range []string{"M" + string(cyrillicA) + "kefile", "a" + string(divisionSlash) + "b", "a" + string(zeroWidth) + "b", "caf" + string(acuteE)} {
		message := refusedProgram(t, `gate_program = ["`+entry+`"]`)

		require.Contains(t, message, "outside A-Z", entry)
		require.Contains(t, message, "non-ASCII", entry)
	}
}

func TestLoadFolder_AGateProgramEntryWithEveryPortableCharacterIsLoaded(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject+`, gate_program = ["Ab_c-9.d/E-f.sh", "x/"]`))

	require.Empty(t, problems)
	require.Subset(t, project.GateProgram, []string{"Ab_c-9.d/E-f.sh", "x/"})
}

func TestLoadFolder_AGateProgramEntryThatIsNotAPlainPathIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{"./Makefile", "a//b", "scripts//", "a/./b"} {
		require.Contains(t, refusedProgram(t, `gate_program = ["`+entry+`"]`), "is not a plain path", entry)
	}
}

func TestLoadFolder_AGateProgramThatNamesAPathTwiceIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	message := refusedProgram(t, `gate_program = ["scripts/", "Makefile", "scripts"]`)

	require.Contains(t, message, `names "scripts" twice`)
	require.Contains(t, message, `"scripts/"`)
}

func TestLoadFolder_AGateProgramEntryInsideADirectoryEntryIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	inner := refusedProgram(t, `gate_program = ["scripts/", "scripts/check.sh"]`)
	require.Contains(t, inner, `entry "scripts/check.sh" is inside entry "scripts/"`)

	outer := refusedProgram(t, `gate_program = ["a/b/c.sh", "a/"]`)
	require.Contains(t, outer, `entry "a/b/c.sh" is inside entry "a/"`)
}

func TestLoadFolder_AGateProgramEntryInsideAnEntryWithNoTrailingSlashIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	for _, line := range []struct{ list, inner, outer string }{
		{`"scripts", "scripts/x"`, "scripts/x", "scripts"},
		{`"scripts/x", "scripts"`, "scripts/x", "scripts"},
		{`"a", "a/b"`, "a/b", "a"},
		{`"a/b/", "a"`, "a/b/", "a"},
		{`"a", "a/b/c/"`, "a/b/c/", "a"},
	} {
		message := refusedProgram(t, `gate_program = [`+line.list+`]`)

		require.Contains(t, message, `entry "`+line.inner+`" is inside entry "`+line.outer+`"`, line.list)
	}
}

func TestLoadFolder_AGateProgramNextToADirectoryOfALongerNameIsNotInsideIt(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject+`, gate_program = ["scripts/", "scripts-extra/", "scripts.sh"]`))

	require.Empty(t, problems)
	require.Subset(t, project.GateProgram, []string{"scripts/", "scripts-extra/", "scripts.sh"})
}

func TestLoadFolder_AGateProgramThatOverlapsATestPathsGlobIsRefusedAndNamesBoth(t *testing.T) {
	t.Parallel()

	message := refusedProgram(t, `test_paths = ["**/*_test.go"], gate_program = ["scripts/"]`)
	require.Contains(t, message, `entry "scripts/" overlaps `+"`test_paths`"+` entry "**/*_test.go"`)

	message = refusedProgram(t, `test_paths = ["tests/**"], gate_program = ["tests/helpers/"]`)
	require.Contains(t, message, `entry "tests/helpers/" overlaps`)

	message = refusedProgram(t, `test_paths = ["scripts/gate/x_test.go"], gate_program = ["scripts/"]`)
	require.Contains(t, message, `entry "scripts/" overlaps`)

	message = refusedProgram(t, `test_paths = ["tests/**"], gate_program = ["Makefile", "tests/"]`)
	require.Contains(t, message, `entry "tests/" overlaps`)

	message = refusedProgram(t, `test_paths = ["**/*_test.go"], gate_program = ["tools/check_test.go"]`)
	require.Contains(t, message, `entry "tools/check_test.go" overlaps`)

	message = refusedProgram(t, `test_paths = ["docs/**/*.md"], gate_program = ["docs/"]`)
	require.Contains(t, message, `entry "docs/" overlaps`)
}

func TestLoadFolder_AGateProgramThatNoTestPathsGlobCanMatchIsLoaded(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject+`, test_paths = ["tests/**", "pkg/*_test.go"], gate_program = ["Makefile", "scripts/", "tests-gate/", "pkg/gate/check.sh"]`))

	require.Empty(t, problems)
	require.Subset(t, project.GateProgram, []string{"Makefile", "scripts/", "tests-gate/", "pkg/gate/check.sh"})
}

func TestLoadFolder_AGateProgramNeedsARepository(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(`gate_program = ["Makefile"]`))

	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "`gate_program` is set, and the project has no `source` and no `forge`")

	_, problems = loadProjectText(t, call(`gate_program = []`))

	require.Empty(t, problems, "an empty list is today's behavior, so it needs no repository")
}
