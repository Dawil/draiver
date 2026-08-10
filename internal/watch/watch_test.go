package watch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func newWatcher(t *testing.T) (*Watcher, store.Root, string, string, *session.Store) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	ticket := "PROJ-1"
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	m, err := attempt.Create(root, ticket, attempt.New{Tool: "claude-code", Model: "opus-4.8", Repo: "/repo", Actor: "human:x"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.Open(root, ticket, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	w := New(root, ticket, m.ID, "agent:claude-code", sess, ProtocolRecognizer{Ticket: ticket})
	return w, root, ticket, m.ID, sess
}

func bashCallRaw(cmd string, raw string) agent.Event {
	in, _ := json.Marshal(map[string]string{"command": cmd})
	return agent.Event{
		Kind: agent.EventToolCall,
		Tool: &agent.ToolEvent{ID: "t1", Name: "Bash", Input: in},
		Raw:  json.RawMessage(raw),
	}
}

func TestProcess_PromotesDecisionToLog(t *testing.T) {
	w, root, ticket, att, _ := newWatcher(t)

	ev := bashCallRaw(`draiver log PROJ-1 "Chose worktrees over containers" --type decision`,
		`{"type":"assistant","raw":1}`)
	promoted, err := w.Process(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !promoted {
		t.Fatal("expected the decision to be promoted")
	}

	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	// attempt.Create writes a "created" event as seq 1; the promotion is seq 2.
	var found bool
	for _, e := range events {
		if e.Type == "decision" {
			found = true
			if e.Body != "Chose worktrees over containers" {
				t.Fatalf("body = %q", e.Body)
			}
			if e.Actor != "agent:claude-code" {
				t.Fatalf("actor = %q, want agent:claude-code", e.Actor)
			}
		}
	}
	if !found {
		t.Fatalf("no decision event in log: %+v", events)
	}
}

func TestProcess_NonProtocolToolNotPromoted(t *testing.T) {
	w, root, ticket, att, _ := newWatcher(t)
	before, _ := ticketlog.Read(root, ticket, att)

	in, _ := json.Marshal(map[string]string{"file_path": "/x"})
	ev := agent.Event{Kind: agent.EventToolCall, Tool: &agent.ToolEvent{Name: "Read", Input: in}, Raw: json.RawMessage(`{"a":1}`)}
	promoted, err := w.Process(ev)
	if err != nil {
		t.Fatal(err)
	}
	if promoted {
		t.Fatal("a Read tool call must not promote")
	}
	after, _ := ticketlog.Read(root, ticket, att)
	if len(after) != len(before) {
		t.Fatalf("log grew from %d to %d on a non-protocol call", len(before), len(after))
	}
}

func TestProcess_TeesRawOncePerLine(t *testing.T) {
	w, _, _, _, sess := newWatcher(t)

	// One stream-json line normalizes into several events sharing the same Raw
	// (text + tool_use + usage). Teeing must write that line exactly once.
	raw := json.RawMessage(`{"type":"assistant","id":"m1"}`)
	shared := []agent.Event{
		{Kind: agent.EventAssistant, Text: "thinking out loud", Raw: raw},
		{Kind: agent.EventToolCall, Tool: &agent.ToolEvent{Name: "Read"}, Raw: raw},
		{Kind: agent.EventUsage, Usage: &agent.Usage{InputTokens: 10}, Raw: raw},
	}
	for _, ev := range shared {
		if _, err := w.Process(ev); err != nil {
			t.Fatal(err)
		}
	}
	// A second, distinct line tees again.
	if _, err := w.Process(agent.Event{Kind: agent.EventAssistant, Text: "next", Raw: json.RawMessage(`{"type":"assistant","id":"m2"}`)}); err != nil {
		t.Fatal(err)
	}
	// A synthesized event (nil Raw) tees nothing.
	if _, err := w.Process(agent.Event{Kind: agent.EventError, Err: "boom"}); err != nil {
		t.Fatal(err)
	}

	lines, err := sess.ReadStream()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("teed %d lines, want 2: %v", len(lines), lines)
	}
}

func TestProcess_MetersUsageLiveAndCostMonotonic(t *testing.T) {
	w, _, _, _, sess := newWatcher(t)

	// Per-message usage frame: tokens/context climb, cost is zero.
	if _, err := w.Process(agent.Event{
		Kind:  agent.EventUsage,
		Usage: &agent.Usage{InputTokens: 100, CacheReadTokens: 50, ContextTokens: 150, CostUSD: 0},
		Raw:   json.RawMessage(`{"u":1}`),
	}); err != nil {
		t.Fatal(err)
	}
	// Turn-end frame carries the cumulative dollar figure.
	if _, err := w.Process(agent.Event{
		Kind:  agent.EventTurnEnd,
		Usage: &agent.Usage{InputTokens: 120, CacheReadTokens: 60, ContextTokens: 180, CostUSD: 0.42},
		Raw:   json.RawMessage(`{"u":2}`),
	}); err != nil {
		t.Fatal(err)
	}
	m, err := sess.ReadMeter()
	if err != nil {
		t.Fatal(err)
	}
	if m.Usage.ContextTokens != 180 {
		t.Fatalf("context = %d, want live 180", m.Usage.ContextTokens)
	}
	if m.Usage.CostUSD != 0.42 {
		t.Fatalf("cost = %v, want 0.42", m.Usage.CostUSD)
	}

	// Next turn's first per-message frame reports cost 0 again — cost must not
	// regress below the last known cumulative figure.
	if _, err := w.Process(agent.Event{
		Kind:  agent.EventUsage,
		Usage: &agent.Usage{InputTokens: 200, ContextTokens: 260, CostUSD: 0},
		Raw:   json.RawMessage(`{"u":3}`),
	}); err != nil {
		t.Fatal(err)
	}
	m, _ = sess.ReadMeter()
	if m.Usage.ContextTokens != 260 {
		t.Fatalf("context = %d, want live 260", m.Usage.ContextTokens)
	}
	if m.Usage.CostUSD != 0.42 {
		t.Fatalf("cost regressed to %v, want held 0.42", m.Usage.CostUSD)
	}
}

// A cumulative turn-end frame (ContextTokens == 0, as the adapter emits for a
// result line) must fold cost only and leave the live context gauge alone — it
// carries a session-wide token total, not a context snapshot. Guards the
// drvctl-009 "2M context" regression.
func TestProcess_CumulativeFrameDoesNotClobberContextGauge(t *testing.T) {
	w, _, _, _, sess := newWatcher(t)

	// Real per-request snapshot sets the live gauge.
	if _, err := w.Process(agent.Event{
		Kind:  agent.EventUsage,
		Usage: &agent.Usage{InputTokens: 2, CacheReadTokens: 132895, CacheCreationTokens: 2623, ContextTokens: 135520, CostUSD: 0},
		Raw:   json.RawMessage(`{"u":1}`),
	}); err != nil {
		t.Fatal(err)
	}
	// Cumulative turn-end frame: huge summed tokens, ContextTokens deliberately 0,
	// carrying the dollar figure.
	if _, err := w.Process(agent.Event{
		Kind:  agent.EventTurnEnd,
		Usage: &agent.Usage{InputTokens: 43, CacheReadTokens: 1931206, CacheCreationTokens: 120403, ContextTokens: 0, CostUSD: 3.33},
		Raw:   json.RawMessage(`{"u":2}`),
	}); err != nil {
		t.Fatal(err)
	}

	m, err := sess.ReadMeter()
	if err != nil {
		t.Fatal(err)
	}
	if m.Usage.ContextTokens != 135520 {
		t.Fatalf("context = %d, want held 135520 (cumulative frame must not clobber the gauge)", m.Usage.ContextTokens)
	}
	if m.Usage.CacheReadTokens != 132895 {
		t.Fatalf("cache_read = %d, want held 132895 (token counts held with the gauge)", m.Usage.CacheReadTokens)
	}
	if m.Usage.CostUSD != 3.33 {
		t.Fatalf("cost = %v, want 3.33 folded from the turn-end frame", m.Usage.CostUSD)
	}
}

// The session-cumulative Totals sum every per-request frame — and only those.
// The cumulative turn-end frame (ContextTokens == 0) must be excluded, or the
// turn's requests are double-counted (its cache_read is already their sum).
func TestProcess_TotalsSumPerRequestFramesOnly(t *testing.T) {
	w, _, _, _, sess := newWatcher(t)

	// Two per-request frames: their raw fields sum into Totals.
	if _, err := w.Process(agent.Event{
		Kind:  agent.EventUsage,
		Usage: &agent.Usage{InputTokens: 100, OutputTokens: 20, CacheCreationTokens: 50, ContextTokens: 150},
		Raw:   json.RawMessage(`{"u":1}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Process(agent.Event{
		Kind:  agent.EventUsage,
		Usage: &agent.Usage{InputTokens: 5, OutputTokens: 30, CacheReadTokens: 145, ContextTokens: 150},
		Raw:   json.RawMessage(`{"u":2}`),
	}); err != nil {
		t.Fatal(err)
	}
	// Cumulative turn-end frame: huge summed cache_read, ContextTokens 0. Excluded.
	if _, err := w.Process(agent.Event{
		Kind:  agent.EventTurnEnd,
		Usage: &agent.Usage{InputTokens: 105, OutputTokens: 50, CacheReadTokens: 999999, ContextTokens: 0, CostUSD: 0.5},
		Raw:   json.RawMessage(`{"u":3}`),
	}); err != nil {
		t.Fatal(err)
	}

	m, err := sess.ReadMeter()
	if err != nil {
		t.Fatal(err)
	}
	want := agent.Totals{InputTokens: 105, OutputTokens: 50, CacheReadTokens: 145, CacheCreationTokens: 50}
	if m.Totals != want {
		t.Fatalf("totals = %+v, want %+v (turn-end frame must not be summed)", m.Totals, want)
	}
	if !m.Totals.CachingActive() {
		t.Fatal("caching_active should be true after a cache read/creation was seen")
	}
}

func TestPromote_AdvancesHeartbeat(t *testing.T) {
	w, _, _, _, sess := newWatcher(t)
	fixed := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return fixed }

	if _, err := w.Process(bashCallRaw(`draiver log PROJ-1 note-body --type note`, `{"r":1}`)); err != nil {
		t.Fatal(err)
	}
	m, err := sess.ReadMeter()
	if err != nil {
		t.Fatal(err)
	}
	if !m.LastEventAt.Equal(fixed) {
		t.Fatalf("LastEventAt = %v, want %v", m.LastEventAt, fixed)
	}
}

func TestIngest_DrainsChannelAndPromotes(t *testing.T) {
	w, root, ticket, att, _ := newWatcher(t)

	ch := make(chan agent.Event, 4)
	ch <- agent.Event{Kind: agent.EventAssistant, Text: "hi", Raw: json.RawMessage(`{"1":1}`)}
	ch <- bashCallRaw(`draiver escalate PROJ-1 "blocked on creds"`, `{"2":1}`)
	ch <- agent.Event{Kind: agent.EventUsage, Usage: &agent.Usage{ContextTokens: 42}, Raw: json.RawMessage(`{"3":1}`)}
	close(ch)

	if err := w.Ingest(context.Background(), ch); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	events, _ := ticketlog.Read(root, ticket, att)
	var esc bool
	for _, e := range events {
		if e.Type == "escalation" && e.Body == "blocked on creds" {
			esc = true
		}
	}
	if !esc {
		t.Fatalf("escalation not promoted: %+v", events)
	}
}

func TestIngest_HonorsContextCancel(t *testing.T) {
	w, _, _, _, _ := newWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch := make(chan agent.Event) // never sends
	if err := w.Ingest(ctx, ch); err != context.Canceled {
		t.Fatalf("ingest err = %v, want context.Canceled", err)
	}
}
