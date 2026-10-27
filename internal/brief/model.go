package brief

import "strings"

// Model is the model of a lane and the tier row that lets a strong model run.
type Model struct {
	Name string
	Tier Tier
}

// ParseModel returns the model that a dispatch names for lane. A model is chosen, never inherited.
// A model with opus or fable in its name needs a tier row, written as `name:row`.
func ParseModel(lane, model string) (Model, error) {
	if inherited(model) {
		return Model{}, refuse("no model for lane %s. Name the model of the lane: the least powerful model that can do the job, "+
			"and the mid tier at the least for a reviewer. A dispatch with no model inherits the orchestrator's.", lane)
	}

	if !needsTier(model) {
		return Model{Name: model, Tier: TierNone}, nil
	}

	name, row, _ := strings.Cut(model, ":")

	tier, err := ParseTier(row)
	if err != nil {
		return Model{}, refuse("lane %s asks for %s with no tier row. Use sonnet, or %s:<row> where <row> is one of: "+
			"subtle (a concurrency, migration or security change), union (the union gate and the integration review), "+
			"stuck (a fix lane at round 4 or later, or the design lane after two regressions or two patch rounds on one mechanism).",
			lane, name, name)
	}

	return Model{Name: name, Tier: tier}, nil
}

// inherited reports whether a dispatch names no model: it is blank, inherits or holds a placeholder or a space.
func inherited(model string) bool {
	switch model {
	case "", "inherit", "default":
		return true
	}

	return strings.ContainsAny(model[:1], "[<") || strings.ContainsAny(model, blanks)
}

// needsTier reports whether the model name holds a word that needs a tier row: opus or fable.
func needsTier(model string) bool {
	return strings.Contains(model, "opus") || strings.Contains(model, "fable")
}
