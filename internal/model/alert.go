package model

import "fmt"

// Alert is one open security alert of a pull request, as the forge reports it. Severity is the word of the forge.
type Alert struct {
	Number   int
	Rule     string
	Severity string
	URL      string
}

// String names the alert by its number, its rule and its severity.
func (a Alert) String() string {
	return fmt.Sprintf("#%d %s (%s) %s", a.Number, a.Rule, a.Severity, a.URL)
}
