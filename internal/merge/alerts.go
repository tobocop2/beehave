package merge

import (
	"errors"
	"fmt"

	"github.com/tobocop2/beehave/internal/model"
)

// alerts refuses a pull request with an open security alert at the gated head. A project that does not set security_alerts reads none.
// A forge that cannot read alerts is refused, and so is a read that fails: a failed read is never an empty list.
func (a *attempt) alerts() *refusal {
	if !a.req.Alerts {
		return nil
	}

	found, stop := a.readAlerts()
	if stop != nil {
		return stop
	}

	return a.openAlerts(found)
}

// readAlerts reads the open alerts at the gated head, or returns the refusal for a forge that cannot read them or a read that failed.
func (a *attempt) readAlerts() ([]model.Alert, *refusal) {
	unknown := fmt.Sprintf("        The security alerts of #%d cannot be read, so an open one cannot be ruled out.", a.req.PR)

	reads, ok := a.forge.(AlertReads)
	if !ok {
		return nil, refuse(fmt.Sprintf("REFUSE: %s: security alerts: this forge cannot read them.", model.ErrNoCapability), unknown)
	}

	found, err := reads.OpenAlerts(a.ctx, a.req.PR, a.req.Gated)
	if errors.Is(err, model.ErrNoCapability) {
		return nil, refuse("REFUSE: "+err.Error(), unknown)
	}

	if err != nil {
		return nil, transient(err.Error(), fmt.Sprintf("REFUSE: the security alerts of #%d could not be read, so an open one cannot be ruled out.", a.req.PR))
	}

	return found, nil
}

// openAlerts refuses a pull request that has an open alert, and lists each one.
func (a *attempt) openAlerts(found []model.Alert) *refusal {
	if len(found) == 0 {
		return nil
	}

	lines := []string{fmt.Sprintf("REFUSE: #%d has open security alerts at %s. Fix each alert or dismiss it on the forge:", a.req.PR, a.req.Gated)}
	for _, alert := range found {
		lines = append(lines, "        "+alert.String())
	}

	return refuse(lines...)
}
