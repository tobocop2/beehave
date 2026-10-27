package brief_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/brief"
)

func put(t *testing.T, dir, name string, size int) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600))

	return path
}

func size(t *testing.T, name string, opts brief.SizeOptions) brief.Report {
	t.Helper()

	report, err := brief.Size(name, opts)
	require.NoError(t, err)

	return report
}

func TestSize_ListsEachPathOnceWithTokensAndTheTotal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a, b := put(t, dir, "a.md", 400), put(t, dir, "b.md", 800)
	text := "Read " + a + " and `" + b + "` and " + a + " again.\n"
	name := writeFile(t, "brief.md", text)

	report := size(t, name, brief.SizeOptions{})

	assert.Equal(t, "brief-size: "+name, report.Out[0])
	assert.Equal(t, "  tokens     bytes  file", report.Out[2])
	assert.Equal(t, fmt.Sprintf("%8d %9d  %s  (the brief)", len(text)/4, len(text), name), report.Out[3])
	assert.Equal(t, fmt.Sprintf("%8d %9d  %s", 100, 400, a), report.Out[4])
	assert.Equal(t, fmt.Sprintf("%8d %9d  %s", 200, 800, b), report.Out[5])
	assert.Equal(t, fmt.Sprintf("TOTAL: about %d tokens, %d bytes, in 3 files; 0 named paths unsized.", (len(text)+1200)/4, len(text)+1200), report.Out[6])
	assert.Equal(t, "BUDGET: 40000 tokens (the read limit is 160000 bytes). The lane context cap is 150000 tokens, and a fresh lane starts at about 35,000.", report.Out[7])
	assert.Empty(t, report.Warn)
}

func TestSize_TheRoundRootComesFromTheOptionThenTheBriefThenTheFallback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	put(t, dir, "reports/r.md", 40)
	text := "`<S>` is `" + dir + "`. See <S>/reports/r.md now.\n"
	name := writeFile(t, "brief.md", text)

	fromBrief := size(t, name, brief.SizeOptions{Fallback: "/elsewhere"})
	assert.Equal(t, "<S> is "+dir, fromBrief.Out[1])
	assert.Contains(t, fromBrief.Out[4], dir+"/reports/r.md")

	explicit := size(t, name, brief.SizeOptions{Root: "/given/", Fallback: "/elsewhere"})
	assert.Equal(t, "<S> is /given", explicit.Out[1])
	assert.Contains(t, explicit.Out[4], "/given/reports/r.md  (unsized: not a file here)")

	plain := writeFile(t, "plain.md", "See <S>/reports/r.md.\n")

	fallback := size(t, plain, brief.SizeOptions{Fallback: dir})
	assert.Equal(t, "<S> is "+dir, fallback.Out[1])
	assert.Contains(t, fallback.Out[4], dir+"/reports/r.md")

	unknown := size(t, plain, brief.SizeOptions{})
	assert.Equal(t, "<S> is unknown: pass the round folder as the root. A <S>/ path is unsized.", unknown.Out[1])
	assert.Contains(t, unknown.Out[4], "<S>/reports/r.md  (unsized: not a file here)")
	assert.Contains(t, unknown.Out[5], "1 named paths unsized.")
}

func TestSize_ARootStatedAsARelativePathIsUnknown(t *testing.T) {
	t.Parallel()

	name := writeFile(t, "brief.md", "`<S>` is `relative/dir`.\n")

	assert.Equal(t, "<S> is unknown: pass the round folder as the root. A <S>/ path is unsized.", size(t, name, brief.SizeOptions{}).Out[1])
}

func TestSize_AnUnsizedPathIsCountedAndADirectoryIsSkipped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	name := writeFile(t, "brief.md", "Read /no/such/file.md and "+dir+" too.\n")

	report := size(t, name, brief.SizeOptions{})

	assert.Contains(t, strings.Join(report.Out, "\n"), "-         -  /no/such/file.md  (unsized: not a file here)")
	assert.NotContains(t, strings.Join(report.Out, "\n"), dir+"  (unsized")
	assert.Contains(t, strings.Join(report.Out, "\n"), "in 1 files; 1 named paths unsized.")
}

func TestSize_APathStartsAfterABlankAQuoteABracketOrAnEqualsSign(t *testing.T) {
	t.Parallel()

	text := strings.Join([]string{
		"/a/start.md",
		"and/or the url https://host/x/y.md",
		"quote \"/a/quote.md\" tick `/a/tick.md` paren (/a/paren.md) eq =/a/eq.md angle </a/angle.md>",
		"double //a/double.md and trailing /a/end.md. and /a/dir/",
	}, "\n") + "\n"

	report := strings.Join(size(t, writeFile(t, "brief.md", text), brief.SizeOptions{}).Out, "\n")

	for _, path := range []string{"/a/start.md", "/a/quote.md", "/a/tick.md", "/a/paren.md", "/a/eq.md", "/a/angle.md", "/a/end.md", "/a/dir"} {
		assert.Contains(t, report, path+"  (unsized", path)
	}

	assert.NotContains(t, report, "/y.md")
	assert.NotContains(t, report, "/double.md")
	assert.NotContains(t, report, "/or")
}

