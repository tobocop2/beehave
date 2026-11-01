package shell_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/check/shell"
	"github.com/tobocop2/obee/internal/model"
)

// wrapperForms reads the command shapes of testdata/wrapper_forms.txt. `@` stands for the command and `Q` for the command in single quotes.
func wrapperForms(t *testing.T) []string {
	t.Helper()

	data, err := os.ReadFile("testdata/wrapper_forms.txt")
	require.NoError(t, err)

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func fillForm(form, payload string) string {
	form = strings.ReplaceAll(form, "@", payload)

	return strings.ReplaceAll(form, "Q", "'"+payload+"'")
}

func TestEveryWrapperFormRefusesABulkStageAndALocalDelete(t *testing.T) {
	t.Parallel()

	for _, form := range wrapperForms(t) {
		if !strings.ContainsAny(form, "@Q") {
			continue // a form with no place for a command
		}

		for _, payload := range []string{"git add -A", "rm f.bak"} {
			command := fillForm(form, payload)
			assert.NotEmpty(t, shell.Scan(command), command)
		}
	}
}

func TestEveryWrapperFormAllowsAClean(t *testing.T) {
	t.Parallel()

	for _, form := range wrapperForms(t) {
		for _, payload := range []string{"ls", "git status"} {
			command := fillForm(form, payload)
			assert.Empty(t, shell.Scan(command), command)
		}
	}
}

func TestNestedWrappersAreScannedInLinearTime(t *testing.T) {
	t.Parallel()

	command := strings.Repeat("flock /tmp/l ", 60) + "git add -A"
	assert.Len(t, shell.Scan(command), 1)
}

// worstShapes are the forms whose scan cost grew fastest before the limit: each is a head and the words that repeat behind it.
var worstShapes = []struct {
	name  string
	head  string
	words []string
}{
	{"spaced words", "flock /l", []string{"'a b'"}},
	{"glued su", "su", []string{"-cx"}},
	{"glued wrappers", "su", []string{"-cwatch", "-cflock", "-csu", "-cdoas"}},
	{"glued wrappers behind doas", "doas", []string{"-cwatch", "-cdoas", "-cflock", "-cchroot"}},
	{"glued mix", "su", []string{"-cflock", "-cx", "'a b'", "-csu"}},
}

// behindWrapper builds a command with exactly n words behind the head.
func behindWrapper(head string, words []string, n int) string {
	parts := []string{head}
	for i := range n {
		parts = append(parts, words[i%len(words)])
	}

	return strings.Join(parts, " ")
}

// BenchmarkScanAtTheLimit measures the worst shapes at the word limit: `go test -bench ScanAtTheLimit ./internal/check/shell`.
// The limit is a word count, so the bound holds by construction; the benchmark shows what it costs.
func BenchmarkScanAtTheLimit(b *testing.B) {
	for _, shape := range worstShapes {
		command := behindWrapper(shape.head, shape.words, shell.MaxWordsBehind-len(strings.Fields(shape.head))+1)

		b.Run(shape.name, func(b *testing.B) {
			for b.Loop() {
				shell.Scan(command)
			}
		})
	}
}

func TestACommandPastTheLimitBehindAWrapperIsOpaque(t *testing.T) {
	t.Parallel()

	for _, shape := range worstShapes {
		command := behindWrapper(shape.head, shape.words, shell.MaxWordsBehind+1)
		assert.Contains(t, shell.Scan(command), model.Finding{Kind: model.FindingOpaque, Text: strings.Fields(shape.head)[0]}, shape.name)
	}

	fill := strings.Repeat("x ", shell.MaxWordsBehind)
	assert.NotEmpty(t, shell.Scan("doas "+fill+"ls"))
	assert.Empty(t, shell.Scan("doas "+strings.Repeat("x ", shell.MaxWordsBehind-2)+"ls"))
}

func TestAWordWithManyLettersIsPastTheLineLimit(t *testing.T) {
	t.Parallel()

	command := "su -" + strings.Repeat("c", shell.MaxLinesBehind+2) + "x y"
	assert.Contains(t, shell.Scan(command), model.Finding{Kind: model.FindingOpaque, Text: "su"})

	assert.Empty(t, shell.Scan("su -"+strings.Repeat("c", shell.MaxLinesBehind/2)+"x y"))
}

func TestAWrapperFormReportsEachFindingOnce(t *testing.T) {
	t.Parallel()

	assert.Len(t, shell.Scan("flock /tmp/l sudo flock /tmp/m git add -A"), 1)
	assert.Len(t, shell.Scan("watch -n 2 'git add -A'"), 1)
}
