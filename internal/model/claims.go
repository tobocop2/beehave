package model

// ClaimsName names the claim store that a project selects.
type ClaimsName string

// The claim stores.
const (
	ClaimsLocal ClaimsName = "local"
	ClaimsBeads ClaimsName = "beads"
)
