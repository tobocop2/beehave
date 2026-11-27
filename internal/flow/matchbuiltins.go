package flow

import (
	"slices"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/closing"
	"github.com/tobocop2/beehest/internal/workflow"
)

// pathsOutside returns the paths that no glob of allow matches, sorted. It reads allow as the allow_paths of an agent call does (the doublestar dialect)
// and refuses the same bad lists, and None as well, so the library function before_review and the scope of an agent judge a path alike. It records no step: it matches text.
func pathsOutside(_ *script, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var allow, paths starlark.Value

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"allow": &allow, "paths": &paths}); err != nil {
		return nil, err
	}

	globs, err := workflow.PathsOutsideAllow(allow)
	if err != nil {
		return nil, err
	}

	names, err := workflow.Words("paths", paths)
	if err != nil {
		return nil, err
	}

	outside := slices.DeleteFunc(slices.Clone(names), func(name string) bool { return workflow.AllowsPath(globs, name) })
	slices.Sort(outside)

	return textList(outside), nil
}

// closingReferences returns the issues that a text closes with a closing keyword, each as "#N" or "owner/repo#N", in the order of the text.
// It reads the grammar of the merge guard, so a commit that the merge guard refuses is a finding before review too. It records no step: it matches text.
func closingReferences(_ *script, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var text string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"text": &text}); err != nil {
		return nil, err
	}

	return textList(closing.References(text)), nil
}

// keepIssuesKey is the name of the builtin keep_issues.
const keepIssuesKey = "keep_issues"

// keepIssues returns the issues of an owner line in the form "keep #N[, owner/repo#N ...]", and an empty list for any other text.
// It reads the form that the merge reads in the recorded owner answers, so the library and the merge agree on what an answer keeps. It records no step: it matches text.
func keepIssues(_ *script, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var text string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{textKeyword: &text}); err != nil {
		return nil, err
	}

	kept := workflow.KeepIssues(text)
	names := make([]string, len(kept))

	for i, issue := range kept {
		names[i] = string(issue)
	}

	return textList(names), nil
}
