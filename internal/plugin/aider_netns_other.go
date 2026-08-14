//go:build !linux

package plugin

import "os/exec"

// applyNetnsToCmd is a no-op on non-Linux; CLONE_NEWNET is unavailable. The
// adapter is expected to run on the Linux 信创 box where airtapd runs; the
// host's startup check (agent.NetnsAvailable) surfaces the requirement.
func applyNetnsToCmd(c *exec.Cmd) {}
