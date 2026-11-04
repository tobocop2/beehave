package flow

import (
	"fmt"
	"regexp"

	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/workflow"
)

// forbidKeyword is the keyword that holds the RE2 patterns that the public text of a step may not match.
const forbidKeyword = "forbid_text"

// textRules holds the compiled forbid_text patterns of a step that posts text to a forge or a tracker.
type textRules []*regexp.Regexp

// unpackPosting unpacks the arguments of a posting step into `into`, with forbid_text added, and returns the compiled patterns.
func unpackPosting(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, into map[string]any) (textRules, error) {
	var forbid starlark.Value

	into[forbidKeyword] = &forbid

	if err := workflow.Unpack(b.Name(), args, kwargs, into); err != nil {
		return nil, err
	}

	return readForbidden(forbid)
}

// readForbidden reads the forbid_text argument of a posting step. An absent argument forbids nothing.
func readForbidden(v starlark.Value) (textRules, error) {
	patterns, err := workflow.Words(forbidKeyword, v)
	if err != nil {
		return nil, err
	}

	return merge.Compile(patterns)
}

// postedText is one piece of text that a step posts, with the name of its argument.
type postedText struct{ name, text string }

// refuse returns an error, before the post, for the first piece of text that a pattern matches. The error names the pattern and the argument, never the text.
func (f textRules) refuse(posted ...postedText) error {
	for _, one := range posted {
		if rule, found := merge.ForbiddenBy(f, one.text); found {
			return fmt.Errorf("%s: a line of the %s matches the forbidden pattern %q. Remove that text and post again", forbidKeyword, one.name, rule.String())
		}
	}

	return nil
}
