package merge

import (
	"errors"
	"fmt"

	forgepkg "github.com/tobocop2/beehave/internal/forge"
)

// alerts refuses a pull request with an open security alert at the gated head. A project that does not set security_alerts reads none.
// A forge that cannot read alerts is refused, and so is a read that fails: a failed read is never an empty list.
func (a *attempt) alerts() *refusal {
	if !a.req.Alerts {
		return nil
	}

	reads, ok := a.forge.(forgepkg.AlertReads)
	if !ok {
		return refuse("REFUSE: " + forgepkg.NoCapability(forgepkg.AbilityAlerts, fmt.Sprintf("the security alerts of #%d cannot be read, so an open one cannot be ruled out.", a.req.PR)).Error())
	}

	found, err := reads.OpenAlerts(a.ctx, a.req.PR, a.req.Gated)
	if errors.Is(err, forgepkg.ErrNoCapability) {
		return refuse("REFUSE: "+err.Error(), fmt.Sprintf("        The security alerts of #%d cannot be read, so an open one cannot be ruled out.", a.req.PR))
	}

	if err != nil {
		return transient(err.Error(), fmt.Sprintf("REFUSE: the security alerts of #%d could not be read, so an open one cannot be ruled out.", a.req.PR))
	}

	if len(found) == 0 {
		return nil
	}

	lines := []string{fmt.Sprintf("REFUSE: #%d has open security alerts at %s. Fix each alert or dismiss it on the forge:", a.req.PR, a.req.Gated)}
	for _, alert := range found {
		lines = append(lines, "        "+alert.String())
	}

	return refuse(lines...)
}
