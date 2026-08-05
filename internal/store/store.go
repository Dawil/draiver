// Package store resolves the on-disk layout of the ticket data root. A ticket
// holds a shared spec.md plus one or more attempts; each attempt owns its own
// log, artefacts, hash chain, and generated state.md. store owns paths only;
// reading and appending the log lives in ticketlog.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Root is a resolved data-root directory holding one folder per ticket.
type Root struct{ Dir string }

// Resolve picks the data root from, in order: an explicit flag value, the
// DRAIVER_DATA environment variable, then ~/.draiver/data.
func Resolve(flagVal string) (Root, error) {
	dir := flagVal
	if dir == "" {
		dir = os.Getenv("DRAIVER_DATA")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Root{}, fmt.Errorf("resolve data root: %w", err)
		}
		dir = filepath.Join(home, ".draiver", "data")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Root{}, fmt.Errorf("resolve data root: %w", err)
	}
	return Root{Dir: abs}, nil
}

// --- root-level runtime paths (draiverctld; rebuildable, out of the hash chain) ---

// ControllerPath is the running controller's identity record: the pid and boot
// nonce of the `ctl up` supervisor ("pid 1"), written on start and removed on
// clean exit. It is the root-level "is the daemon up?" handle the imperative
// client verbs probe before handing an attempt off, and the nonce that stamps
// their transient desired-markers so a marker dies with the daemon that owns it
// (drvctl-016). It is deliberately outside any ticket, since one controller
// spans all of them.
func (r Root) ControllerPath() string { return filepath.Join(r.Dir, "controller.json") }

// --- ticket-level paths (shared across attempts) ---

// TicketDir is the folder for a ticket id.
func (r Root) TicketDir(id string) string { return filepath.Join(r.Dir, id) }

// SpecPath is the immutable design input for a ticket, shared across attempts.
func (r Root) SpecPath(id string) string { return filepath.Join(r.TicketDir(id), "spec.md") }

// AttemptsDir holds a ticket's attempts.
func (r Root) AttemptsDir(id string) string { return filepath.Join(r.TicketDir(id), "attempts") }

// --- attempt-level paths ---

// AttemptDir is the folder for one attempt on a ticket.
func (r Root) AttemptDir(id, attempt string) string {
	return filepath.Join(r.AttemptsDir(id), attempt)
}

// AttemptMetaPath is an attempt's provenance file (tool, model, started, ...).
func (r Root) AttemptMetaPath(id, attempt string) string {
	return filepath.Join(r.AttemptDir(id, attempt), "attempt.md")
}

// LogDir is the append-only event directory for one attempt.
func (r Root) LogDir(id, attempt string) string {
	return filepath.Join(r.AttemptDir(id, attempt), "log")
}

// ArtefactsDir holds blobs an attempt's log references.
func (r Root) ArtefactsDir(id, attempt string) string {
	return filepath.Join(r.AttemptDir(id, attempt), "artefacts")
}

// StatePath is the generated projection for one attempt (never read as truth).
func (r Root) StatePath(id, attempt string) string {
	return filepath.Join(r.AttemptDir(id, attempt), "state.md")
}

// DesiredMarkerPath is an attempt's transient imperative desired-marker: the
// stamped "the client asked the daemon to run this now" flag `ctl start` writes
// and the reconcile loop unions into its desired set (drvctl-016). It is
// rebuildable runtime state, not part of the hash chain, and lives at the
// attempt root (alongside the generated state.md) so it survives a session
// flush that clears the session/ dir.
func (r Root) DesiredMarkerPath(id, attempt string) string {
	return filepath.Join(r.AttemptDir(id, attempt), "desired.json")
}

// --- session runtime paths (draiverctld; rebuildable, out of the hash chain) ---

// SessionDir holds an attempt's per-session runtime state. It is rebuildable
// from disk plus the live process table and is deliberately excluded from the
// hash-chained log, so it is created on demand when a session starts rather than
// with the attempt.
func (r Root) SessionDir(id, attempt string) string {
	return filepath.Join(r.AttemptDir(id, attempt), "session")
}

// SessionMetaPath is the session's identity record (adapter, model, session id,
// pid, worktree, started).
func (r Root) SessionMetaPath(id, attempt string) string {
	return filepath.Join(r.SessionDir(id, attempt), "session.json")
}

// SessionStreamPath is the raw stream-json tee (the session "journal"); semantic
// events are promoted from it into the durable log.
func (r Root) SessionStreamPath(id, attempt string) string {
	return filepath.Join(r.SessionDir(id, attempt), "stream.jsonl")
}

// SessionMeterPath is the live token/cost/context + watchdog tally, folded into
// attempt.md metrics on retire.
func (r Root) SessionMeterPath(id, attempt string) string {
	return filepath.Join(r.SessionDir(id, attempt), "meter.json")
}

// --- existence & listing ---

// Exists reports whether a ticket folder is present.
func (r Root) Exists(id string) bool {
	fi, err := os.Stat(r.TicketDir(id))
	return err == nil && fi.IsDir()
}

// AttemptExists reports whether an attempt folder is present.
func (r Root) AttemptExists(id, attempt string) bool {
	fi, err := os.Stat(r.AttemptDir(id, attempt))
	return err == nil && fi.IsDir()
}

// EnsureTicketDir creates the ticket folder (for the shared spec.md).
func (r Root) EnsureTicketDir(id string) error {
	if err := os.MkdirAll(r.TicketDir(id), 0o755); err != nil {
		return fmt.Errorf("ensure ticket dir %s: %w", id, err)
	}
	return nil
}

// EnsureAttemptDirs creates an attempt's log/ and artefacts/ subdirs (and every
// parent, including the ticket folder).
func (r Root) EnsureAttemptDirs(id, attempt string) error {
	for _, d := range []string{r.LogDir(id, attempt), r.ArtefactsDir(id, attempt)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("ensure %s: %w", d, err)
		}
	}
	return nil
}

// EnsureSessionDir creates an attempt's session/ runtime dir (and every parent).
func (r Root) EnsureSessionDir(id, attempt string) error {
	if err := os.MkdirAll(r.SessionDir(id, attempt), 0o755); err != nil {
		return fmt.Errorf("ensure session dir %s/%s: %w", id, attempt, err)
	}
	return nil
}

// ListTickets returns the ticket ids present under the root, sorted. A missing
// root is treated as empty, not an error.
func (r Root) ListTickets() ([]string, error) {
	return listDirs(r.Dir)
}

// ListAttempts returns the attempt ids for a ticket, sorted. A ticket with no
// attempts dir is treated as empty, not an error.
func (r Root) ListAttempts(id string) ([]string, error) {
	return listDirs(r.AttemptsDir(id))
}

func listDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
