package workflow

import (
	"errors"
	"fmt"
	"slices"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/model"
)

// The functions of this file own the rules for the arguments of the builtins that land pull requests: merge and preflight.
// The run and the check both call them.

// approvedField is the optional bool field of a member of prs that carries the approval of that member.
const approvedField = "approved"

// ownDraftField is the optional int field of a member of prs: the number of the draft that the run opened, which the merge guard for drafts accepts.
const ownDraftField = "own_draft"

// closesField is the required list field of a member of prs: the issues that the pull request must close.
const closesField = "closes"

// keptField is the optional list field of a member of prs: the issues that the owner kept, which a closing keyword in a commit message may name.
const keptField = "kept"

// closingField is the optional list field of a member of prs: the closing issues that a check read, and that the merge reads again and refuses on a difference.
const closingField = "closing"

// PullRequest is one member of the prs argument of a merge call. Approved is true when another step accepted the head of this member.
// OwnDraft is the number of the draft that the run opened for this member, or zero when the member names none.
// Closes holds the issues that the pull request must close, and nothing else: an empty list means it closes no issue.
// Kept holds the issues that the owner kept. Closing is nil when the member pins no set, and a list (empty for no issue) when it does.
// The fields are those of a batch member in the same order, so the merge converts one into the other and a field that only one of them has does not compile.
type PullRequest struct {
	Item, Head string
	Approved   bool
	OwnDraft   int
	Closes     []int
	Kept       []model.IssueRef
	Closing    []model.IssueRef
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

	return pullRequestMembers("prs", entries)
}

// Members reads the members argument of a merge call: a non-empty list of member structs in landing order, with no item twice.
// An error names the member by its place in the list, as members[1].
func Members(v starlark.Value) ([]PullRequest, error) {
	entries, ok := Elements(v)
	if !ok {
		return nil, errors.New("`members` must be a list of struct(item = ..., head = ..., closes = [...]), in the order to land them")
	}

	if len(entries) == 0 {
		return nil, errors.New("`members` is empty. Pass the members of the gated union, one struct(item = ..., head = ..., closes = [...]) for each")
	}

	list, err := pullRequestMembers("members", entries)
	if err != nil {
		return nil, err
	}

	if err := onceEach(list); err != nil {
		return nil, err
	}

	return list, nil
}

// onceEach refuses a union that names one item twice. A member whose item only the run knows is not compared.
func onceEach(list []PullRequest) error {
	for i, m := range list {
		if m.Item != "" && slices.ContainsFunc(list[:i], func(o PullRequest) bool { return o.Item == m.Item }) {
			return fmt.Errorf("`members` names item %s twice. Name each member of the union once", m.Item)
		}
	}

	return nil
}

// pullRequestMembers reads each member of a list keyword. A computed member has no value to read and is skipped.
func pullRequestMembers(keyword string, entries []starlark.Value) ([]PullRequest, error) {
	list := make([]PullRequest, 0, len(entries))

	for i, entry := range entries {
		if _, later := entry.(computed); later {
			continue
		}

		pr, err := pullRequest(entry)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", keyword, i, err)
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

	if m.Closes, err = prCloses(pr); err != nil {
		return err
	}

	if m.Kept, err = prIssues(pr, keptField); err != nil {
		return err
	}

	m.Closing, err = prIssues(pr, closingField)

	return err
}

// prIssues reads an optional field of a member struct that lists issues as text: "#12" or "owner/repo#12". A member that leaves the field out, or gives a value that only the run knows, has none (nil).
// A field that is present gives a list that is never nil, so an empty closing list is a set of no issue and not a missing set.
func prIssues(pr *starlarkstruct.Struct, field string) ([]model.IssueRef, error) {
	fields := starlark.StringDict{}
	pr.ToStringDict(fields)

	value, found := fields[field]
	if !found {
		return nil, nil
	}

	if _, later := value.(computed); later {
		return nil, nil
	}

	list, isList := value.(*starlark.List)
	if !isList {
		return nil, fmt.Errorf("`%s` of a member of `prs` must be a list of issues as text, such as [\"#12\", \"owner/repo#5\"], not %s", field, value.Type())
	}

	return issueList(field, list)
}

// issueList reads each element of a list field of a member as an issue as text. An element that only the run knows is left out.
func issueList(field string, list *starlark.List) ([]model.IssueRef, error) {
	issues := make([]model.IssueRef, 0, list.Len())

	for i := range list.Len() {
		if _, later := list.Index(i).(computed); later {
			continue
		}

		issue, err := issueText(field, list.Index(i))
		if err != nil {
			return nil, err
		}

		issues = append(issues, issue)
	}

	return issues, nil
}

// issueText reads one issue as text of a list field of a member.
func issueText(field string, value starlark.Value) (model.IssueRef, error) {
	text, isText := value.(starlark.String)
	issue, valid := model.ParseIssue(text.GoString())

	if !isText || !valid {
		return "", fmt.Errorf("`%s` of a member of `prs` holds %s, which is not an issue as text. Write \"#12\" or \"owner/repo#5\"", field, value)
	}

	return issue, nil
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
