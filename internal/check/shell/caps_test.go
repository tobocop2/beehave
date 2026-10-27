package shell_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/check/shell"
)

// The caps are the size of a variable value (4096 bytes) and the number of values one lookup works out (1000).
const (
	sizeCap       = 4096
	resolutionCap = 1000
)

// padded returns the text, a space and x characters, so that the whole is exactly size bytes.
func padded(text string, size int) string {
	return text + " " + strings.Repeat("x", size-len(text)-1)
}

// refChain sets n variables, each naming the next, the last one holding the text. It runs the first one, so n values are worked out.
func refChain(n int, text string) string {
	var script strings.Builder

	fmt.Fprintf(&script, "V%d='%s'\n", n-1, text)

	for i := n - 2; i >= 0; i-- {
		fmt.Fprintf(&script, "V%d='$V%d'\n", i, i+1)
	}

	script.WriteString("$V0\n")

	return script.String()
}

func capCases() []shellCase {
	const (
		remoteDelete = `ssh h rm -rf "$D"`
		staging      = `sudo git add -A`
	)

	pastCap := "W='" + padded(remoteDelete, sizeCap+1) + "'; "
	pastCapStaging := "W='" + padded(staging, sizeCap+1) + "'; "

	return []shellCase{
		{"an unpadded variable after sudo is a remote delete", "hit", "remote-delete", `W='ssh h rm -rf "$D"'; sudo $W`},
		{"a variable past the size cap after sudo is a hit", "hit", "opaque-command", pastCap + "sudo $W"},
		{"a variable past the size cap after env is a hit", "hit", "opaque-command", pastCap + "env $W"},
		{"a variable past the size cap after nice is a hit", "hit", "opaque-command", pastCap + "nice $W"},
		{"a variable past the size cap after nice with an option is a hit", "hit", "opaque-command", pastCap + "nice -n 5 $W"},
		{"a variable past the size cap after timeout is a hit", "hit", "opaque-command", pastCap + "timeout 5 $W"},
		{"a variable past the size cap after xargs is a hit", "hit", "opaque-command", pastCap + "xargs $W"},
		{"a variable past the size cap after a pipe and sudo is a hit", "hit", "opaque-command", pastCap + "echo a | sudo $W"},
		{"a variable past the size cap after ssh and sudo is a hit", "hit", "opaque-command", pastCap + "ssh box sudo $W"},
		{"a variable past the size cap after sudo in a shell string is a hit", "hit", "opaque-command", pastCap + "bash -c 'sudo $W'"},
		{"a padded staging after sudo is a hit", "hit", "opaque-command", pastCapStaging + "sudo $W"},
		{"a variable past the size cap as an argument of a plain command is clean", "clean", "", pastCap + "echo $W"},
		{"a variable that names itself is not unreadable", "nokind", "opaque-command", "W='ssh h $W'; $W"},
		{"a wrapper value past the size cap is a hit", "hit", "opaque-command", "W='" + padded(remoteDelete, sizeCap+1) + "'; $W"},
		{"a wrapper value at the size cap is read", "hit", "remote-delete", "W='" + padded(remoteDelete, sizeCap) + "'; $W"},
		{"a wrapper value at the size cap is not unreadable", "nokind", "opaque-command", "W='" + padded(remoteDelete, sizeCap) + "'; $W"},
		{"a padded staging past the size cap is a hit", "hit", "opaque-command", "W='" + padded(staging, sizeCap+1) + "'; $W"},
		{"a padded staging at the size cap is a hit", "hit", "bulk-staging", "W='" + padded(staging, sizeCap) + "'; $W"},
		{"a padded value past the size cap with arguments is a hit", "hit", "opaque-command", "W='" + padded("ssh h", sizeCap+1) + "'; $W rm -rf \"$D\""},
		{"a harmless variable past the size cap is a hit as a command word", "hit", "opaque-command", "W='" + padded("sudo echo", sizeCap+1) + "'; $W"},
		{"a harmless variable past the size cap is clean as an argument", "clean", "", "W='" + padded("sudo echo", sizeCap+1) + "'; echo $W"},
		{"a chain of values at the resolution cap is read", "hit", "remote-delete", refChain(resolutionCap, `ssh h rm -rf "$D"`)},
		{"a chain of values past the resolution cap is a hit", "hit", "opaque-command", refChain(resolutionCap+1, `ssh h rm -rf "$D"`)},
		{"a chain of values past the resolution cap hides no staging", "hit", "opaque-command", refChain(resolutionCap+1, "sudo git add -A")},
		{"a quoted string in a wrapper value stays one word", "hit", "remote-delete", `W='ssh h "rm -rf /x"'; $W`},
		{"a quoted variable in a wrapper value is a remote delete", "hit", "remote-delete", `W='ssh h rm -rf "$D"'; $W`},
		{"a wrapper value that is not one command splits at white space", "hit", "remote-delete", `W='ssh h; rm -rf "$D"'; $W`},
	}
}

func TestScanCapCases(t *testing.T) {
	t.Parallel()

	for _, tc := range capCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings := shell.Scan(tc.command)

			switch tc.expect {
			case expectClean:
				assert.Empty(t, findings, tc.name)
			case expectHit:
				assert.True(t, hasKind(findings, tc.kind), "%s: %v", tc.name, findings)
			case expectNoKind:
				assert.False(t, hasKind(findings, tc.kind), "%s: %v", tc.name, findings)
			}
		})
	}
}
