package model

import "time"

// TrackerName names a tracker adapter that a project selects. The package of each tracker exports its name.
type TrackerName string

// ItemFields lists the fields that every tracker declares for an item: id, title, body and severity. Each tracker returns this list from its ItemFields,
// and the check of a workflow file reads it to refuse an item field that no tracker has. The severity is P0 to P3, or P? when the tracker keeps none.
func ItemFields() []string { return []string{"id", "title", "body", "severity"} }

// ForgeName names a forge adapter that a project selects. The package of each forge exports its name.
type ForgeName string

// OverlapRequest names what Forge.Overlap compares. Set PR, or set Files to a list of file names and optionally Since.
type OverlapRequest struct {
	PR          string
	Files       string
	Since       string
	Base        string
	Integration string
	Now         time.Time
}

// Overlap is the lines the check prints and how many overlaps it found. A caller exits 1 when Found is above zero.
type Overlap struct {
	Lines []string
	Found int
}
