package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// newAttempt sets up a real ticket + attempt and returns an open session store.
func newAttempt(t *testing.T) (store.Root, string, string, *Store) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	ticket := "PROJ-1"
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	m, err := attempt.Create(root, ticket, attempt.New{Tool: "claude-code", Model: "opus-4.8", Repo: "/repo", Actor: "agent:x"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(root, ticket, m.ID)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return root, ticket, m.ID, s
}

func TestOpenRejectsMissingAttempt(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if _, err := Open(root, "NOPE-1", "0001"); err == nil {
		t.Fatal("expected error opening session on nonexistent attempt")
	}
}

func TestIdentityRoundTrips(t *testing.T) {
	_, _, _, s := newAttempt(t)
	want := Identity{
		Adapter:   "claude-code",
		Model:     "opus-4.8",
		SessionID: "sess-abc123",
		PID:       4242,
		Worktree:  "/tmp/wt/PROJ-1-0001",
		Started:   time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC),
	}
	if err := s.WriteIdentity(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("identity round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	// A fresh store on the same attempt reads it back — the store holds no truth.
	reopened, err := Open(s.root, s.ticket, s.attempt)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, _ := reopened.ReadIdentity(); got != want {
		t.Errorf("reopened identity mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestReadIdentityMissingIsNotExist(t *testing.T) {
	_, _, _, s := newAttempt(t)
	_, err := s.ReadIdentity()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want fs.ErrNotExist, got %v", err)
	}
}

func TestMeterRoundTripsAndUpdates(t *testing.T) {
	_, _, _, s := newAttempt(t)

	// Missing meter reads as ErrNotExist but UpdateMeter starts from zero.
	if _, err := s.ReadMeter(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want fs.ErrNotExist for absent meter, got %v", err)
	}

	// RecordUsage replaces the cumulative snapshot; watchdog bump via UpdateMeter.
	if _, err := s.RecordUsage(agent.Usage{InputTokens: 100, OutputTokens: 20, ContextTokens: 1200, CostUSD: 0.03}); err != nil {
		t.Fatal(err)
	}
	beat := time.Date(2026, 8, 4, 9, 5, 0, 0, time.UTC)
	got, err := s.UpdateMeter(func(m *Meter) {
		m.Respawns++
		m.LastEventAt = beat
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage.InputTokens != 100 || got.Usage.CostUSD != 0.03 || got.Respawns != 1 || !got.LastEventAt.Equal(beat) {
		t.Errorf("meter after update: %+v", got)
	}

	// Usage is cumulative, so a newer snapshot replaces (not sums) the tokens.
	got, err = s.RecordUsage(agent.Usage{InputTokens: 250, OutputTokens: 60, ContextTokens: 1500, CostUSD: 0.07})
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage.InputTokens != 250 || got.Usage.ContextTokens != 1500 {
		t.Errorf("RecordUsage should replace snapshot, got %+v", got.Usage)
	}
	// The watchdog counters survive a usage update.
	if got.Respawns != 1 || !got.LastEventAt.Equal(beat) {
		t.Errorf("watchdog counters clobbered by RecordUsage: %+v", got)
	}

	reloaded, err := s.ReadMeter()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded != got {
		t.Errorf("meter reload mismatch:\n got %+v\nwant %+v", reloaded, got)
	}
}

func TestStreamAppendRoundTrips(t *testing.T) {
	_, _, _, s := newAttempt(t)

	// Empty before any append.
	if lines, err := s.ReadStream(); err != nil || len(lines) != 0 {
		t.Fatalf("empty stream = %v, %v", lines, err)
	}

	in := []string{`{"kind":"system"}`, `{"kind":"assistant","text":"hi"}`, `{"kind":"turn_end"}`}
	for _, l := range in {
		if err := s.AppendStream([]byte(l)); err != nil {
			t.Fatal(err)
		}
	}
	// A line arriving with its own trailing newline must not double up.
	if err := s.AppendStream([]byte(`{"kind":"error"}` + "\n")); err != nil {
		t.Fatal(err)
	}

	lines, err := s.ReadStream()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 4 {
		t.Fatalf("want 4 lines, got %d: %v", len(lines), lines)
	}
	for i, l := range lines {
		if !json.Valid(l) {
			t.Errorf("line %d not valid json: %s", i, l)
		}
	}
	if string(lines[0]) != in[0] || string(lines[3]) != `{"kind":"error"}` {
		t.Errorf("stream content mismatch: %v", lines)
	}
}

func TestStreamRejectsEmbeddedNewline(t *testing.T) {
	_, _, _, s := newAttempt(t)
	if err := s.AppendStream([]byte("{\"a\":1}\n{\"b\":2}")); err == nil {
		t.Error("expected error on embedded newline")
	}
}

func TestStreamConcurrentAppends(t *testing.T) {
	_, _, _, s := newAttempt(t)
	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			if err := s.AppendStream([]byte(fmt.Sprintf(`{"n":%d}`, k))); err != nil {
				t.Errorf("append %d: %v", k, err)
			}
		}(i)
	}
	wg.Wait()

	lines, err := s.ReadStream()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != n {
		t.Fatalf("want %d lines, got %d (interleaving corrupted the tee)", n, len(lines))
	}
	seen := make(map[int]bool, n)
	for _, l := range lines {
		if !json.Valid(l) {
			t.Fatalf("corrupt line: %s", l)
		}
		var v struct{ N int }
		if err := json.Unmarshal(l, &v); err != nil {
			t.Fatalf("unmarshal %s: %v", l, err)
		}
		seen[v.N] = true
	}
	if len(seen) != n {
		t.Errorf("want %d distinct lines, got %d", n, len(seen))
	}
}

// The session/ store must never touch the attempt's durable, hash-chained log.
func TestSessionStaysOutOfHashChain(t *testing.T) {
	root, ticket, id, s := newAttempt(t)

	if err := s.WriteIdentity(Identity{Adapter: "claude-code", SessionID: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordUsage(agent.Usage{InputTokens: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendStream([]byte(`{"kind":"system"}`)); err != nil {
		t.Fatal(err)
	}

	// The log still holds only the genesis "created" event — session writes did
	// not append to it.
	events, err := ticketlog.Read(root, ticket, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "created" {
		t.Errorf("session writes leaked into the log: %+v", events)
	}
	// And the session files live under session/, siblings of log/, not inside it.
	if _, err := os.Stat(root.SessionDir(ticket, id)); err != nil {
		t.Errorf("session dir missing: %v", err)
	}
}

// Atomic replacement leaves a single valid file and no leftover temp files.
func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	root, ticket, id, s := newAttempt(t)
	for i := 0; i < 5; i++ {
		if err := s.WriteIdentity(Identity{Adapter: "claude-code", SessionID: fmt.Sprintf("s%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ReadIdentity()
	if err != nil || got.SessionID != "s4" {
		t.Fatalf("final identity = %+v, %v", got, err)
	}
	entries, err := os.ReadDir(root.SessionDir(ticket, id))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "session.json" {
			t.Errorf("unexpected leftover file in session dir: %s", e.Name())
		}
	}
}
