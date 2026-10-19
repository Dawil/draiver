package web

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// seedCoordinatorBoard builds a Running coordinator CAP that `wants:` C1 and C2,
// woken for C1's escalation (#2) and carrying two doorbells still queued behind it
// (C1's done #3 and C2's review #2). It returns a server whose liveness stub treats
// nothing as alive — CAP has only a `created`+`woken-by` log, so it is log-Running
// and, being undesired, stays Running on the board (a coordinator card).
func seedCoordinatorBoard(t *testing.T) (*Server, store.Root) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}

	if err := root.EnsureAttemptDirs("CAP", "0001"); err != nil {
		t.Fatal(err)
	}
	writeSpecWith(t, root, "CAP", "wants:\n  - C1\n  - C2\n", "Coordinator")
	ticketlog.Append(root, "CAP", "0001", event.Event{Type: "created", Actor: "a", Body: "start"})
	// The spine's activation: woken by C1 #2 — the board's "why".
	ticketlog.Append(root, "CAP", "0001", event.Event{
		Type: project.WakeEventType, Actor: "agent:draiverctld",
		WokenChild: "C1", WokenSeq: 2, Body: "Woken by `C1` #2",
	})

	// C1: a handled escalation (#2, the activation's doorbell) + an unhandled done (#3).
	mkAttempt(t, root, "C1", "Child one", "0001")                                                    // created #1
	ticketlog.Append(root, "C1", "0001", event.Event{Type: "escalation", Actor: "a", Body: "stuck"}) // #2
	ticketlog.Append(root, "C1", "0001", event.Event{Type: "done", Actor: "a", Body: "closed"})      // #3

	// C2: an unhandled review doorbell (#2).
	mkAttempt(t, root, "C2", "Child two", "0001")                                             // created #1
	ticketlog.Append(root, "C2", "0001", event.Event{Type: "review", Actor: "a", Body: "pr"}) // #2

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.alive = func(pid int) bool { return false }
	return s, root
}

// TestBoardRendersCoordinatorCard pins AC#1+#2: a `wants:` ticket renders as a
// distinct coordinator card showing the wake reason (latest activation's child)
// and the derived queue depth (child doorbells behind the watermark).
func TestBoardRendersCoordinatorCard(t *testing.T) {
	s, _ := seedCoordinatorBoard(t)
	body := get(t, s.Handler(), "/board").Body.String()

	for _, want := range []string{
		`data-testid="card-CAP-0001"`,        // the CAP card is rendered
		`data-coordinator="true"`,            // marked distinct from an ordinary running ticket
		`card-coordinator`,                   // the distinguishing class (accent rail)
		`data-testid="coordinator-CAP-0001"`, // the wake-reason line
		`manager — on it, re: `,              // the why, human-facing
		`class="coordinator-child">C1<`,      // naming the child from the latest activation
		`data-testid="queue-CAP-0001"`,       // the derived queue pill
		`· 2 queued`,                         // C1#3 + C2#2 behind the C1#2 activation
	} {
		if !strings.Contains(body, want) {
			t.Errorf("board missing %q\n%s", want, body)
		}
	}
	// The coordinator is in the Running column, not Pending.
	if strings.Contains(body, `data-testid="count-running">0<`) {
		t.Errorf("coordinator not in Running column\n%s", body)
	}
}

// TestBoardOrdinaryRunningTicketIsNotCoordinator is the negative twin: a plain
// running ticket (no `wants:`) carries none of the coordinator markup.
func TestBoardOrdinaryRunningTicketIsNotCoordinator(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	mkAttempt(t, root, "PLAIN", "Plain running", "0001")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.alive = func(pid int) bool { return false }

	body := get(t, s.Handler(), "/board").Body.String()
	if !strings.Contains(body, `data-testid="card-PLAIN-0001"`) {
		t.Fatalf("PLAIN card not rendered\n%s", body)
	}
	for _, unwanted := range []string{`data-coordinator="true"`, `data-testid="coordinator-PLAIN-0001"`, "card-coordinator"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("ordinary ticket wrongly marked coordinator: %q\n%s", unwanted, body)
		}
	}
}

// TestCapabilityPanelShowsManagerAction pins AC#3: the capability panel surfaces the
// manager's OWN current action line (its wake reason + queue depth), not only the
// children's states.
func TestCapabilityPanelShowsManagerAction(t *testing.T) {
	s, _ := seedCoordinatorBoard(t)
	body := get(t, s.Handler(), "/ticket/CAP/0001").Body.String()

	for _, want := range []string{
		`data-testid="capability-action"`, // the manager's own line exists
		`On it, re: `,                     // its current action
		`class="coordinator-child">C1<`,   // the child it is on
		`· 2 queued`,                      // queued behind it
	} {
		if !strings.Contains(body, want) {
			t.Errorf("capability panel missing manager action %q\n%s", want, body)
		}
	}
}

// TestInlineMDRendersWaitingReasonBackticks pins the AC#4 bug fix directly: a
// WaitingReason carrying markdown backticks renders as <code>, not literal
// backticks, because the board runs it through the markdown pipeline.
func TestInlineMDRendersWaitingReasonBackticks(t *testing.T) {
	s := &Server{}
	// Build the markdown pipeline the real server uses (New wires s.md); borrow it via
	// a throwaway server over an empty root so inlineMD has a configured renderer.
	real, err := New(store.Root{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s.md = real.md

	got := string(s.inlineMD("escalated to `CAP`"))
	if strings.Contains(got, "`") {
		t.Errorf("inlineMD left literal backticks: %q", got)
	}
	if !strings.Contains(got, "<code>CAP</code>") {
		t.Errorf("inlineMD did not render backticks as code: %q", got)
	}
	if strings.HasPrefix(got, "<p>") {
		t.Errorf("inlineMD left the block <p> wrapper: %q", got)
	}
}
