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

// TestBoardRendersPendingTier pins the drvweb-015 acceptance: a desired,
// log-Running attempt with no live agent renders in the Pending sub-section
// (nested under Running, decision #2) with its "waiting on X" reason, while a
// Running attempt with a live agent and a Needs-me attempt do not fold into
// Pending. Liveness is stubbed so only os.Getpid() reads as alive.
func TestBoardRendersPendingTier(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}

	// SRC — Running, not enabled: E2E's after: predecessor, still short of Review.
	mkAttempt(t, root, "SRC", "Source", "0001")

	// E2E — enabled + Running + dead pid ⇒ desired with no live agent ⇒ Pending. Its
	// spec is admitted after SRC, which is only Running, so the gate is shut and the
	// reason is "waiting on SRC".
	if err := root.EnsureAttemptDirs("E2E", "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath("E2E"),
		[]byte("---\nid: E2E\ntitle: E2E tests\nafter:\n  - SRC\n---\n\n# E2E tests"), 0o644)
	ticketlog.Append(root, "E2E", "0001", event.Event{Type: "created", Actor: "a", Body: "start"})
	ticketlog.Append(root, "E2E", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
	writeSession(t, root, "E2E", "0001", session.Identity{SessionID: "s-e2e", PID: deadPID})

	// LIVE — enabled + Running + live pid ⇒ a live agent is working: Running, not
	// Pending (liveness wins over the Pending overlay).
	mkAttempt(t, root, "LIVE", "Live one", "0001")
	ticketlog.Append(root, "LIVE", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
	writeSession(t, root, "LIVE", "0001", session.Identity{SessionID: "s-live", PID: os.Getpid()})

	// STUCK — open escalation ⇒ Needs-me; enabled + dead pid must NOT become Pending
	// (Control passes every non-Running state straight through).
	mkAttempt(t, root, "STUCK", "Blocked", "0001")
	ticketlog.Append(root, "STUCK", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})
	ticketlog.Append(root, "STUCK", "0001", event.Event{Type: "escalation", Actor: "a", Body: "which?"})
	writeSession(t, root, "STUCK", "0001", session.Identity{SessionID: "s-stuck", PID: deadPID})

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.alive = func(pid int) bool { return pid == os.Getpid() }
	body := get(t, s.Handler(), "/board").Body.String()

	for _, want := range []string{
		`data-testid="col-pending"`,                      // the Pending sub-section
		`data-testid="count-pending">1<`,                 // exactly one attempt: E2E
		`data-testid="attempt-link-E2E-0001"`,            // the Pending card
		`data-testid="waiting-E2E-0001">waiting on SRC<`, // its waiting reason
	} {
		if !strings.Contains(body, want) {
			t.Errorf("board missing %q\n%s", want, body)
		}
	}
	// Running holds SRC (undesired) + LIVE (live agent); E2E and STUCK are elsewhere.
	if !strings.Contains(body, `data-testid="count-running">2<`) {
		t.Errorf("running count want 2 (SRC + LIVE)\n%s", body)
	}
	// A Needs-me attempt is not folded into Pending — it stays in Stuck.
	if !strings.Contains(body, `data-testid="count-stuck">1<`) {
		t.Errorf("stuck count want 1 (STUCK)\n%s", body)
	}
	// Only a Pending card carries a waiting reason; the live/undesired/blocked cards
	// render none.
	for _, none := range []string{"waiting-LIVE-0001", "waiting-SRC-0001", "waiting-STUCK-0001"} {
		if strings.Contains(body, none) {
			t.Errorf("non-Pending card unexpectedly carries a waiting reason: %q", none)
		}
	}
}
