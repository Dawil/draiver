package acceptance

import (
	"fmt"
	"os"
	"strings"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// ---- manager-signal/board.feature (drvweb-023): the coordinator card ----

func init() {
	drvweb023 := map[string]StepFunc{
		"a board with a woken coordinator and queued child doorbells":                  givenWokenCoordinatorBoard,
		"the board renders the coordinator card marked distinct from a running ticket": thenBoardMarksCoordinatorDistinct,
		"the coordinator card shows the wake reason and the derived queue depth":       thenCoordinatorCardShowsWakeAndQueue,
		"the capability panel shows the manager's own current action line":             thenCapabilityShowsManagerAction,
		"a board with a child escalated up to a pre-digest coordinator":                givenEscalatedToCoordinatorBoard,
		"the board renders the escalated reason as markdown with no literal backticks": thenEscalatedReasonRendersMarkdown,
	}
	for text, fn := range drvweb023 {
		registry[text] = fn
	}
}

// writeCoordSpec writes a ticket's spec.md with an explicit frontmatter block onto
// the board's root, so a scenario can author `wants:` edges the default seedBoard
// attempt does not carry. frontmatter is the raw YAML body between the id/title and
// the closing fence (e.g. "wants:\n  - C1\n").
func writeCoordSpec(root store.Root, id, frontmatter, title string) error {
	body := "---\nid: " + id + "\ntitle: " + title + "\n" + frontmatter + "---\n\n# " + title + "\n"
	return os.WriteFile(root.SpecPath(id), []byte(body), 0o644)
}

// givenWokenCoordinatorBoard seeds, onto a real board, a Running coordinator CAP
// that `wants:` C1 and C2, woken by C1's escalation (#2) — the spine's `woken-by`
// activation — with two child doorbells still queued behind it (C1's done #3 and
// C2's review #2). CAP's own log is only created+woken-by, so it is log-Running and,
// being undesired, stays a Running coordinator card (not Pending).
func givenWokenCoordinatorBoard(w *World, sr *StepRun) error {
	b, err := seedBoard(w, greenPyramid)
	if err != nil {
		return err
	}
	if err := b.root.EnsureAttemptDirs("CAP", "0001"); err != nil {
		return err
	}
	if err := writeCoordSpec(b.root, "CAP", "wants:\n  - C1\n  - C2\n", "Coordinator"); err != nil {
		return err
	}
	if _, err := ticketlog.Append(b.root, "CAP", "0001", event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return err
	}
	if _, err := ticketlog.Append(b.root, "CAP", "0001", event.Event{
		Type: project.WakeEventType, Actor: "agent:draiverctld",
		WokenChild: "C1", WokenSeq: 2, Body: "Woken by `C1` #2 — a child `escalation` lifecycle doorbell.",
	}); err != nil {
		return err
	}

	// C1: a handled escalation (#2 — the activation's doorbell) and an unhandled done (#3).
	if err := seedChildDoorbells(b.root, "C1", "Child one", "escalation", "done"); err != nil {
		return err
	}
	// C2: an unhandled review doorbell (#2).
	if err := seedChildDoorbells(b.root, "C2", "Child two", "review"); err != nil {
		return err
	}

	w.set("board", b)
	return nil
}

// seedChildDoorbells creates a child attempt (created #1) and appends one event per
// doorbell type in order, so the seqs are deterministic (#2, #3, …).
func seedChildDoorbells(root store.Root, id, title string, types ...string) error {
	if err := root.EnsureAttemptDirs(id, "0001"); err != nil {
		return err
	}
	if err := writeCoordSpec(root, id, "", title); err != nil {
		return err
	}
	if _, err := ticketlog.Append(root, id, "0001", event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return err
	}
	for _, typ := range types {
		if _, err := ticketlog.Append(root, id, "0001", event.Event{Type: typ, Actor: "agent:acceptance", Body: typ}); err != nil {
			return err
		}
	}
	return nil
}

func thenBoardMarksCoordinatorDistinct(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/board"))
	if err != nil {
		return err
	}
	for _, want := range []string{
		`data-testid="card-CAP-0001"`,
		`data-coordinator="true"`,
		`card-coordinator`,
	} {
		if !strings.Contains(html, want) {
			return fmt.Errorf("board does not mark CAP a distinct coordinator card (missing %q)", want)
		}
	}
	return shoot(w, sr, "/board", "coordinator-card", true, `[data-testid="coordinator-CAP-0001"]`, "")
}

func thenCoordinatorCardShowsWakeAndQueue(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/board"))
	if err != nil {
		return err
	}
	for _, want := range []string{
		`data-testid="coordinator-CAP-0001"`,
		"manager — on it, re: ",
		`class="coordinator-child">C1<`,
		`data-testid="queue-CAP-0001"`,
		"· 2 queued",
	} {
		if !strings.Contains(html, want) {
			return fmt.Errorf("coordinator card missing wake reason / queue depth (%q)", want)
		}
	}
	return nil
}

func thenCapabilityShowsManagerAction(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/ticket/CAP/0001"))
	if err != nil {
		return err
	}
	for _, want := range []string{
		`data-testid="capability-action"`,
		"On it, re: ",
		`class="coordinator-child">C1<`,
		"· 2 queued",
	} {
		if !strings.Contains(html, want) {
			return fmt.Errorf("capability panel does not show the manager's own action line (%q)", want)
		}
	}
	return shoot(w, sr, "/ticket/CAP/0001", "capability-action", true, `[data-testid="capability-action"]`, "")
}

// givenEscalatedToCoordinatorBoard seeds the drvctl-050 move-up: a pre-digest
// coordinator CAP `wants:` KID, and KID carries an open escalation, so KID renders
// Pending "escalated to `CAP`" — the WaitingReason whose backticks the board used
// to show literally (the drvweb-023 bug this fixes). CAP is log-Running (enabled +
// supervision), the predicate DeriveEscalated requires.
func givenEscalatedToCoordinatorBoard(w *World, sr *StepRun) error {
	b, err := seedBoard(w, greenPyramid)
	if err != nil {
		return err
	}
	if err := b.root.EnsureAttemptDirs("CAP", "0001"); err != nil {
		return err
	}
	if err := writeCoordSpec(b.root, "CAP", "wants:\n  - KID\n", "Coordinator"); err != nil {
		return err
	}
	for _, e := range []event.Event{
		{Type: "created", Actor: "agent:acceptance", Body: "start"},
		{Type: "enable", Actor: "human:acceptance", Body: "on"},
		{Type: "supervision", Actor: "human:acceptance", Outcome: "pre-digest", Body: "pre-digest"},
	} {
		if _, err := ticketlog.Append(b.root, "CAP", "0001", e); err != nil {
			return err
		}
	}

	if err := b.root.EnsureAttemptDirs("KID", "0001"); err != nil {
		return err
	}
	if err := writeCoordSpec(b.root, "KID", "", "Child"); err != nil {
		return err
	}
	for _, e := range []event.Event{
		{Type: "created", Actor: "agent:acceptance", Body: "start"},
		{Type: "escalation", Actor: "agent:acceptance", Body: "which endpoint?"},
	} {
		if _, err := ticketlog.Append(b.root, "KID", "0001", e); err != nil {
			return err
		}
	}

	w.set("board", b)
	return nil
}

func thenEscalatedReasonRendersMarkdown(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/board"))
	if err != nil {
		return err
	}
	if !strings.Contains(html, "escalated to <code>CAP</code>") {
		return fmt.Errorf("escalated reason not rendered as markdown code (want `escalated to <code>CAP</code>`)")
	}
	if strings.Contains(html, "escalated to `CAP`") {
		return fmt.Errorf("escalated reason still shows literal backticks — the drvweb-023 bug is not fixed")
	}
	return shoot(w, sr, "/board", "escalated-reason-markdown", true, `[data-testid="waiting-KID-0001"]`, "")
}
