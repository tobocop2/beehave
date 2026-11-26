package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ErrNullAnswer is the cause when a command prints null where a value is due.
var ErrNullAnswer = errors.New("the answer is null")

const nullPayload = "null"

// DecodeError is an answer of a command that does not match the expected shape. Cause stays reachable with errors.Is.
type DecodeError struct {
	Command string
	Cause   error
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("cannot read the answer of `%s` (%v)", e.Command, e.Cause)
}

// Unwrap returns the cause, such as a JSON syntax error.
func (e *DecodeError) Unwrap() error {
	return e.Cause
}

// Decode reads one JSON payload from a command into T. A payload of another shape, a null payload or text after the value is a *DecodeError that names the command.
func Decode[T any](command, text string) (T, error) { //nolint:ireturn // the decoder returns the caller's own type
	var out T

	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return out, &DecodeError{Command: command, Cause: err}
	}

	if strings.TrimSpace(text) == nullPayload {
		return out, &DecodeError{Command: command, Cause: ErrNullAnswer}
	}

	return out, nil
}

var errNullInPages = errors.New("a page or an item is null")

// DecodePages reads the output of `gh api --paginate --slurp`, a list of pages, as one list. A null page or a null item is an answer of the wrong shape, not an empty one.
func DecodePages[T any](command, text string) ([]T, error) {
	pages, err := Decode[[]*[]*T](command, text)
	if err != nil {
		return nil, err
	}

	items := []T{}

	for _, page := range pages {
		if page == nil || slices.Contains(*page, nil) {
			return nil, &DecodeError{Command: command, Cause: errNullInPages}
		}

		for _, item := range *page {
			items = append(items, *item)
		}
	}

	return items, nil
}

var errNoGraphList = errors.New("a page holds no list")

// graphPage is one page of a paged GraphQL list, aliased list, under the repository or at the top of the answer.
type graphPage[T any] struct {
	Data *struct {
		Repository *struct {
			List *graphList[T] `json:"list"`
		} `json:"repository"`
		List *graphList[T] `json:"list"`
	} `json:"data"`
}

// graphList is the total of a GraphQL list and the rows of one page.
type graphList[T any] struct {
	TotalCount int `json:"totalCount"`
	Nodes      []T `json:"nodes"`
}

// list is the list of the page, or nil when the page holds none.
func (p graphPage[T]) list() *graphList[T] {
	switch {
	case p.Data == nil:
		return nil
	case p.Data.Repository != nil:
		return p.Data.Repository.List
	}

	return p.Data.List
}

// DecodeGraphPages reads the output of `gh api graphql --paginate --slurp` for a query whose one paged list is aliased list, as one list.
// A page with no list is an answer of the wrong shape. Fewer rows than the total of the last page is a cut list, a *DecodeError that wraps ErrMorePages.
func DecodeGraphPages[T any](command, text string) ([]T, error) {
	pages, err := Decode[[]graphPage[T]](command, text)
	if err != nil {
		return nil, err
	}

	rows, total, ok := joinPages(pages)
	if !ok {
		return nil, &DecodeError{Command: command, Cause: errNoGraphList}
	}

	if len(rows) < total {
		return nil, &DecodeError{Command: command, Cause: fmt.Errorf("%d of %d rows, so the rest are unknown: %w", len(rows), total, ErrMorePages)}
	}

	return rows, nil
}

// joinPages joins the rows of the pages and returns the total of the last page. It is false when a page holds no list.
func joinPages[T any](pages []graphPage[T]) ([]T, int, bool) {
	rows, total := []T{}, 0

	for _, page := range pages {
		list := page.list()
		if list == nil {
			return nil, 0, false
		}

		rows, total = append(rows, list.Nodes...), list.TotalCount
	}

	return rows, total, true
}

// Deref returns the text, or empty for nil.
func Deref(text *string) string {
	if text == nil {
		return ""
	}

	return *text
}

// SplitRepo splits a repository written owner/name. Any other spelling is not ok.
func SplitRepo(repo string) (owner, name string, ok bool) {
	owner, name, found := strings.Cut(repo, "/")

	return owner, name, found && IsRepoPart(owner) && IsRepoPart(name)
}

// URLNumber returns the number at the end of an address such as https://github.com/o/r/<kind>/<number>, which gh prints after it creates a pull request or an issue.
func URLNumber(text, kind string) (int, bool) {
	text = strings.TrimSpace(text)
	cut := strings.LastIndex(text, "/")

	if cut < 0 || !strings.HasSuffix(text[:cut], "/"+kind) || !IsDigits(text[cut+1:]) {
		return 0, false
	}

	number, err := strconv.Atoi(text[cut+1:])

	if err != nil || number <= 0 {
		return 0, false
	}

	return number, true
}
