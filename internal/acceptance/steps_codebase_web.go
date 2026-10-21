package acceptance

import (
	"fmt"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/web"
)

// These steps exercise drv-039's web rendering of codebase ledgers end to end over a
// real internal/web server: a `type: codebase` ledger renders with no play/enable
// control and no Done affordance, rests Parked, and lives in its own view — a toggle
// keeps it off the code-ticket board until it hits an attention state (an open
// escalation surfaces it through the normal Stuck path). Code tickets are untouched.
// Asserting on the served HTML is asserting on the operator-facing board itself.

func init() {
	drv039 := map[string]StepFunc{
		"a board with a resting codebase ledger and an ordinary ticket":           givenLedgerAndOrdinaryBoard,
		"the ledger card carries no play control and no Done affordance":          thenLedgerCardNoPlayNoDone,
		"the ledger card is marked a ledger resting Parked":                       thenLedgerCardMarkedParked,
		"the code view shows the ordinary ticket but not the resting ledger":      thenCodeViewHidesRestingLedger,
		"the ledgers view shows the ledger but not the ordinary ticket":           thenLedgersViewShowsOnlyLedgers,
		"a board with a codebase ledger that has an open escalation":              givenLedgerEscalationBoard,
		"the code view surfaces the ledger in the attention path marked a ledger": thenCodeViewSurfacesEscalatedLedger,
		"the ledgers toggle shows the ledger needs attention":                     thenLedgersToggleShowsAttention,
		"the ordinary ticket on the code view still shows its play control":       thenOrdinaryKeepsPlayControl,
	}
	for text, fn := range drv039 {
		registry[text] = fn
	}
}

