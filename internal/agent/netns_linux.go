//go:build linux

// netns_linux.go: the bash tool's network-namespace isolation (the
// 数据不出境 moat). Per MVP plan §5 `fix-bash-tool-egress-bypass`, the bash
// subprocess dials out directly (exec.Command), bypassing installEgress (which
// only gates Go-process HTTP dials via http.DefaultTransport.DialContext). To
// close that hole we run every bash subprocess in a fresh CLONE_NEWNET netns:
// it gets a loopback-only network namespace with no default route, so
// raw-socket tools (curl, wget, nc, bespoke binaries) cannot reach any
// non-loopback address regardless of proxy env.
//
// CLONE_NEWNET requires CAP_SYS_ADMIN in the current user namespace. On a
// hardened 信创 kernel with unprivileged_userns_clone=0 and no CAP_SYS_ADMIN
// the clone is denied — and we FAIL CLOSED: the bash tool refuses to run
// (netnsAvailable reports false, bashTool returns an error to the agent loop)
// rather than silently falling back to an un-isolated exec that would leave
// the moat broken. The proxy-env fallback is intentionally DROPPED: no
// listening CONNECT proxy exists on the box, and raw-socket tools ignore env
// anyway.
package agent

import (
	"os/exec"
	"sync"
	"syscall"
)

// netnsAvailable reports whether this process can create a CLONE_NEWNET netns.
// It is a var (not a func) so tests can swap it to force the fail-closed or
// isolated paths without depending on kernel capabilities. The default probes
// once via a trivial clone and caches the result for the process lifetime.
var netnsAvailable = defaultNetnsAvailable

// defaultNetnsAvailable probes CLONE_NEWNET once and memoizes it so we do not
// fork on every bash dispatch.
var (
	defaultNetnsAvailable = sync.OnceValue(probeNetns)
)

// probeNetns performs a one-shot CLONE_NEWNET clone of `true`. If the clone
// succeeds the process can isolate bash subprocesses; if it is denied
// (EPERM / userns disabled) we report false so the bash tool fails closed.
func probeNetns() bool {
	c := exec.Command("true")
	c.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	return c.Run() == nil
}

// applyNetns sets the clone flag so c runs in a loopback-only netns. Called
// only after netnsAvailable() reported true, so the clone will succeed.
func applyNetns(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
}
