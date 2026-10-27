package takeover

import (
	_ "embed" // the text of the notice is Markdown files in text/
	"regexp"
	"strings"
)

var (
	//go:embed text/notice.md
	noticeMD string
	//go:embed text/why_idle.md
	whyIdleMD string
	//go:embed text/why_assigned.md
	whyAssignedMD string
)

// block returns the text of a block without its last line break.
func block(text string) string { return strings.TrimSuffix(text, "\n") }

// keepWord and releaseWord match the token when it stands alone as a word. A word letter is any Unicode letter, digit or underscore, so KEEPER, KEEP_ALL and KEEPé do not say KEEP.
var (
	keepWord    = regexp.MustCompile(`(?:^|[^\p{L}\p{N}_])KEEP(?:[^\p{L}\p{N}_]|$)`)
	releaseWord = regexp.MustCompile(`(?:^|[^\p{L}\p{N}_])RELEASE(?:[^\p{L}\p{N}_]|$)`)
)

// says reports whether the text holds the word of the pattern.
func says(word *regexp.Regexp, text string) bool { return word.MatchString(text) }
