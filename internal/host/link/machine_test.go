package link_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

func TestForMachine_SendsTheOptionsAndGuardsTheTarget(t *testing.T) {
	t.Parallel()

	ssh := proctest.Always(model.CommandResult{})
	opts := link.Options{Address: "10.0.0.5", User: "ci", Port: 2222, Identity: "/keys/ci", KnownHosts: model.KnownHostsAcceptNew}

	l, err := link.ForMachine("gates", opts, ssh)
	require.NoError(t, err)

	_, err = l.Remote(t.Context(), "true", "true")
	require.NoError(t, err)

	assert.Equal(t, [][]string{{
		"-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "HostName=10.0.0.5", "-o", "User=ci", "-o", "Port=2222", "-o", "IdentityFile=/keys/ci", "-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=accept-new", "--", "gates", "true",
	}}, ssh.Calls)
}

func TestForMachine_NoPolicyMeansStrictAndNeverNo(t *testing.T) {
	t.Parallel()

	ssh := proctest.Always(model.CommandResult{})

	l, err := link.ForMachine("gates", link.Options{}, ssh)
	require.NoError(t, err)

	l.Exec(t.Context(), "true")

	line := strings.Join(ssh.Calls[0], " ")
	assert.Contains(t, line, "StrictHostKeyChecking=yes")
	assert.NotContains(t, line, "HostName")
	assert.NotContains(t, line, "StrictHostKeyChecking=no")
}

func TestForMachine_RefusesAWordThatCouldBecomeAnOption(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		target string
		opts   link.Options
	}{
		"a target with a dash":   {target: "-oProxyCommand=x"},
		"a target with a space":  {target: "a b"},
		"an empty target":        {target: ""},
		"an address with a dash": {target: "gates", opts: link.Options{Address: "-x"}},
		"a user with a space":    {target: "gates", opts: link.Options{User: "a b"}},
		"an identity with space": {target: "gates", opts: link.Options{Identity: "/k/a b"}},
		"an unknown policy":      {target: "gates", opts: link.Options{KnownHosts: "no"}},
		"a port too high":        {target: "gates", opts: link.Options{Port: 70000}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := link.ForMachine(tc.target, tc.opts, proctest.Always(model.CommandResult{}))

			require.ErrorIs(t, err, link.ErrConfig)
		})
	}
}
