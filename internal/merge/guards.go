package merge

import (
	"fmt"
	"strconv"
	"strings"
)

// settings refuses a merge with no repository or base.
func (a *attempt) settings() *refusal {
	for _, key := range []struct{ name, value string }{
		{"repository", a.req.Repo}, {"base branch", a.req.Base},
	} {
		if key.value == "" {
			return refuse("REFUSE: the " + key.name + " of the merge is not set.")
		}
	}

	if a.req.IntegrationPR != 0 && a.req.Dir == "" {
		return refuse("REFUSE: the state folder is not set, so the row of the merge cannot be locked.")
	}

	return nil
}

// pin refuses a gated commit id that is not the full 40 characters.
func (a *attempt) pin() *refusal {
	if len(a.req.Gated) != shaLength {
		return refuse(fmt.Sprintf("REFUSE: gated SHA must be the full 40 characters, got '%s'", a.req.Gated))
	}

	return nil
}

// state shows the first read and refuses a pull request that is not open, not on the base, not on the gated head or not mergeable.
func (a *attempt) state() *refusal {
	view := a.view
	if view.IsDraft == nil {
		return refuse(fmt.Sprintf("REFUSE: cannot read #%d for its state", a.req.PR))
	}

	state, base, head, mergeable := text(view.State), text(view.BaseRefName), text(view.HeadRefOid), text(view.Mergeable)
	a.say("before: " + tsvLine(state, strconv.FormatBool(*view.IsDraft), base, head, mergeable))

	switch {
	case state != "OPEN":
		return refuse("REFUSE: state is " + state)
	case base != a.req.Base:
		return refuse(fmt.Sprintf("REFUSE: base is %s, expected %s", base, a.req.Base))
	case head != a.req.Gated:
		return refuse(fmt.Sprintf("REFUSE: head %s is not the gated %s. Re-gate.", head, a.req.Gated))
	case mergeable != "MERGEABLE":
		return refuse("REFUSE: mergeable is " + mergeable)
	}

	return nil
}

// tsvLine joins values with tabs. A tab, a newline or a backslash inside a value is escaped, so no value splits the line.
func tsvLine(values ...string) string {
	escape := strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\n", "\\n", "\r", "\\r")
	cells := make([]string, len(values))

	for i, value := range values {
		cells[i] = escape.Replace(value)
	}

	return strings.Join(cells, "\t")
}

// draft refuses a draft unless the owner allowed readying it. A draft skips its checks, so the checks guard judged nothing.
func (a *attempt) draft() *refusal {
	if !*a.view.IsDraft || a.req.AllowReady {
		return nil
	}

	return refuse(
		fmt.Sprintf("REFUSE: #%d is a draft. Merging it means marking it ready first, which is a separate decision.", a.req.PR),
		"        Pass --allow-ready if readying it is what you mean.",
		"        Its checks are skipped while it is a draft, so the checks guard judged nothing.",
		"        Ready it, let CI run, then run the merge again.",
	)
}
