package workflow

import (
	"errors"
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// The functions of this file own the rules for the arguments of the builtins that land pull requests: merge and preflight.
// The run and the check both call them.

// approvedField is the optional bool field of a member of prs that carries the approval of that member.
const approvedField = "approved"

// ownDraftField is the optional int field of a member of prs: the number of the draft that the run opened, which the merge guard for drafts accepts.
const ownDraftField = "own_draft"

// PullRequest is one member of the prs argument of a merge call. Approved is true when another step accepted the head of this member.
// OwnDraft is the number of the draft that the run opened for this member, or zero when the member names none.
type PullRequest struct {
	Item, Head string
	Approved   bool
	OwnDraft   int
}

// errNoPullRequest is returned for a prs argument with no member.
var errNoPullRequest = errors.New("`prs` is empty. Pass a list of struct(item = ..., head = ...), one for each pull request to land")

// computed stands in for a value that only the run knows: a member or a field that the check cannot read from the file. PullRequests skips it.
type computed struct{}

func (computed) String() string        { return "<computed>" }
func (computed) Type() string          { return "computed" }
func (computed) Freeze()               {}
func (computed) Truth() starlark.Bool  { return starlark.True }
func (computed) Hash() (uint32, error) { return 0, errors.New("a computed value has no hash") }

// PullRequests reads the prs argument of a merge call: a non-empty list of structs with the text fields item and head.
// An error names the member by its place in the list, as prs[1].
func PullRequests(v starlark.Value) ([]PullRequest, error) {
	entries, ok := Elements(v)
	if !ok {
		return nil, errors.New("`prs` must be a list of struct(item = ..., head = ...)")
	}

	if len(entries) == 0 {
		return nil, errNoPullRequest
	}

	return pullRequestMembers(entries)
}

// pullRequestMembers reads each member of the prs list. A computed member has no value to read and is skipped.
func pullRequestMembers(entries []starlark.Value) ([]PullRequest, error) {
	list := make([]PullRequest, 0, len(entries))

	for i, entry := range entries {
		if _, later := entry.(computed); later {
			continue
		}

		pr, err := pullRequest(entry)
		if err != nil {
			return nil, fmt.Errorf("prs[%d]: %w", i, err)
		}

		list = append(list, pr)
	}

	return list, nil
}

// pullRequest reads one entry of the prs argument.
func pullRequest(entry starlark.Value) (PullRequest, error) {
	pr, isStruct := entry.(*starlarkstruct.Struct)
	if !isStruct {
		return PullRequest{}, fmt.Errorf("the member has the type %s, not struct. Use struct(item = ..., head = ...)", entry.Type())
	}

	item, err := prField(pr, "item")
	if err != nil {
		return PullRequest{}, err
	}

	head, err := prField(pr, "head")
	if err != nil {
		return PullRequest{}, err
	}

	member := PullRequest{Item: item, Head: head}
	err = member.readOptional(pr)

	return member, err
}

// readOptional reads the optional fields approved and own_draft of a member struct. The first wrong field is the error.
func (m *PullRequest) readOptional(pr *starlarkstruct.Struct) (err error) {
	if m.Approved, err = prApproved(pr); err != nil {
		return err
	}

	m.OwnDraft, err = prOwnDraft(pr)

	return err
}

// prOwnDraft reads the optional field own_draft of a member struct with the rule of the own_draft keyword of preflight. A member that leaves it out names no draft.
func prOwnDraft(pr *starlarkstruct.Struct) (int, error) {
	fields := starlark.StringDict{}
	pr.ToStringDict(fields)

	value, found := fields[ownDraftField]
	if !found {
		return 0, nil
	}

	if _, later := value.(computed); later {
		return 0, nil
	}

	return OwnDraft(value)
}

// prApproved reads the optional bool field approved of a member struct. A member that leaves it out is not approved.
func prApproved(pr *starlarkstruct.Struct) (bool, error) {
	fields := starlark.StringDict{}
	pr.ToStringDict(fields)

	value, found := fields[approvedField]
	if !found {
		return false, nil
	}

	if _, later := value.(computed); later {
		return false, nil
	}

	// The field holds any Starlark value that the file gave, and only a bool is an approval.
	approved, isBool := value.(starlark.Bool)
	if !isBool {
		return false, fmt.Errorf("`approved` of a member of `prs` must be a bool, not %s. Use approved = True or approved = False", value.Type())
	}

	return bool(approved), nil
}

// prField reads a text field of a member struct.
func prField(pr *starlarkstruct.Struct, name string) (string, error) {
	value, err := pr.Attr(name)
	if err != nil {
		return "", fmt.Errorf("a member of `prs` has no `%s`. Use struct(item = ..., head = ...)", name)
	}

	if _, later := value.(computed); later {
		return "", nil
	}

	text, ok := starlark.AsString(value)
	if !ok || text == "" {
		return "", fmt.Errorf("`%s` of a member of `prs` must be text that is not empty", name)
	}

	return text, nil
}

// OwnerIdentity reads the own dict into its logins and its addresses. An absent dict has none.
func OwnerIdentity(v starlark.Value) (logins, emails []string, err error) {
	dict, ok := Optional[*starlark.Dict](v)
	if !ok {
		return nil, nil, errors.New("`own` must be a dict with `logins` and `emails`. Write {\"logins\": [...], \"emails\": [...]}")
	}

	if dict == nil {
		return nil, nil, nil
	}

	if logins, err = ownerList(dict, "logins"); err != nil {
		return nil, nil, err
	}

	emails, err = ownerList(dict, "emails")

	return logins, emails, err
}

// ownerList reads one list of the own dict. A list that the dict leaves out is empty.
func ownerList(dict *starlark.Dict, key string) ([]string, error) {
	list, found, err := dict.Get(starlark.String(key))
	if !found {
		return nil, err
	}

	return Words("own."+key, list)
}

// HandOffLanding lists the keywords of merge that only a landing reads, in the order that the refusal looks at them.
func HandOffLanding() []string {
	return []string{holdKeyword, "own", "forbid_text", "gate_run", "integration"}
}

// HandOffTakes is the refusal of a keyword of a landing on a merge call with hand_off. The call lands nothing, so the keyword would do nothing.
func HandOffTakes(keyword string) error {
	return fmt.Errorf("merge with `hand_off` lands nothing, so it takes no `%s`. Remove `%s` from the call", keyword, keyword)
}
