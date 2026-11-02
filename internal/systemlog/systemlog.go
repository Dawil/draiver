// Package systemlog is the per-attempt system-event journal: the operational
// view of what the *system* (the board's operator-initiated actions and
// draiverctld's per-attempt lifecycle) did to an attempt, and when (drv-045).
//
// It is modelled directly on reconcile's ctl.jsonl health substrate: a
// rebuildable, append-only JSON-lines file (session/system.jsonl) whose every
// record carries its OWN recorded timestamp, stamped at emit time — never at
// render time. That is the whole point: a system line's time is the time its
// writer stamped it, so the System Logs panel reads truthfully on a live follow
// AND on historical replay, the defect the old now()-stamped agent-render splice
// suffered.
//
// system.jsonl is NOT a truth substrate. It is an operational projection of
// actions, exactly like ctl.jsonl: safe to delete (the stream simply starts empty
// and refills from subsequent actions), and out of the durable hash-chained ticket
// log. A system event that is also a durable truth (a promotion-result, a retire
// note) stays authoritative in the ticket log; the system.jsonl line is a separate,
// operational record of the *act*, written directly at the action site — the fact
// is not double-written through two coordinated paths.
package systemlog

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Dawil/draiver/internal/store"
)

// System event classes — the stable family identifier a reader (and a test)
// keys on, mirroring reconcile's health Class* contract. They are intentionally
// coarse: the family of the action, not its exact detail (the detail rides in
// Message and Fields).
const (
	ClassProvenance   = "provenance"       // a provenance (repo/base) settings edit
	ClassGit          = "git"              // a git control (push/sync)
	ClassReviewEnvUp  = "review-env-up"    // a review environment brought up
	ClassReviewEnvDn  = "review-env-down"  // a review environment torn down
	ClassPromotion    = "promotion-check"  // a promotion check run (the act, not the durable verdict)
	ClassSupervision  = "supervision"      // the supervision dial (sibling action, optional)
	ClassCtlSession   = "ctl-session"      // draiverctld admitted/started a session for the attempt
	ClassCtlRetire    = "ctl-retire"       // draiverctld retired/parked the attempt
)

// SystemEvent is one line of session/system.jsonl: a timestamped operational
// record of a system action on an attempt. It mirrors reconcile.HealthEvent — an
// RFC3339 UTC TS stamped at emit time, a stable Class (the event family), a short
// human Message, and optional structured Fields (env name, a from→to for a setting
// change, a commit, a verdict, the actor). The struct is exported because streamlog
// unmarshals it to render the System Logs panel.
type SystemEvent struct {
	TS      string            `json:"ts"`               // RFC3339 UTC, stamped at emit
	Class   string            `json:"class"`            // one of the Class* contract strings
	Message string            `json:"message"`          // short human line, pre-composed by the emitter
	Fields  map[string]string `json:"fields,omitempty"` // optional structured detail (env, from, to, commit, outcome, actor)

	// Error marks the action as a failure, so the renderer colours the line with the
	// error role (mirroring a health error-start) rather than the neutral system role.
	Error bool `json:"error,omitempty"`
}

// Append stamps ev with the current time and appends it to the attempt's
// system.jsonl. It is best-effort and mirrors reconcile.appendHealth: it ensures
// the session dir, marshals one line, and O_APPEND|O_CREATE-writes it. The record's
// recorded TS is always set here (overwriting any caller-supplied value) so every
// line owns the real moment of the action. A write failure is returned for the
// caller to log operationally, but callers never fail their action on it —
// system.jsonl is rebuildable and the next action re-opens the file.
func Append(root store.Root, id, attempt string, now func() time.Time, ev SystemEvent) error {
	if now == nil {
		now = time.Now
	}
	ev.TS = now().UTC().Format(time.RFC3339)
	if err := root.EnsureSessionDir(id, attempt); err != nil {
		return fmt.Errorf("systemlog %s/%s: ensure dir: %w", id, attempt, err)
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("systemlog %s/%s: marshal: %w", id, attempt, err)
	}
	f, err := os.OpenFile(root.SessionSystemLogPath(id, attempt), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("systemlog %s/%s: open: %w", id, attempt, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("systemlog %s/%s: write: %w", id, attempt, err)
	}
	return nil
}
