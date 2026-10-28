package model

import (
	"errors"
	"fmt"
)

// ClaimsName names the claim store that a project selects.
type ClaimsName string

// The claim stores.
const (
	ClaimsLocal ClaimsName = "local"
	ClaimsBeads ClaimsName = "beads"
)

// ErrUnknownClaims is returned when text names no claim store.
var ErrUnknownClaims = errors.New("unknown claims")

// ClaimsNames lists every claim store.
func ClaimsNames() []ClaimsName {
	return []ClaimsName{ClaimsLocal, ClaimsBeads}
}

// ParseClaims returns the claim store that text names.
func ParseClaims(text string) (ClaimsName, error) {
	for _, name := range ClaimsNames() {
		if string(name) == text {
			return name, nil
		}
	}

	return "", fmt.Errorf("%w %q: use one of %s", ErrUnknownClaims, text, joinNames(ClaimsNames()))
}