// seedLedgerBoard stands up a real board over a fresh data root carrying the given
// typed tickets (reusing addTypedTicket / cbAppend from steps_codebase.go) and
// returns a *board pointing at the live internal/web server. Everything is
// registered for cleanup on the World.
func seedLedgerBoard(w *World, build func(root store.Root, repo string) error) (*board, error) {
	dir, err := os.MkdirTemp("", "acc-cbweb-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	root := store.Root{Dir: dir}
	if err := build(root, dir); err != nil {
		return nil, err
	}
	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	return &board{root: root, wd: dir, srv: ts}, nil
}

// givenLedgerAndOrdinaryBoard: a resting codebase ledger (LED-1, Parked) beside an
// ordinary code ticket (ORD-1, Running).
func givenLedgerAndOrdinaryBoard(w *World, sr *StepRun) error {
	b, err := seedLedgerBoard(w, func(root store.Root, repo string) error {
		if err := addTypedTicket(root, repo, "LED-1", "type: codebase\n"); err != nil {
			return err
		}
		return addTypedTicket(root, repo, "ORD-1", "")
	})
	if err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

// givenLedgerEscalationBoard: a codebase ledger (LED-1) with an open escalation →
// Needs-me, beside an ordinary ticket, so the code view's attention path is exercised.
func givenLedgerEscalationBoard(w *World, sr *StepRun) error {
	b, err := seedLedgerBoard(w, func(root store.Root, repo string) error {
		if err := addTypedTicket(root, repo, "LED-1", "type: codebase\n"); err != nil {
			return err
		}
		if err := cbAppend(root, "LED-1", event.Event{Type: "escalation", Body: "which trunk is authoritative for this codebase?"}); err != nil {
			return err
		}
		return addTypedTicket(root, repo, "ORD-1", "")
	})
	if err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

func thenLedgerCardNoPlayNoDone(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	// The ledgers view renders only ledgers, so any play/enable button or Done
	// (accept) affordance in the whole region would be on a ledger card.
	html, err := getBody(b.url("/board?view=ledgers"))
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="card-LED-1-0001"`) {
		return fmt.Errorf("the ledger card is not on the ledgers view")
	}
	if strings.Contains(html, `data-testid="enable-btn-LED-1-0001"`) {
		return fmt.Errorf("the ledger card carries a play/enable control; a ledger is never enabled")
	}
	// A Done affordance is the green accept tick (data-outcome=\"accepted\"), rendered
	// only on a Done card. A ledger is never Done, so it must not appear at all.
	if strings.Contains(html, `data-outcome="accepted"`) {
		return fmt.Errorf("the ledger card carries a Done (accept) affordance; a ledger never reaches Done")
	}
	attachText(sr, "ledger-no-play-no-done",
		"the codebase ledger LED-1 renders with no play/enable button and no Done (accept) affordance")
	// Evidence: screenshot the full styled ledgers view (the `/` page, not the bare
	// `/board` htmx partial — the partial has no <head>, so no stylesheet) showing the
	// ledger card as the operator sees it, with no play control and no Done tick.
	return shoot(w, sr, "/?view=ledgers", "ledger-no-play-no-done", true, `[data-testid="card-LED-1-0001"]`, "")
}

func thenLedgerCardMarkedParked(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/board?view=ledgers"))
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="ledger-badge-LED-1-0001"`) {
		return fmt.Errorf("the ledger card is not marked a ledger")
	}
	if !strings.Contains(html, "parked — codebase ledger") {
		return fmt.Errorf("the ledger card does not show the Parked resting state")
	}
	attachText(sr, "ledger-marked-parked",
		"the ledger card carries a `ledger` marker and reads `parked — codebase ledger`")
	return nil
}

func thenCodeViewHidesRestingLedger(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/board?view=code"))
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="attempt-link-ORD-1-0001"`) {
		return fmt.Errorf("the ordinary ticket is absent from the code view")
	}
	if strings.Contains(html, `data-testid="card-LED-1-0001"`) {
		return fmt.Errorf("a resting ledger appears on the code view; it must stay in the ledgers view until it needs attention")
	}
	attachText(sr, "code-view-hides-ledger",
		"the code view shows ORD-1 and omits the resting ledger LED-1")
	// Evidence: the full styled code view — the ordinary ticket present, the resting
	// ledger absent (it stays in the ledgers view until it needs attention).
	return shoot(w, sr, "/?view=code", "code-view-hides-ledger", true, `[data-testid="attempt-link-ORD-1-0001"]`, "")
}

func thenLedgersViewShowsOnlyLedgers(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/board?view=ledgers"))
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="card-LED-1-0001"`) {
		return fmt.Errorf("the ledger is absent from the ledgers view")
	}
	if strings.Contains(html, `data-testid="card-ORD-1-0001"`) {
		return fmt.Errorf("the ordinary ticket appears on the ledgers view; it must not")
	}
	attachText(sr, "ledgers-view-only-ledgers",
		"the ledgers view shows LED-1 and omits the ordinary ticket ORD-1")
	return nil
}

func thenCodeViewSurfacesEscalatedLedger(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/board?view=code"))
	if err != nil {
		return err
	}
	// The Stuck column holds the ledger's card, marked a ledger — the normal
	// Needs-me attention path, not a ledger-specific one.
	stuck, ok := sliceBetween(html, `data-testid="col-stuck"`, `data-testid="col-review"`)
	if !ok || !strings.Contains(stuck, `data-testid="card-LED-1-0001"`) {
		return fmt.Errorf("the open-escalation ledger did not surface in the code view's Stuck column")
	}
	if !strings.Contains(html, `data-testid="ledger-badge-LED-1-0001"`) {
		return fmt.Errorf("the surfaced ledger is not marked a ledger")
	}
	attachText(sr, "code-view-surfaces-escalated-ledger",
		"an open-escalation ledger surfaces in the code view's Stuck column, marked a ledger")
	// Evidence: the full styled code view with the escalated ledger surfaced into the
	// Stuck column and the Ledgers toggle lit — the normal Needs-me attention path.
	return shoot(w, sr, "/?view=code", "code-view-surfaces-escalated-ledger", true, `[data-testid="card-LED-1-0001"]`, "")
}

func thenLedgersToggleShowsAttention(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/board?view=code"))
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="ledgers-attention"`) {
		return fmt.Errorf("the ledgers toggle shows no attention indicator for a ledger that needs the operator")
	}
	attachText(sr, "ledgers-toggle-attention",
		"the Ledgers toggle carries an attention dot while a ledger is in Needs-me, even from the code view")
	return nil
}

func thenOrdinaryKeepsPlayControl(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/board?view=code"))
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="enable-btn-ORD-1-0001"`) {
		return fmt.Errorf("the ordinary ticket lost its play/enable control (a regression)")
	}
	attachText(sr, "ordinary-keeps-play",
		"the ordinary code ticket ORD-1 still renders its play/enable control — no regression")
	// Evidence: the full styled code view — the ordinary ticket keeps its play/enable
	// control exactly as today (no regression).
	return shoot(w, sr, "/?view=code", "ordinary-keeps-play", true, `[data-testid="enable-btn-ORD-1-0001"]`, "")
}
