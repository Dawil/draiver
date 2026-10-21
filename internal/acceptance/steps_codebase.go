package acceptance

import (
	"fmt"
	"os"
	"strings"

	"github.com/Dawil/draiver/internal/audit"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/kb"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// These steps exercise drv-037's ticket-type model end to end over real on-disk
// specs and the real control fold (project.LoadAttempt / LoadAll). A `type:
// codebase` ledger is never enabled, rests Parked (Pending), and has no Done
// transition; an ordinary ticket (absent / `type: ticket`) is untouched. The fold
// the board, brief, and reconciler all read is project.LoadAttempt, so asserting
// on it is asserting on the agent- and operator-facing surface.

const cbAtt = "0001"

// seedTypedTicket writes a throwaway data root with one attempt whose spec carries
// the given `type:` line (pass "" for none), plus attempt.md and a created event —
// the minimal on-disk ticket project.LoadAttempt reads. The root is registered for
// cleanup on the World.
func seedTypedTicket(w *World, ticket, typeLine string) (store.Root, error) {
	dir, err := os.MkdirTemp("", "acc-cb-")
	if err != nil {
		return store.Root{}, err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	root := store.Root{Dir: dir}
	if err := addTypedTicket(root, dir, ticket, typeLine); err != nil {
		return store.Root{}, err
	}
	return root, nil
}

// addTypedTicket writes one typed ticket (spec + attempt.md + created event) into an
// existing root, so a scenario can stand up several tickets side by side.
func addTypedTicket(root store.Root, repo, ticket, typeLine string) error {
	if err := root.EnsureAttemptDirs(ticket, cbAtt); err != nil {
		return err
	}
	spec := "---\nid: " + ticket + "\ntitle: " + ticket + " subject\n" + typeLine + "---\n\n# " + ticket + "\n"
	if err := os.WriteFile(root.SpecPath(ticket), []byte(spec), 0o644); err != nil {
		return err
	}
	meta := "---\nid: " + cbAtt + "\nticket: " + ticket + "\nrepo: " + repo + "\nbase: main\n---\n"
	if err := os.WriteFile(root.AttemptMetaPath(ticket, cbAtt), []byte(meta), 0o644); err != nil {
		return err
	}
	_, err := ticketlog.Append(root, ticket, cbAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"})
	return err
}

func cbAppend(root store.Root, ticket string, e event.Event) error {
	e.Actor = "agent:acceptance"
	_, err := ticketlog.Append(root, ticket, cbAtt, e)
	return err
}

func cbLoad(w *World) (project.Attempt, error) {
	root := w.get("root").(store.Root)
	ticket := w.get("ticket").(string)
	return project.LoadAttempt(root, ticket, cbAtt)
}

// ---- an ordinary ticket is unaffected ----

func givenTicketNoType(w *World, sr *StepRun) error {
	root, err := seedTypedTicket(w, "ORD-1", "")
	if err != nil {
		return err
	}
	w.set("root", root)
	w.set("ticket", "ORD-1")
	return nil
}

func thenLoadsAsOrdinaryTicketRunning(w *World, sr *StepRun) error {
	a, err := cbLoad(w)
	if err != nil {
		return err
	}
	if a.Type != project.TypeTicket {
		return fmt.Errorf("Type = %q, want %q (an absent type defaults to ordinary)", a.Type, project.TypeTicket)
	}
	if a.State != project.Running {
		return fmt.Errorf("resting state = %q, want Running (an ordinary ticket is untouched)", a.State)
	}
	attachText(sr, "ordinary-ticket",
		"spec with no type: → type "+a.Type+", resting state "+string(a.State)+" (exactly as today)")
	return nil
}

// ---- a codebase ledger rests Parked, never actuates ----

func givenCodebaseLedger(w *World, sr *StepRun) error {
	root, err := seedTypedTicket(w, "CB-1", "type: codebase\n")
	if err != nil {
		return err
	}
	w.set("root", root)
	w.set("ticket", "CB-1")
	return nil
}

func thenLedgerRestsParkedNotEnabled(w *World, sr *StepRun) error {
	a, err := cbLoad(w)
	if err != nil {
		return err
	}
	if a.Type != project.TypeCodebase {
		return fmt.Errorf("Type = %q, want %q", a.Type, project.TypeCodebase)
	}
	if a.State != project.Pending {
		return fmt.Errorf("resting state = %q, want Pending (Parked)", a.State)
	}
	if a.Enabled {
		return fmt.Errorf("a freshly-authored ledger must not be enabled")
	}
	attachText(sr, "ledger-rests-parked",
		"type: codebase → type "+a.Type+", resting state "+string(a.State)+" (Parked), enabled="+fmt.Sprintf("%t", a.Enabled))
	return nil
}

func thenReconcilerNeverAdmits(w *World, sr *StepRun) error {
	a, err := cbLoad(w)
	if err != nil {
		return err
	}
	// Mirror the reconciler's admission predicate (reconcile.go: a desired run is
	// `State == Running && Enabled`). A Parked, unenabled ledger can never satisfy it,
	// so the daemon never spawns a session for it — enforced by construction, no gate.
	admitted := a.State == project.Running && a.Enabled
	if admitted {
		return fmt.Errorf("reconciler would admit the ledger (state=%q enabled=%t); it must never run one", a.State, a.Enabled)
	}
	return nil
}

// ---- no Done transition, but Archivable ----

func givenLedgerStrayDoneArchive(w *World, sr *StepRun) error {
	root, err := seedTypedTicket(w, "CB-1", "type: codebase\n")
	if err != nil {
		return err
	}
	if err := cbAppend(root, "CB-1", event.Event{Type: "done", Body: "an attempt to close the ledger"}); err != nil {
		return err
	}
	if err := cbAppend(root, "CB-1", event.Event{Type: "archive", Body: "the codebase is retired"}); err != nil {
		return err
	}
	w.set("root", root)
	w.set("ticket", "CB-1")
	return nil
}

func thenLedgerNeverDoneRestsParked(w *World, sr *StepRun) error {
	a, err := cbLoad(w)
	if err != nil {
		return err
	}
	if a.State == project.Done {
		return fmt.Errorf("ledger reached Done — the transition must be excluded for a codebase ledger")
	}
	if a.State != project.Pending {
		return fmt.Errorf("ledger after a stray done = %q, want Pending (still Parked)", a.State)
	}
	attachText(sr, "ledger-no-done",
		"a stray `done` event does not close the ledger: resting state is "+string(a.State)+" (Parked), not Done")
	return nil
}

func thenLedgerTakenOffBoard(w *World, sr *StepRun) error {
	a, err := cbLoad(w)
	if err != nil {
		return err
	}
	if !a.Archived {
		return fmt.Errorf("ledger is not archived; Archived is a ledger's only terminal state")
	}
	return nil
}

// ---- surfaces only via the normal state machine ----

func givenLedgerOpenEscalation(w *World, sr *StepRun) error {
	root, err := seedTypedTicket(w, "CB-1", "type: codebase\n")
	if err != nil {
		return err
	}
	if err := cbAppend(root, "CB-1", event.Event{Type: "escalation", Body: "which trunk is authoritative for this codebase?"}); err != nil {
		return err
	}
	w.set("root", root)
	w.set("ticket", "CB-1")
	return nil
}

func thenLedgerSurfacesNeedsMe(w *World, sr *StepRun) error {
	a, err := cbLoad(w)
	if err != nil {
		return err
	}
	if a.State != project.NeedsMe {
		return fmt.Errorf("ledger with an open escalation = %q, want Needs me (it surfaces via the normal machine)", a.State)
	}
	if len(a.OpenEscalations) != 1 {
		return fmt.Errorf("open escalations = %d, want 1", len(a.OpenEscalations))
	}
	attachText(sr, "ledger-needs-me",
		"an open escalation on a ledger surfaces as "+string(a.State)+" — the same Needs-me path as any ticket")
	return nil
}

func givenLedgerAndOrdinary(w *World, sr *StepRun) error {
	dir, err := os.MkdirTemp("", "acc-cb-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	root := store.Root{Dir: dir}
	// A resting codebase ledger (Parked) beside an ordinary ticket (Running).
	if err := addTypedTicket(root, dir, "CB-1", "type: codebase\n"); err != nil {
		return err
	}
	if err := addTypedTicket(root, dir, "ORD-1", ""); err != nil {
		return err
	}
	w.set("root", root)
	return nil
}

func thenRollupCountsOrdinaryNotLedger(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	attempts, err := project.LoadAll(root)
	if err != nil {
		return err
	}
	// Mirror the rollup's tally (web/rollup.go outcomeOf): the Running/attention
	// totals count only these control states. A Parked (Pending) ledger is in none
	// of them, so it never inflates the human's attention or Running counts.
	counted := map[project.State]bool{
		project.Done: true, project.Review: true, project.Running: true, project.NeedsMe: true,
	}
	totals := map[project.State]int{}
	var ledger project.Attempt
	for _, a := range attempts {
		if counted[a.State] {
			totals[a.State]++
		}
		if a.Ticket == "CB-1" {
			ledger = a
		}
	}
	if ledger.State != project.Pending {
		return fmt.Errorf("the ledger is not at rest (state=%q); cannot assert the resting case", ledger.State)
	}
	if counted[ledger.State] {
		return fmt.Errorf("a resting ledger (%q) counts toward the Running/attention totals; it must not", ledger.State)
	}
	if totals[project.Running] != 1 {
		return fmt.Errorf("Running total = %d, want 1 (the ordinary ticket only, not the ledger)", totals[project.Running])
	}
	attachText(sr, "rollup-excludes-ledger", fmt.Sprintf(
		"Running total = %d (the ordinary ORD-1 only); the Parked ledger CB-1 (state %s) is absent from every attention/Running total",
		totals[project.Running], ledger.State))
	return nil
}

// ---- validation: reject unknown ----

func givenSpecUnknownType(w *World, sr *StepRun) error {
	root, err := seedTypedTicket(w, "BAD-1", "type: codbase\n") // a typo for codebase
	if err != nil {
		return err
	}
	w.set("root", root)
	w.set("ticket", "BAD-1")
	return nil
}

func thenLoadingRejectedClearly(w *World, sr *StepRun) error {
	_, err := cbLoad(w)
	if err == nil {
		return fmt.Errorf("loading a ticket with an unknown type succeeded; want a reject-unknown error")
	}
	if !strings.Contains(err.Error(), "unknown ticket type") {
		return fmt.Errorf("error does not clearly name the unknown type: %v", err)
	}
	attachText(sr, "reject-unknown-type",
		"$ draiver recall BAD-1\nerror: "+err.Error())
	return nil
}

// ---- recall + log work on a ledger with no new flags ----

func givenLedgerWithDecision(w *World, sr *StepRun) error {
	root, err := seedTypedTicket(w, "CB-1", "type: codebase\n")
	if err != nil {
		return err
	}
	if err := cbAppend(root, "CB-1", event.Event{Type: "decision", Body: "the registry's authoritative trunk is platform-admin/devops, not main", Tags: []string{"registry"}}); err != nil {
		return err
	}
	w.set("root", root)
	w.set("ticket", "CB-1")
	return nil
}

func thenRecallAndLogWorkUnchanged(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	ticket := w.get("ticket").(string)
	// recall: fold the ledger's log exactly as `draiver recall` does — no --codebase flag.
	events, err := ticketlog.Read(root, ticket, cbAtt)
	if err != nil {
		return err
	}
	view := kb.Fold(events)
	var decision *kb.Fact
	for _, f := range view.Live() {
		if f.Type == "decision" {
			d := f
			decision = &d
		}
	}
	if decision == nil || !strings.Contains(decision.Body, "platform-admin/devops") {
		return fmt.Errorf("recall did not return the ledger's live decision: %+v", view.Live())
	}
	// log: append a further fact onto the ledger — the ordinary log path, no new flag.
	if err := cbAppend(root, ticket, event.Event{Type: "gotcha", Body: "the mirror remote lags the authoritative trunk by a day"}); err != nil {
		return fmt.Errorf("log onto a ledger failed: %w", err)
	}
	// The ledger's log stays a valid hash chain after both operations.
	res, err := audit.VerifyAttempt(root, ticket, cbAtt)
	if err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("audit failed on the ledger after recall+log: %s", res.Reason)
	}
	attachText(sr, "ledger-recall-log",
		"$ draiver recall "+ticket+"   # no new flags\n"+recallIndexLines(view.Live())+
			"\n$ draiver log "+ticket+" --type gotcha \"...\"   # the ordinary log path; audit stays green")
	return nil
}
