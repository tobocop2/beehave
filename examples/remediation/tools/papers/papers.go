package papers

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/tobocop2/obee/examples/remediation/tools/check"
	"github.com/tobocop2/obee/internal/model"
)

const papersIndent = "         "

var revisionPair = regexp.MustCompile(`(?s)^.+\.\..+$`)

// runner runs git in the repository under check. The package declares its own git runner.
type runner interface {
	Run(ctx context.Context, args []string) model.CommandResult
}

// SourcePaths lists the source paths a revision range changes. The first line starts with REFUSED when it finds one.
func SourcePaths(ctx context.Context, git runner, revisionRange, sourceGlobs string) ([]string, error) {
	if !revisionPair.MatchString(revisionRange) || strings.HasPrefix(revisionRange, "-") {
		return nil, &check.Refusal{Message: "usage: remedy papers <base>..<head>"}
	}

	result := git.Run(ctx, []string{"diff", "--name-only", revisionRange})
	if result.ExitCode != 0 {
		return nil, &check.Refusal{Message: fmt.Sprintf("git cannot read the range %s.", revisionRange)}
	}

	hits := sourceHits(result.Stdout, sourceGlobs)
	if len(hits) == 0 {
		return []string{fmt.Sprintf("OK: %s changes no source path.", revisionRange)}, nil
	}

	return refusedLines(revisionRange, hits, sourceGlobs), nil
}

func sourceHits(names, sourceGlobs string) []string {
	var (
		hits     []string
		compiled [][]globToken
	)

	for _, pattern := range pyFields(sourceGlobs) {
		compiled = append(compiled, compileGlob(pattern))
	}

	for _, name := range strings.Split(names, newline) {
		if name != "" && isSource(name, sourceGlobs == "", compiled) {
			hits = append(hits, name)
		}
	}

	return hits
}

func refusedLines(revisionRange string, hits []string, sourceGlobs string) []string {
	patterns := sourceGlobs
	if patterns == "" {
		patterns = "<every path except *.md and *.txt>"
	}

	return []string{
		fmt.Sprintf("REFUSED: %s changes %d source path(s). This lane is proof and papers only:\n%s", revisionRange, len(hits), papersIndent+strings.Join(hits, "\n"+papersIndent)),
		papersIndent + "A change to source is a fix lane, with its acceptance script green. Patterns: " + patterns,
	}
}

// isSource reports whether the path is source under the globs; no globs means every path but a document.
func isSource(name string, noGlobs bool, patterns [][]globToken) bool {
	if noGlobs {
		return !strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".txt")
	}

	for _, pattern := range patterns {
		if globMatches(pattern, name) {
			return true
		}
	}

	return false
}
