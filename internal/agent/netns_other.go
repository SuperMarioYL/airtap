//go:build !linux

// netns_other.go: on non-Linux platforms CLONE_NEWNET is unavailable, so the
// bash tool FAILS CLOSED (refuses to run) rather than silently running an
// un-isolated subprocess. Airtapd is a Linux-信创-box daemon; on macOS / other
// dev hosts the bash tool is not expected to be usable, and tests assert the
// fail-closed behavior. This preserves the 数据不出境 moat: there is no path
// where a bash subprocess escapes the egress gate.
package agent

import "os/exec"

// netnsAvailable is a var so tests can swap it; on non-Linux it is always
// false, forcing bashTool to fail-closed.
var netnsAvailable = func() bool { return false }

// applyNetns is a no-op on non-Linux; bashTool checks availability first and
// never reaches here when isolation is impossible.
func applyNetns(c *exec.Cmd) {}
