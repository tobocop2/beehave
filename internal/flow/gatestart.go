package flow

import (
	"context"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// gateHostStepName is the recorded step in which the first gate of a host with a size table starts the host.
const gateHostStepName = "gate:host"

// hostStart is the record of the step gate:host: whether this gate started the host.
type hostStart struct {
	Started bool `json:"started"`
}

// startSizedHost starts the host of a gate when the provider has a size table and the host is absent. host_up leaves such a host for the first gate,
// so the count of gates that wait is the count at the moment the gate needs the host. The start is a recorded step: a replay reads it and starts no second host.
// The gates of one engine start in turn under the placement lock, so the second gate finds the host up. heldLock says that the caller holds the lock.
// It returns the cluster that this gate started, or an empty text when it started none.
func (e *Engine) startSizedHost(ctx dbos.Context, in ExclusiveInput, heldLock bool) (string, error) {
	host, err := e.sizedHostOf(in)
	if err != nil || host == "" {
		return "", err
	}

	if !heldLock {
		e.placement.Lock()
		defer e.placement.Unlock()
	}

	got, err := runStep(e, ctx, gateHostStepName, func(c context.Context) (hostStart, error) {
		started, _, err := e.deps.Hosts.Up(c, in.Project, host, e.gatesWaiting(in.Project, e.store().List))

		return hostStart{Started: started}, err
	})
	if err != nil || !got.Started {
		return "", err
	}

	return host, nil
}

// sizedHostOf gives the cluster of a gate when the provider of the cluster has a size table. Any other gate gives an empty text.
func (e *Engine) sizedHostOf(in ExclusiveInput) (string, error) {
	host := strings.TrimPrefix(in.HostKey, hostKeyPrefix)
	if e.deps.Hosts == nil || host == "" {
		return "", nil
	}

	sized, err := e.deps.Hosts.Sized(in.Project, host)
	if err != nil || !sized {
		return "", err
	}

	return host, nil
}

// noteGateHost keeps the cluster that a gate of this run started, so host_teardown stops it as it stops a host that host_up started.
func (s *script) noteGateHost(ran ExclusiveOutput) {
	if ran.HostStarted != "" {
		s.hostStarted, s.gateStarted = true, ran.HostStarted
	}
}
