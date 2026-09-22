package web

import (
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// TestBoardMovesEscalationUpUnderPreDigest pins the drvctl-050 board acceptance:
// under pre-digest, a Needs-me child whose escalation is absorbed by an enabled
// pre-digest parent renders as Pending "escalated to <P>" — off the Needs-me
// (Stuck) column and attributed to the parent — while under passthrough the same
// child stays in Stuck. Liveness is stubbed so only os.Getpid() reads as alive.
func TestBoardMovesEscalationUpUnderPreDigest(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}

	// CAP — the coordinator: wants KID, enabled, pre-digest override, and a live
	// session so it sits Running (not itself Pending), leaving KID the only card the
	// move-up can lift.
	if err := root.EnsureAttemptDirs("CAP", "0001"); err != nil {
		t.Fatal(err)
	}
	writeSpecWith(t, root, "CAP", "wants:\n  - KID\n", "Capability")
	ticketlog.Append(root, "CAP", "0001", event.Event{Type: "created", Actor: "a", Body: "start"})
	ticketlog.Append(root, "CAP", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
	ticketlog.Append(root, "CAP", "0001", event.Event{Type: "supervision", Actor: "h", Outcome: "pre-digest", Body: "pre-digest"})
	writeSession(t, root, "CAP", "0001", session.Identity{SessionID: "s-cap", PID: os.Getpid()})

	// KID — an open escalation ⇒ Needs-me on its own log.
	mkAttempt(t, root, "KID", "Child", "0001")
	ticketlog.Append(root, "KID", "0001", event.Event{Type: "escalation", Actor: "a", Body: "which endpoint?"})
	writeSession(t, root, "KID", "0001", session.Identity{SessionID: "s-kid", PID: deadPID})

	// Passthrough default (no CAP override yet would be passthrough) — but CAP carries
	// an explicit pre-digest override, so the move-up fires regardless of the default.
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.alive = func(pid int) bool { return pid == os.Getpid() }
	body := get(t, s.Handler(), "/board").Body.String()

	for _, want := range []string{
		`data-testid="attempt-link-KID-0001"`, // the KID card is rendered
		`data-testid="waiting-KID-0001"`,      // with a Pending waiting reason
		"escalated to `CAP`",                  // attributed to the parent
		`data-testid="count-stuck">0<`,        // KID left the Needs-me column
	} {
		if !strings.Contains(body, want) {
			t.Errorf("board missing %q\n%s", want, body)
		}
	}
	// KID must be Pending, not Stuck: at least one Pending card (KID), and no KID card
	// carries a Needs-me marker.
	if strings.Contains(body, `data-testid="count-pending">0<`) {
		t.Errorf("KID not folded into Pending\n%s", body)
	}
}

// TestBoardKeepsEscalationUnderPassthrough is the negative twin: with the parent in
// passthrough (the floor / default), the child stays in the human's Needs-me column.
func TestBoardKeepsEscalationUnderPassthrough(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}

	if err := root.EnsureAttemptDirs("CAP", "0001"); err != nil {
		t.Fatal(err)
	}
	writeSpecWith(t, root, "CAP", "wants:\n  - KID\n", "Capability")
	ticketlog.Append(root, "CAP", "0001", event.Event{Type: "created", Actor: "a", Body: "start"})
	ticketlog.Append(root, "CAP", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
	writeSession(t, root, "CAP", "0001", session.Identity{SessionID: "s-cap", PID: os.Getpid()})

	mkAttempt(t, root, "KID", "Child", "0001")
	ticketlog.Append(root, "KID", "0001", event.Event{Type: "escalation", Actor: "a", Body: "which endpoint?"})
	writeSession(t, root, "KID", "0001", session.Identity{SessionID: "s-kid", PID: deadPID})

	// Default supervision is passthrough, and CAP has no override ⇒ no move-up.
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.alive = func(pid int) bool { return pid == os.Getpid() }
	body := get(t, s.Handler(), "/board").Body.String()

	if !strings.Contains(body, `data-testid="count-stuck">1<`) {
		t.Errorf("passthrough child not in Needs-me column\n%s", body)
	}
	if strings.Contains(body, "escalated to") {
		t.Errorf("passthrough child unexpectedly attributed to a parent\n%s", body)
	}
}
