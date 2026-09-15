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

// TestEnableButtonHiddenForViaParentActiveChild pins drvweb-022: a child attempt
// activated through its parent's wants: edge is Running, Desired, and Live but is
// never *directly* Enabled (supervision flows through the parent, so it gets no
// enable event of its own). The green play button must not leak onto that
// already-running card, and — because the write path shares the gate — its enable
// POST must be a no-op. Without the runtime-bit fold, canEnable saw Running &&
// !Enabled → true and the button both rendered and, on click, appended a real
// enable, pinning the child's supervision independently of the parent edge.
func TestEnableButtonHiddenForViaParentActiveChild(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}

	// PARENT wants: CHILD, and is itself enabled + Running ⇒ a desiredness source
	// that pulls CHILD in down the wants: edge (DeriveDesired).
	if err := root.EnsureAttemptDirs("PARENT", "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath("PARENT"),
		[]byte("---\nid: PARENT\ntitle: Coordinator\nwants:\n  - CHILD\n---\n\n# Coordinator"), 0o644)
	ticketlog.Append(root, "PARENT", "0001", event.Event{Type: "created", Actor: "a", Body: "start"})
	ticketlog.Append(root, "PARENT", "0001", event.Event{Type: "enable", Actor: "h", Body: "on"})

	// CHILD — Running, NOT directly enabled, with a live agent (os.Getpid reads as
	// alive under the stub). Enabled-via-parent ⇒ Desired; live pid ⇒ Live. So its
	// Control state is Running and it is genuinely already being worked, not parked.
	mkAttempt(t, root, "CHILD", "Worker", "0001")
	writeSession(t, root, "CHILD", "0001", session.Identity{SessionID: "s-child", PID: os.Getpid()})

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.alive = func(pid int) bool { return pid == os.Getpid() }
	h := s.Handler()

	// The child's card sits in Running (it is live) yet shows NO play button: it is
	// already supervised through its parent, not a parked card to opt in.
	board := get(t, h, "/board").Body.String()
	if !strings.Contains(board, `data-testid="card-CHILD-0001"`) {
		t.Fatalf("child card missing from board:\n%s", board)
	}
	if strings.Contains(board, `data-testid="enable-btn-CHILD-0001"`) {
		t.Errorf("a via-parent-active child must show no enable button:\n%s", board)
	}

	// Its enable POST is a no-op: handleEnable re-derives Live/Desired before the
	// canEnable gate, so it does not convert an enabled-via-parent child into a
	// directly enabled attempt.
	rr := post(t, h, "/ticket/CHILD/0001/enable")
	if rr.Code != 200 {
		t.Fatalf("POST enable = %d, want 200", rr.Code)
	}
	if n := len(enableEvents(t, root, "CHILD", "0001")); n != 0 {
		t.Errorf("enable POST for a via-parent-active child must write nothing; enable events = %d, want 0", n)
	}
	// The re-rendered card still carries no button, so a subsequent poll matches.
	if strings.Contains(rr.Body.String(), `data-testid="enable-btn-CHILD-0001"`) {
		t.Errorf("enable POST response must not resurrect the button:\n%s", rr.Body.String())
	}
}
