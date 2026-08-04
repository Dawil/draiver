package reconcile

import (
	"errors"
	"os"
	"syscall"
)

// OSProc is the production Proc: it reasons about real session processes through
// the OS process table. Liveness is a signal-0 probe; a recorded pid may in
// principle have been recycled by the OS onto an unrelated process, an accepted
// Tier-0 risk noted in Adopt.
type OSProc struct{}

// Alive reports whether pid names a live process. Signal 0 performs the kernel's
// existence-and-permission check without delivering a signal: no error means the
// process exists and we may signal it; EPERM means it exists but is owned by
// another user (still alive); ESRCH means it is gone.
func (OSProc) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Terminate sends SIGTERM to pid, asking it to stop. A process that is already
// gone is treated as success — the goal state (not running) is reached either way.
func (OSProc) Terminate(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}
