package config

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	holdLabel        = "hold"
	noIntegrationPR  = "none"
	defaultStore     = "github"
	defaultFactor    = "2"
	defaultJobsDir   = "/home/jobs"
	defaultHours     = 1.0
	defaultDiskFloor = 60
	floatBits        = 64
)

var (
	itemSplit    = regexp.MustCompile(`[ \t\n,#]+`)
	factorFormat = regexp.MustCompile(`^[0-9.]+$`)
)

// Settings reads the round keys from a merged environment. A key is read and checked when a
// command asks for it, so a bad value blocks only the commands that read that key.
type Settings struct {
	env map[string]string
}

// NewSettings returns the settings of a merged environment. Nothing is checked until a key is read.
func NewSettings(env map[string]string) Settings {
	return Settings{env: env}
}

// Repo returns REPO, or "" when unset.
func (s Settings) Repo() string { return s.env["REPO"] }

// DefaultBranch returns DEFAULT_BRANCH, or "" when unset.
func (s Settings) DefaultBranch() string { return s.env["DEFAULT_BRANCH"] }

// RoundPrefix returns ROUND_PREFIX, or "" when unset.
func (s Settings) RoundPrefix() string { return s.env["ROUND_PREFIX"] }

// Base returns BASE, or "" when unset.
func (s Settings) Base() string { return s.env["BASE"] }

// IntegrationBranch returns INTEGRATION_BRANCH, or "" when unset.
func (s Settings) IntegrationBranch() string { return s.env["INTEGRATION_BRANCH"] }

// RoundName returns ROUND, or "" when unset.
func (s Settings) RoundName() string { return s.env["ROUND"] }

// MeLogin returns ME_LOGIN, or "" when unset.
func (s Settings) MeLogin() string { return s.env["ME_LOGIN"] }

// MeEmail returns ME_EMAIL, or "" when unset.
func (s Settings) MeEmail() string { return s.env["ME_EMAIL"] }

// RoundEpic returns ROUND_EPIC, or "" when unset.
func (s Settings) RoundEpic() string { return s.env["ROUND_EPIC"] }

// BoxSSH returns BOX_SSH, or "" when unset.
func (s Settings) BoxSSH() string { return s.env["BOX_SSH"] }

// MutationStepRE returns MUTATION_STEP_RE, or "" when unset.
func (s Settings) MutationStepRE() string { return s.env["MUTATION_STEP_RE"] }

// IssueStore returns ISSUE_STORE, or "github" when unset or empty.
func (s Settings) IssueStore() string { return s.orDefault("ISSUE_STORE", defaultStore) }

// JobsDir returns JOBS_DIR, or "/home/jobs" when unset or empty.
func (s Settings) JobsDir() string { return s.orDefault("JOBS_DIR", defaultJobsDir) }

// IdleHours returns IDLE_HOURS, or 1 when unset or empty.
func (s Settings) IdleHours() (float64, error) { return s.float("IDLE_HOURS", defaultHours) }

// GraceHours returns GRACE_HOURS, or 1 when unset or empty.
func (s Settings) GraceHours() (float64, error) { return s.float("GRACE_HOURS", defaultHours) }

// FloorPRsStaging returns FLOOR_PRS_STAGING and whether it is set.
func (s Settings) FloorPRsStaging() (int, bool, error) { return s.whole("FLOOR_PRS_STAGING") }

// FloorPRsDev returns FLOOR_PRS_DEV and whether it is set.
func (s Settings) FloorPRsDev() (int, bool, error) { return s.whole("FLOOR_PRS_DEV") }

// FloorPRsOpen returns FLOOR_PRS_OPEN and whether it is set.
func (s Settings) FloorPRsOpen() (int, bool, error) { return s.whole("FLOOR_PRS_OPEN") }

// FloorPRsBase returns FLOOR_PRS_BASE and whether it is set.
func (s Settings) FloorPRsBase() (int, bool, error) { return s.whole("FLOOR_PRS_BASE") }

