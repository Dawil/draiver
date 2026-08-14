package session

import (
	"encoding/json"
	"errors"
	"os"
	"syscall"

	"github.com/Dawil/draiver/internal/store"
)

// Alive reports whether an attempt has a live agent session: its session.json
// records a pid that still names a running process. It reads session.json
// directly — without session.Open, which would MkdirAll the session/ dir, since a
// liveness check must not materialise a dir for every never-run attempt — and
// probes with the same signal-0 kernel check reconcile.OSProc.Alive and the web
// dot use. The project/CLI tier is out-of-process from the daemon, so it asks the
// OS itself rather than the daemon.
//
// A missing or unparseable session.json (never ran), or a zero/dead pid, reads as
// not live. pid-reuse — a recycled pid making a reaped session look live — is the
// accepted Tier-0 risk documented on reconcile.OSProc.Alive; it is low risk for a
// local dev fleet.
func Alive(root store.Root, ticket, attempt string) bool {
	data, err := os.ReadFile(root.SessionMetaPath(ticket, attempt))
	if err != nil {
		return false
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return false
	}
	return pidAlive(id.PID)
}

// pidAlive performs the signal-0 existence-and-permission probe: no error means
// the process exists and we may signal it, EPERM means it exists but is owned by
// another user (still alive), ESRCH means it is gone.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
