package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/check/shell"
)

func TestPrograms_ReadsTheProgramPastWrappersQuotesAndShellStrings(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"make check", "/usr/bin/make -j4 check", "timeout 600 make check", "env FOO=1 make check", "nice -n 10 make check", "exec make check",
		"command make check", "sh -c 'make check'", `"make" check`, "'make' check", `\make check`, "FOO=1 make check", "go vet ./... && make check",
		"true; make check | tee out.txt", "sudo -u ci timeout -s KILL 60 make check", "bash -c \"timeout 9 make all\"",
	} {
		programs, readable := shell.Programs(text)

		assert.True(t, readable, text)
		assert.Contains(t, programs, "make", text)
	}
}

func TestPrograms_ACommandWithNoMakeNamesNoMake(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"go test ./...", "./make.sh check", "echo make", "golangci-lint run"} {
		programs, readable := shell.Programs(text)

		assert.True(t, readable, text)
		assert.NotContains(t, programs, "make", text)
	}
}

func TestPrograms_AnAssignmentOnlyCommandNamesNoProgram(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"X=1", "X=1 Y=2", ""} {
		programs, readable := shell.Programs(text)

		assert.True(t, readable, text)
		assert.Empty(t, programs, text)
	}
}

func TestPrograms_ACommandWordNoStaticReadCanSeeIsUnreadable(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"$MAKE check", "${MAKE} check", "$(which make) check", "`which make` check", "make (", "timeout 60 $TOOL run"} {
		_, readable := shell.Programs(text)

		assert.False(t, readable, text)
	}
}
