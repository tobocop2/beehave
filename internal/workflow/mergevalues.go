package workflow

import (
	"errors"
	"fmt"
	"slices"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// The functions of this file own the rules for the arguments of the builtins that land pull requests: merge and preflight.
// The run and the check both call them.

// approvedField is the optional bool field of a member of prs that carries the approval of that member.
const approvedField = "approved"

// ownDraftField is the optional int field of a member of prs: the number of the draft that the run opened, which the merge guard for drafts accepts.
const ownDraftField = "own_draft"

// closesField is the required list field of a member of prs: the issues that the pull request must close.
const closesField = "closes"

// PullRequest is one member of the prs argument of a merge call. Approved is true when another step accepted the head of this member.
// OwnDraft is the number of the draft that the run opened for this member, or zero when the member names none.
// Closes holds the issues that the pull request must close, and nothing else: an empty list means it closes no issue.
type PullRequest struct {
	Item, Head string
	Approved   bool
	OwnDraft   int
	Closes     []int
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
	err = member.readFields(pr)

	return member, err
}

// readFields reads the fields approved, own_draft and closes of a member struct. The first wrong field is the error.
func (m *PullRequest) readFields(pr *starlarkstruct.Struct) (err error) {
	if m.Approved, err = prApproved(pr); err != nil {
		return err
	}

	if m.OwnDraft, err = prOwnDraft(pr); err != nil {
		return err
	}

	m.Closes, err = prCloses(pr)

	return err
}

// prCloses reads the required list field closes of a member struct: issue numbers of 1 or more, each once. A value that only the run knows is left to the run.
func prCloses(pr *starlarkstruct.Struct) ([]int, error) {
	value, err := pr.Attr(closesField)
	if err != nil {
		return nil, fmt.Errorf("a member of `prs` has no `%s`. Write closes = [12] for the issues the pull request closes, or closes = [] for none", closesField)
	}

	if _, later := value.(computed); later {
		return nil, nil
	}

	list, isList := value.(*starlark.List)
	if !isList {
		return nil, errCloses
	}

	return closesNumbers(list)
}

// closesNumbers reads the issue numbers of a closes list. A number that only the run knows is skipped.
func closesNumbers(list *starlark.List) ([]int, error) {
	var numbers []int

	for i := range list.Len() {
		if _, later := list.Index(i).(computed); later {
			continue
		}

		number, err := starlark.AsInt32(list.Index(i))
		if err != nil || number < 1 || slices.Contains(numbers, int(number)) {
			return nil, errCloses
		}

		numbers = append(numbers, int(number))
	}

	return numbers, nil
}

// errCloses is the refusal for a closes field that is not a list of different issue numbers.
var errCloses = errors.New("`closes` of a member of `prs` must be a list of issue numbers, each 1 or more and none twice. Write closes = [12], or closes = [] for none")

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

// integrationKeyword is the keyword of merge that names the integration pull request.
const integrationKeyword = "integration"

// MergeKeywords says which keywords a merge call names. The run reads it from the arguments and the check reads it from the file, so MergeForm owns the rule.
type MergeKeywords struct {
	Prs, Member, GateRun, Integration, HandOff, Gate, GatedTree, ExpectTree bool
}

// MergeForm refuses a merge call whose keywords do not make one form. A call with `prs` lands a list with a gate of its own. A call with `member` lands one pull request
// and takes the gate it was given. A keyword of the other form would do nothing, so it is refused.
func MergeForm(k MergeKeywords) error {
	switch {
	case k.Prs && k.Member:
		return errors.New("merge takes `prs` or `member`, not both. Use `prs` for a list of pull requests and `member` for one")
	case !k.Prs && !k.Member:
		return errors.New("merge needs `prs` or `member`. Pass `member = struct(item = ..., head = ..., closes = [])` for one pull request")
	case k.Member:
		return memberForm(k)
	}

	for _, named := range []struct {
		keyword string
		given   bool
	}{{"gate", k.Gate}, {"gated_tree", k.GatedTree}, {"expect_tree", k.ExpectTree}} {
		if named.given {
			return fmt.Errorf("merge with `prs` runs its own gate, so it takes no `%s`. Remove `%s` from the call, or merge one pull request with `member`", named.keyword, named.keyword)
		}
	}

	return nil
}

// memberForm refuses the keywords of a list in a call with `member`, and a gate with no tree or a tree with no gate.
func memberForm(k MergeKeywords) error {
	for _, named := range []struct {
		keyword string
		given   bool
	}{{"gate_run", k.GateRun}, {integrationKeyword, k.Integration}, {"hand_off", k.HandOff}} {
		if named.given {
			return fmt.Errorf("merge with `member` lands one pull request, so it takes no `%s`. Remove `%s` from the call", named.keyword, named.keyword)
		}
	}

	switch {
	case k.Gate && !k.GatedTree:
		return errors.New("merge with `gate` needs `gated_tree`, the tree that the gate ran on. Pass gated_tree = gate_result.tree")
	case k.GatedTree && !k.Gate:
		return errors.New("merge with `gated_tree` needs `gate`, the result of the gate call. Pass gate = gate_result")
	}

	return nil
}

// Member reads the `member` argument of a merge call: one struct with the fields of a member of `prs`.
func Member(v starlark.Value) (PullRequest, error) {
	pr, err := pullRequest(v)
	if err != nil {
		return PullRequest{}, fmt.Errorf("member: %w", err)
	}

	return pr, nil
}

// HandOffLanding lists the keywords of merge that only a landing reads, in the order that the refusal looks at them.
func HandOffLanding() []string {
	return []string{holdKeyword, "own", "forbid_text", "gate_run", integrationKeyword}
}

// HandOffTakes is the refusal of a keyword of a landing on a merge call with hand_off. The call lands nothing, so the keyword would do nothing.
func HandOffTakes(keyword string) error {
	return fmt.Errorf("merge with `hand_off` lands nothing, so it takes no `%s`. Remove `%s` from the call", keyword, keyword)
}
