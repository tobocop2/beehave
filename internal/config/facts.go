package config

import (
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// withFacts checks the facts of a project file and copies them into the profile. A fact that the file leaves out stays empty.
func withFacts(profile model.Project, raw rawProject) (model.Project, error) {
	if err := errors.Join(checkLists(raw), checkDicts(raw)); err != nil {
		return model.Project{}, err
	}

	profile.People, profile.Epics = maps.Clone(raw.People), maps.Clone(raw.Epics)
	profile.GateFiles, profile.Conventions = slices.Clone(raw.GateFiles), slices.Clone(raw.Conventions)
	profile.BriefRules, profile.StackedRules = slices.Clone(raw.BriefRules), slices.Clone(raw.StackedRules)
	profile.Limits, profile.Traps, profile.TokenRules = slices.Clone(raw.Limits), slices.Clone(raw.Traps), slices.Clone(raw.TokenRules)

	return profile, nil
}

// checkLists refuses a blank item in a list of facts.
func checkLists(raw rawProject) error {
	lists := map[string]paths{
		"gate_files": raw.GateFiles, "conventions": raw.Conventions, "brief_rules": raw.BriefRules, "stacked_rules": raw.StackedRules,
		"limits": raw.Limits, "traps": raw.Traps, "token_rules": raw.TokenRules,
	}

	var problems []error

	for _, key := range slices.Sorted(maps.Keys(lists)) {
		if slices.ContainsFunc(lists[key], blank) {
			problems = append(problems, refuseAt(raw.At, "`%s` lists a blank string. Write the fact or remove the item.", key))
		}
	}

	return errors.Join(problems...)
}

// checkDicts refuses a blank key or a blank value in a dict of facts.
func checkDicts(raw rawProject) error {
	var problems []error

	dicts := map[string]table{"people": raw.People, "epics": raw.Epics}

	for _, key := range slices.Sorted(maps.Keys(dicts)) {
		for _, name := range slices.Sorted(maps.Keys(dicts[key])) {
			value := dicts[key][name]

			if blank(name) || blank(value) {
				problems = append(problems, refuseAt(raw.At, "`%s` holds a blank name or value (%q: %q). Write both or remove the entry.", key, name, value))
			}
		}
	}

	return errors.Join(problems...)
}

func blank(text string) bool { return strings.TrimSpace(text) == "" }
