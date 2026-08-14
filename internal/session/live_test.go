package session

import (
	"os"
	"testing"
)

func TestAlive(t *testing.T) {
	root, ticket, id, s := newAttempt(t)

	// No session.json yet (never ran): not live.
	if Alive(root, ticket, id) {
		t.Fatal("Alive = true for an attempt with no session.json; want false")
	}

	// A recorded pid that is alive — use this test process, which is definitely
	// running — reads as live.
	if err := s.WriteIdentity(Identity{Adapter: "claude-code", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if !Alive(root, ticket, id) {
		t.Fatalf("Alive = false for pid %d (this process); want true", os.Getpid())
	}

	// A zero pid (identity written but no live process) reads as not live.
	if err := s.WriteIdentity(Identity{Adapter: "claude-code", PID: 0}); err != nil {
		t.Fatal(err)
	}
	if Alive(root, ticket, id) {
		t.Fatal("Alive = true for a zero pid; want false")
	}

	// A dead pid reads as not live. pid 2^31-1 is above the kernel's pid_max and
	// so never names a live process — a stable stand-in for a reaped session.
	if err := s.WriteIdentity(Identity{Adapter: "claude-code", PID: 1<<31 - 1}); err != nil {
		t.Fatal(err)
	}
	if Alive(root, ticket, id) {
		t.Fatal("Alive = true for a dead pid; want false")
	}
}
