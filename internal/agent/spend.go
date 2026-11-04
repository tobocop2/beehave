package agent

// Spend is the cost of one run, or the sum of several runs, together with what is known about it. Every place that shows, records or sums a cost holds
// a Spend, so no place can drop a mark. The JSON keys are the output names of an agent call.
//
// Cost is the dollars. Estimated is true when some of them are an estimate that beehave made from the tokens and the price table, not a cost that the CLI reported.
// Unknown is true when some run used tokens of a model that has no price: Cost then holds no dollars for that run, and is not the cost of it. A run that
// cost nothing has neither mark.
type Spend struct {
	Cost      float64 `json:"cost_usd,omitempty"`
	Estimated bool    `json:"cost_estimated,omitempty"`
	Unknown   bool    `json:"cost_unknown,omitempty"`
}

// Plus is the sum of two spends: the dollars add, and each mark is set when either side sets it.
func (s Spend) Plus(other Spend) Spend {
	return Spend{Cost: s.Cost + other.Cost, Estimated: s.Estimated || other.Estimated, Unknown: s.Unknown || other.Unknown}
}
