package acceptance

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/completion"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/wake"
)

// These steps exercise drvctl-055's park-pending-own-async-work contract end to end
// over a real on-disk, hash-chained attempt log — the same primitives the live
// daemon drives: `internal/wake`.Pending (is a detached test in flight a parkable
// suspend point?) and `internal/completion`.Gate (does a handoff-less turn-end with
// a live async job auto-park rather than escalate a human?). Asserting on those
// engines is asserting on the agent-and-human-facing behaviour: a parked worker
// shows Pending, never a false Needs-me escalation. The reconciler's own
// `woken-by` authoring that re-admits the worker when the result lands is covered
// at the unit rung (internal/reconcile/async_jobs_test.go).

const (
	asyncTicket = "ASY-1"
	asyncAtt    = "0001"
)

// seedAsyncData makes a throwaway data root with one leaf-worker attempt and a
// spec, registered for cleanup on the World, and returns it.
func seedAsyncData(w *World) (store.Root, error) {
	dir, err := os.MkdirTemp("", "acc-async-")
	if err != nil {
		return store.Root{}, err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	root := store.Root{Dir: dir}
	if err := root.EnsureAttemptDirs(asyncTicket, asyncAtt); err != nil {
		return store.Root{}, err
	}
	spec := "---\nid: " + asyncTicket + "\ntitle: park pending own async work\n---\n\n# park pending own async work\n"
	if err := os.WriteFile(root.SpecPath(asyncTicket), []byte(spec), 0o644); err != nil {
		return store.Root{}, err
	}
	return root, nil
}

func appendAsync(root store.Root, e event.Event) (event.Event, error) {
	e.Actor = "agent:acceptance"
	return ticketlog.Append(root, asyncTicket, asyncAtt, e)
}

// asyncSource returns the AsyncJob wake source among the attempt's pending sources,
// or nil when none — the read the park primitive and completion gate both make.
func asyncSource(root store.Root) (*wake.Source, error) {
	srcs, err := wake.Pending(root, asyncTicket, asyncAtt)
	if err != nil {
		return nil, err
	}
	for i := range srcs {
		if srcs[i].Kind == wake.AsyncJob {
			return &srcs[i], nil
		}
	}
	return nil, nil
}

// ---- park-async.feature: a detached test in flight is a suspend point ----

func givenLaunchedDetachedTest(w *World, sr *StepRun) error {
	root, err := seedAsyncData(w)
	if err != nil {
		return err
	}
	if _, err := appendAsync(root, event.Event{Type: "created", Body: "start"}); err != nil {
		return err
	}
	// What `draiver test --detach && draiver park` writes to its own log: the launch
	// marker that registers the async-job wake source, then the park behind it.
	if _, err := appendAsync(root, event.Event{Type: "async-job", Rung: "unit", Body: "launched detached `draiver test`"}); err != nil {
		return err
	}
	if _, err := appendAsync(root, event.Event{Type: "park", Body: "parked behind the detached test"}); err != nil {
		return err
	}
	w.set("root", root)
	return nil
}

func thenHasAsyncSource(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	src, err := asyncSource(root)
	if err != nil {
		return err
	}
	if src == nil {
		return fmt.Errorf("a launched-but-unanswered detached test must yield an async-job wake source, got none")
	}
	attachText(sr, "pending-async-source",
		"$ draiver park "+asyncTicket+"\n# wake.Pending reports a live async-job source:\n  kind: "+string(src.Kind)+"\n  desc: "+src.Desc)
	return nil
}

// thenSuspendNotEscalation asserts the agent-facing consequence: a parkable worker
// is a Pending suspend, never a human escalation. Proven by the park hand-off being
// legitimate (a wake source exists) and no escalation sitting on the log.
func thenSuspendNotEscalation(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	events, err := ticketlog.Read(root, asyncTicket, asyncAtt)
	if err != nil {
		return err
	}
	for _, e := range events {
		if e.Type == "escalation" {
			return fmt.Errorf("a worker parked behind a detached test must not escalate a human, found escalation #%d", e.Seq)
		}
	}
	src, err := asyncSource(root)
	if err != nil {
		return err
	}
	if src == nil {
		return fmt.Errorf("the park has no wake source — it would sleep forever rather than suspend to Pending")
	}
	return nil
}

func givenResultLanded(w *World, sr *StepRun) error {
	if err := givenLaunchedDetachedTest(w, sr); err != nil {
		return err
	}
	root := w.get("root").(store.Root)
	// The detached runner lands its verdict on the worker's own log — the doorbell.
	if _, err := appendAsync(root, event.Event{Type: "test-result", Outcome: "pass", Rung: "unit", Commit: "deadbeef", Body: "green"}); err != nil {
		return err
	}
	return nil
}

func thenSourceAnswered(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	src, err := asyncSource(root)
	if err != nil {
		return err
	}
	if src != nil {
		return fmt.Errorf("the landed result must answer the async-job source, but it is still pending: %s", src.Desc)
	}
	attachText(sr, "async-source-answered",
		"# a `test-result` landed after the launch → wake.Pending no longer reports an\n"+
			"# async-job source, so the reconciler re-admits (path-activates) the worker\n"+
			"# to claim review (green) or iterate (red).\n$ draiver recall "+asyncTicket+" --type test-result\n  #4 test-result [pass] — green")
	return nil
}

// ---- completion gate: auto-park a handoff-less turn-end with a live async job ----

func givenTurnEndWithLiveJobNoHandoff(w *World, sr *StepRun) error {
	root, err := seedAsyncData(w)
	if err != nil {
		return err
	}
	if _, err := appendAsync(root, event.Event{Type: "created", Body: "start"}); err != nil {
		return err
	}
	// The agent launched a detached test but ended its turn without the `&& draiver
	// park` (and without review/escalation) — a handoff-less turn-end with a live job.
	if _, err := appendAsync(root, event.Event{Type: "async-job", Rung: "unit", Body: "launched detached `draiver test`"}); err != nil {
		return err
	}
	w.set("root", root)
	return nil
}

func whenCompletionGateConsiders(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	// nil nudge/halt: with nothing to deliver the gate decides the suspend/escalate
	// fork immediately — the real daemon wiring nudges first, which only delays, not
	// changes, the decision this scenario pins.
	g, err := completion.New(root, asyncTicket, asyncAtt, "agent:acceptance", nil, nil)
	if err != nil {
		return err
	}
	out, err := g.Consider(context.Background(), agent.Event{Kind: agent.EventTurnEnd, Turn: "success"})
	if err != nil {
		return err
	}
	w.set("outcome", out)
	return nil
}

func thenGateParksNotEscalates(w *World, sr *StepRun) error {
	out := w.get("outcome").(completion.Outcome)
	if out != completion.Parked {
		return fmt.Errorf("completion gate outcome = %v, want Parked (a live async job suspends, never escalates)", out)
	}
	root := w.get("root").(store.Root)
	events, err := ticketlog.Read(root, asyncTicket, asyncAtt)
	if err != nil {
		return err
	}
	for _, e := range events {
		if e.Type == "escalation" {
			return fmt.Errorf("the gate must not manufacture a human escalation for a live async job, found #%d", e.Seq)
		}
	}
	attachText(sr, "completion-auto-park",
		"turn ended at success with a detached test running and no `review`/`park`/`escalation` filed.\n"+
			"completion gate outcome: Parked (not Escalated) — no false Needs-me lands on the human's board.")
	return nil
}

func thenParkRecorded(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	events, err := ticketlog.Read(root, asyncTicket, asyncAtt)
	if err != nil {
		return err
	}
	var park *event.Event
	for i := range events {
		if events[i].Type == "park" {
			park = &events[i]
		}
	}
	if park == nil {
		return fmt.Errorf("the gate must author the park the agent omitted so the suspend is durable, found none")
	}
	if !strings.Contains(strings.ToLower(park.Body), "auto-park") {
		return fmt.Errorf("the recorded park does not read as gate-authored: %q", park.Body)
	}
	return nil
}

// ---- a synchronous test result is not an async doorbell ----

func givenSyncResultNoLaunch(w *World, sr *StepRun) error {
	root, err := seedAsyncData(w)
	if err != nil {
		return err
	}
	if _, err := appendAsync(root, event.Event{Type: "created", Body: "start"}); err != nil {
		return err
	}
	// A plain synchronous `draiver test --log` green record, with no async-job launch
	// before it — not a detached job, so not an async wake source.
	if _, err := appendAsync(root, event.Event{Type: "test-result", Rung: "unit", Commit: "cafe", Body: "green (sync --log)"}); err != nil {
		return err
	}
	w.set("root", root)
	return nil
}

// ---- park --clear: the operator inverse that force-resolves a stuck source (drv-031) ----

// whenOperatorClears models `draiver park --clear` of an async-job source: it reads
// the pending source and lands the exact terminal fail result the command writes —
// wake.AsyncLaunchFailResult, the shared "answer launch N" discipline — stamped with
// operator (human) provenance rather than an agent verdict.
func whenOperatorClears(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	src, err := asyncSource(root)
	if err != nil {
		return err
	}
	if src == nil {
		return fmt.Errorf("precondition: expected a pending async-job source to clear, found none")
	}
	cleared := wake.AsyncLaunchFailResult(src.Ref, src.Rung,
		"Operator `park --clear`: force-resolved the pending async-job wake source. No climb ran.")
	cleared.Actor = "human:operator"
	if _, err := ticketlog.Append(root, asyncTicket, asyncAtt, cleared); err != nil {
		return err
	}
	return nil
}

// thenClearedResultGateSafe proves the cleared result can never satisfy the review
// gate: it is the exact rule cmd/review.go folds — only results with Outcome != "fail"
// map onto the pyramid's green set — so a required rung sees nothing green at HEAD and
// the gate stays shut. The operator recovered the worker without accidentally passing
// review.
func thenClearedResultGateSafe(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	events, err := ticketlog.Read(root, asyncTicket, asyncAtt)
	if err != nil {
		return err
	}

	var cleared *event.Event
	var green []pyramid.Result
	for i := range events {
		e := events[i]
		if e.Type != "test-result" {
			continue
		}
		cleared = &events[i]
		// cmd/review.go:140 — a failing result never maps onto the gate's green set.
		if e.Outcome != "fail" {
			green = append(green, pyramid.Result{Rung: e.Rung, Commit: e.Commit})
		}
	}
	if cleared == nil || cleared.Outcome != "fail" {
		return fmt.Errorf("the cleared result must carry Outcome=fail, got %+v", cleared)
	}

	// Fold the (empty) green set through the real review-gate helper against a pyramid
	// that requires the unit rung: outstanding must be non-empty ⇒ the gate refuses.
	p := &pyramid.Pyramid{Levels: []pyramid.Level{{Name: "unit", Run: "true"}}}
	_, outstanding := p.RequiredStatus(green, cleared.Commit)
	if len(outstanding) == 0 {
		return fmt.Errorf("a fail-only result satisfied the review gate — a cleared source must never read as green")
	}
	attachText(sr, "clear-gate-safe",
		"$ draiver park --clear "+asyncTicket+"\n"+
			"# lands a terminal test-result answering the launch:\n"+
			"  outcome: fail   refs: [<launch>]   actor: human:operator\n"+
			"# review-gate fold (cmd/review.go) excludes Outcome=fail → required rung still outstanding:\n"+
			"  "+outstanding[0].Name+" (not green at HEAD) — the gate stays shut.")
	return nil
}

func thenNoAsyncSource(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	src, err := asyncSource(root)
	if err != nil {
		return err
	}
	if src != nil {
		return fmt.Errorf("a sync test-result with no detached launch must not register an async wake source, got: %s", src.Desc)
	}
	attachText(sr, "sync-result-no-source",
		"# a synchronous `test --log` result answers no detached launch, so wake.Pending\n"+
			"# reports no async-job source — the worker is a plainly-working leaf, admitted normally.\n$ draiver recall "+asyncTicket+" --type test-result\n  #2 test-result — green (sync --log)")
	return nil
}