func TestSize_AChainOfBriefsIsFollowedAndWarned(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	deep := put(t, dir, "deep.md", 40)
	inner := writeFile(t, "inner.md", "Needs "+deep+"\n")
	name := writeFile(t, "brief.md", "Read "+inner+"\n")
	chained := filepath.Join(dir, "briefs", "older.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(chained), 0o750))
	require.NoError(t, os.WriteFile(chained, []byte("Needs "+deep+"\n"), 0o600))
	named := writeFile(t, "top.md", "Read "+chained+"\n")

	report := size(t, named, brief.SizeOptions{})

	assert.Contains(t, strings.Join(report.Out, "\n"), chained+"  (UNDER briefs/, named by top.md; its names are counted below)")
	assert.Contains(t, strings.Join(report.Out, "\n"), deep)
	assert.Equal(t, "WARN: the reading of "+named+" names a file under a briefs/ directory: "+chained, report.Warn[0])
	assert.Contains(t, report.Warn[1], "A replacement brief is self-contained.")
	assert.Len(t, report.Warn, 4)
	assert.Empty(t, size(t, name, brief.SizeOptions{}).Warn)
}

func TestSize_OnlyAFileUnderADirectoryNamedBriefsIsAChainLink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	notes := put(t, dir, "mybriefs/note.md", 10)
	name := writeFile(t, "brief.md", "Read "+notes+"\n")

	assert.Empty(t, size(t, name, brief.SizeOptions{}).Warn)
}

func TestSize_WarnsAboveTheBudgetAndNeverRefuses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "at.md")
	text := "Read " + path + "\n"
	name := writeFile(t, "brief.md", text)

	put(t, dir, "at.md", brief.ReadLimit-len(text))
	assert.Empty(t, size(t, name, brief.SizeOptions{}).Warn)

	put(t, dir, "at.md", brief.ReadLimit-len(text)+1)
	report := size(t, name, brief.SizeOptions{Cap: 100000})

	require.Len(t, report.Warn, 3)
	assert.Contains(t, report.Warn[0], "WARN: the named reading of "+name+" is about ")
	assert.Contains(t, report.Warn[0], "above the budget of")
	assert.Contains(t, report.Warn[1], "40000 tokens (the read limit is 160000 bytes). With a fresh start of about 35,000 tokens, the lane is at")
	assert.Contains(t, report.Warn[2], "of its 100000 tokens before its first step. Cut the list.")
}

func TestSize_ABriefThatIsMissingOrACapThatIsBadIsARefusal(t *testing.T) {
	t.Parallel()

	_, err := brief.Size("/no/brief.md", brief.SizeOptions{})

	require.Error(t, err)
	assert.Equal(t, "no brief at /no/brief.md. Write the brief first, or give the path of the one you wrote.", err.Error())

	_, err = brief.Size(writeFile(t, "b.md", "x"), brief.SizeOptions{Cap: -5})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the lane context cap is -5")
}

func TestSize_AFileThatCannotBeReadIsARefusal(t *testing.T) {
	t.Parallel()

	nul := put(t, t.TempDir(), "nul.md", 0)
	require.NoError(t, os.WriteFile(nul, []byte("a\x00b"), 0o600))

	_, err := brief.Size(writeFile(t, "b.md", "Read "+nul+"\n"), brief.SizeOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), nul+" holds a NUL byte")

	_, err = brief.Size(t.TempDir(), brief.SizeOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no brief at")

	nulBrief := writeFile(t, "nul.md", "a\x00b")
	_, err = brief.Size(nulBrief, brief.SizeOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), nulBrief+" holds a NUL byte")

	_, err = brief.Check(nulBrief, brief.SizeOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), nulBrief+" holds a NUL byte")
}

func TestSize_AFileNamedTwiceByDifferentBriefsCountsOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	shared := put(t, dir, "shared.md", 8)
	inner := put(t, dir, "briefs/inner.md", 0)
	require.NoError(t, os.WriteFile(inner, []byte("Needs "+shared+" and "+inner+"\n"), 0o600))
	name := writeFile(t, "brief.md", "Read "+shared+" and "+inner+"\n")

	assert.Equal(t, 1, strings.Count(strings.Join(size(t, name, brief.SizeOptions{}).Out, "\n"), shared))
}
