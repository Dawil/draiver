package agentmsg

import (
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
)

func TestUnreadCountsOnlyMarkedAboveFloor(t *testing.T) {
	evs := []event.Event{
		{Seq: 1, Type: "note", To: ToAgent},                     // before session
		{Seq: 2, Type: "note", Actor: "human:david"},            // spine note, unmarked
		{Seq: 3, Type: "decision", To: ToAgent},                 // unread
		{Seq: 4, Type: "note", Actor: "human:webui"},            // restart flush, unmarked
		{Seq: 5, Type: "gotcha", To: ToAgent, Actor: "agent:x"}, // marked counts regardless of actor
	}
	got := Unread(evs, 1, 0)
	if len(got) != 2 || got[0].Seq != 3 || got[1].Seq != 5 {
		t.Fatalf("Unread(watermark=1) = %+v", got)
	}
	if got := Unread(evs, 1, 3); len(got) != 1 || got[0].Seq != 5 {
		t.Fatalf("cursor 3 should leave only #5: %+v", got)
	}
}

func TestFromEnv(t *testing.T) {
	env := map[string]string{EnvTicket: "T", EnvAttempt: "0001", EnvSeq: "7"}
	s, ok := FromEnv(func(k string) string { return env[k] })
	if !ok || s != (Session{"T", "0001", 7}) {
		t.Fatalf("FromEnv = %+v %v", s, ok)
	}
	for _, k := range []string{EnvTicket, EnvAttempt, EnvSeq} {
		e := map[string]string{EnvTicket: "T", EnvAttempt: "0001", EnvSeq: "7"}
		delete(e, k)
		if _, ok := FromEnv(func(k string) string { return e[k] }); ok {
			t.Fatalf("missing %s must mean not-in-session", k)
		}
	}
	env[EnvSeq] = "x"
	if _, ok := FromEnv(func(k string) string { return env[k] }); ok {
		t.Fatal("malformed seq must mean not-in-session")
	}
}

func TestCursorIsMonotonicAndWatermarkRoundTrips(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if n, err := ReadCursor(root, "T", "0001"); err != nil || n != 0 {
		t.Fatalf("missing cursor = %d, %v", n, err)
	}
	_ = AdvanceCursor(root, "T", "0001", 9)
	_ = AdvanceCursor(root, "T", "0001", 4)
	if n, _ := ReadCursor(root, "T", "0001"); n != 9 {
		t.Fatalf("cursor moved backwards: %d", n)
	}
	if err := WriteWatermark(root, "T", "0001", 12); err != nil {
		t.Fatal(err)
	}
	if n, _ := ReadWatermark(root, "T", "0001"); n != 12 {
		t.Fatalf("watermark = %d", n)
	}
	// A corrupt cursor is an error to the reader (the notice treats it as fail-silent).
	if err := os.WriteFile(cursorPath(root, "T", "0001"), []byte("{garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCursor(root, "T", "0001"); err == nil {
		t.Fatal("corrupt cursor should error")
	}
}

func TestNoticeIsCountOnly(t *testing.T) {
	if n := Notice(1); !strings.Contains(n, "1 unread message ") || !strings.Contains(n, "draiver messages") {
		t.Fatalf("Notice(1) = %q", n)
	}
	if n := Notice(3); !strings.Contains(n, "3 unread messages") {
		t.Fatalf("Notice(3) = %q", n)
	}
}