// IntegrationPR returns INTEGRATION_PR and whether it is set. "none" and empty both mean the
// round has no integration pull request.
func (s Settings) IntegrationPR() (int, bool, error) {
	if s.env["INTEGRATION_PR"] == noIntegrationPR {
		return 0, false, nil
	}

	return s.whole("INTEGRATION_PR")
}

// DiskFloorGB returns DISK_FLOOR_GB, or 60 when unset or empty.
func (s Settings) DiskFloorGB() (int, error) {
	floor, set, err := s.whole("DISK_FLOOR_GB")
	if err != nil || !set {
		return defaultDiskFloor, err
	}

	return floor, nil
}

// LaneCap returns LANE_CAP and whether it is set. A cap of 0 allows no lane and is refused.
func (s Settings) LaneCap() (int, bool, error) {
	limit, set, err := s.whole("LANE_CAP")
	if set && limit == 0 {
		return 0, false, refuse("LANE_CAP=%s allows no lane: set 1 or more, or leave it unset.", s.env["LANE_CAP"])
	}

	return limit, set, err
}

// IntegrationPRs returns the pull request numbers of INTEGRATION_PRS, or of INTEGRATION_PR when
// that is empty. Blanks are removed before the split on commas, so "12 13" reads as "1213".
func (s Settings) IntegrationPRs() ([]string, error) {
	raw := s.env["INTEGRATION_PRS"]
	if raw == "" {
		raw = s.env["INTEGRATION_PR"]
	}

	if raw == noIntegrationPR {
		return nil, nil
	}

	entries := slices.DeleteFunc(strings.Split(strings.ReplaceAll(raw, " ", ""), ","), func(entry string) bool { return entry == "" })

	for _, entry := range entries {
		if !isDigits(entry) {
			return nil, refuse("INTEGRATION_PRS has %q, which is not a pull request number. Write numbers separated by commas.", entry)
		}
	}

	return entries, nil
}

// RoundItems returns the item ids of ROUND_ITEMS, split on blanks, tabs, newlines, commas and "#".
func (s Settings) RoundItems() []string {
	var items []string

	for _, item := range itemSplit.Split(s.env["ROUND_ITEMS"], -1) {
		if item != "" {
			items = append(items, item)
		}
	}

	return items
}

// GateLoadFactor returns GATE_LOAD_FACTOR as written, or "2". Any run of digits and dots passes,
// so "1.2.3" passes. The text stays text because "1.2.3" is not a number.
func (s Settings) GateLoadFactor() (string, error) {
	value := s.orDefault("GATE_LOAD_FACTOR", defaultFactor)
	if !factorFormat.MatchString(value) {
		return "", refuse("GATE_LOAD_FACTOR=%s is not a number. Write digits and dots, for example 2.", value)
	}

	return value, nil
}

// IsHeld reports whether the comma-separated label list holds the whole label "hold".
func IsHeld(labels string) bool {
	for label := range strings.SplitSeq(labels, ",") {
		if label == holdLabel {
			return true
		}
	}

	return false
}

func (s Settings) orDefault(name, fallback string) string {
	if value := s.env[name]; value != "" {
		return value
	}

	return fallback
}

func (s Settings) float(name string, fallback float64) (float64, error) {
	value := s.env[name]
	if value == "" {
		return fallback, nil
	}

	number, err := strconv.ParseFloat(strings.TrimSpace(value), floatBits)
	if err != nil || strings.ContainsAny(value, "xX") {
		return 0, refuse("%s=%s is not a number. Write a number such as 1.5.", name, value)
	}

	return number, nil
}

func (s Settings) whole(name string) (int, bool, error) {
	value := s.env[name]
	if value == "" {
		return 0, false, nil
	}

	if !isDigits(value) {
		return 0, false, refuse("%s=%s is not a whole number. Write digits only, for example 3.", name, value)
	}

	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, false, refuse("%s=%s is too large. Write a smaller whole number.", name, value)
	}

	return number, true, nil
}

func isDigits(text string) bool {
	for i := range len(text) {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}

	return true
}
