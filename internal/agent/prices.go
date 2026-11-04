package agent

import (
	"bytes"
	_ "embed" // the price table is a data file in this folder
	"encoding/json"
	"fmt"
)

// tokensPerPrice is the number of tokens that a price in the table covers.
const tokensPerPrice = 1_000_000

// priceData is the price table that ships with beehave. A person updates it by hand. Its rows are estimates, never a bill.
//
//go:embed prices.json
var priceData []byte

// Price is the dollars for one million tokens of one model. CachedInput is the price of an input token that the provider served from its cache;
// 0 means the table has no separate price, and the estimate then charges the input price for those tokens.
type Price struct {
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cached_input"`
	Output      float64 `json:"output"`
}

// Prices maps a model name to its price. It estimates the cost of a run of a CLI that reports tokens and no dollars.
type Prices struct {
	Models map[string]Price `json:"models"`
}

// ParsePrices reads a price table. It refuses an unknown field, a negative price and a row with no output price, so a typo never turns into a wrong estimate.
func ParsePrices(raw []byte) (Prices, error) {
	var prices Prices

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&prices); err != nil {
		return Prices{}, fmt.Errorf("read the price table: %w", err)
	}

	for name, price := range prices.Models {
		if price.Input < 0 || price.CachedInput < 0 || price.Output <= 0 {
			return Prices{}, fmt.Errorf("read the price table: the model %q needs a positive output price and no negative price", name)
		}
	}

	return prices, nil
}

// DefaultPrices returns the price table that ships with beehave. A test reads the file, so it always parses; a table that did not parse would give no estimate.
func DefaultPrices() Prices {
	prices, _ := ParsePrices(priceData) //nolint:errcheck // a test parses the same file

	return prices
}

// Has says whether the table holds a price for the model.
func (p Prices) Has(model string) bool {
	_, known := p.Models[model]

	return known
}

// Estimate returns the dollars that the tokens cost for the model, and false when it has no price for the model or the run used no tokens.
// The estimate never guesses: an unknown model gives no number.
func (p Prices) Estimate(model string, tokens Tokens) (float64, bool) {
	price, known := p.Models[model]
	if !known || tokens == (Tokens{}) {
		return 0, false
	}

	cached := max(tokens.CachedInput, 0)

	cachedPrice := price.CachedInput
	if cachedPrice == 0 {
		cachedPrice = price.Input
	}

	fresh := max(tokens.Input-cached, 0)
	dollars := float64(fresh)*price.Input + float64(cached)*cachedPrice + float64(tokens.Output)*price.Output

	return dollars / tokensPerPrice, true
}
