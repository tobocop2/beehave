package github

import (
	"context"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

// forbidden is the text that gh prints when the token may not read code scanning.
const forbidden = "HTTP 403"

// alertAnswer is one code scanning alert, as the alerts endpoint lists it.
type alertAnswer struct {
	Number int `json:"number"`
	Rule   struct {
		ID                    string `json:"id"`
		Severity              string `json:"severity"`
		SecuritySeverityLevel string `json:"security_severity_level"`
	} `json:"rule"`
	URL                string `json:"html_url"`
	MostRecentInstance struct {
		CommitSHA string `json:"commit_sha"`
	} `json:"most_recent_instance"`
}

// alert is the alert of the engine. The security severity is the better word when the alert has one.
func (a alertAnswer) alert() model.Alert {
	severity := a.Rule.SecuritySeverityLevel
	if severity == "" {
		severity = a.Rule.Severity
	}

	return model.Alert{Number: a.Number, Rule: a.Rule.ID, Severity: severity, URL: a.URL}
}

// analysisAnswer is one code scanning analysis, as the analyses endpoint lists it.
type analysisAnswer struct {
	ID int `json:"id"`
}

// scanned refuses a head that has no code scanning analysis: no alert at an unscanned head does not mean that the head is clean.
// https://docs.github.com/en/rest/code-scanning/code-scanning#list-code-scanning-analyses-for-a-repository
func (f *Forge) scanned(ctx context.Context, pr int, sha string) error {
	rows, err := readPages[analysisAnswer](ctx, f, "code-scanning/analyses?per_page=100&sha="+sha)
	if err != nil {
		return forge.Refusal("cannot read the code scanning analyses of pull request "+strconv.Itoa(pr), err)
	}

	if len(rows) == 0 {
		return forge.Refuse("Code scanning has no analysis for %s, the head of pull request %d, so its security alerts cannot be known.\n"+
			"The project sets security_alerts = True. Wait for the code scanning run to finish, or run it.", sha, pr)
	}

	return nil
}

// OpenAlerts returns the open code scanning alerts of the pull request whose most recent instance is at sha. An alert that was last seen at another commit is left out.
// A head with no analysis is an error, and so is a failed read: neither is an empty list.
// https://docs.github.com/en/rest/code-scanning/code-scanning#list-code-scanning-alerts-for-a-repository
func (f *Forge) OpenAlerts(ctx context.Context, pr int, sha string) ([]model.Alert, error) {
	if err := f.scanned(ctx, pr, sha); err != nil {
		return nil, err
	}

	rows, err := readPages[alertAnswer](ctx, f, "code-scanning/alerts?state=open&per_page=100&pr="+strconv.Itoa(pr))
	if err != nil {
		return nil, forge.Refusal("cannot read the security alerts of pull request "+strconv.Itoa(pr), err)
	}

	found := []model.Alert{}

	for _, row := range rows {
		if row.MostRecentInstance.CommitSHA == sha {
			found = append(found, row.alert())
		}
	}

	return found, nil
}

// Probe tells whether code scanning works for the repository. The alerts endpoint answers 404 or 403 when code scanning is off or the token lacks the security_events scope.
// Any other failure is an error that is no missing ability. Every ability but the alerts needs no probe.
func (f *Forge) Probe(ctx context.Context, a forge.Ability) error {
	if a != forge.AbilityAlerts {
		return nil
	}

	done := f.run.Run(ctx, []string{argAPI, "repos/" + f.repo + "/code-scanning/alerts?per_page=1"})

	switch {
	case done.ExitCode == 0:
		return nil
	case strings.Contains(done.Stderr, notFound), strings.Contains(done.Stderr, forbidden):
		return forge.NoCapability(a, "code scanning is off for the repository, or the token lacks the security_events scope")
	}

	return refused(done, "cannot tell whether code scanning is on")
}
