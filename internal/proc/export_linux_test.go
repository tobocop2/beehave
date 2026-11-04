//go:build linux

package proc

// WithScopeTools returns r with the programs and the unit name prefix that tests supply for the scope of a capped command. An empty value keeps the system one.
func WithScopeTools(r ExecRunner, systemdRun, systemctl, unitPrefix string) ExecRunner {
	r.hooks = platformHooks{systemdRun: systemdRun, systemctl: systemctl, unitPrefix: unitPrefix}

	return r
}
