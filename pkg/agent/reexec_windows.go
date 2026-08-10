//go:build windows

package agent

func reexecAgent() error {
	// The detached upgrade helper replaces and restarts the agent after this
	// process exits and releases the running executable.
	return nil
}
