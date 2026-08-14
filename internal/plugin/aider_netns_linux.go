//go:build linux

package plugin

import (
	"os/exec"
	"syscall"
)

// applyNetnsToCmd isolates the subprocess in a fresh CLONE_NEWNET netns
// (loopback-only, no default route) so raw-socket tools inside aider cannot
// dial off the box. Mirrors internal/agent/netns_linux.go.
func applyNetnsToCmd(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
}
