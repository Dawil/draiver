package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func TestResolveCtlTarget(t *testing.T) {
	dir := newTicket(t) // ticket PROJ-1, attempt 0001
	dataFlag, actorFlag, attemptFlag = "", "", ""
	t.Setenv("DRAIVER_ATTEMPT", "")
	root := store.Root{Dir: dir}

	t.Run("latest attempt", func(t *testing.T) {
		ticket, att, err := resolveCtlTarget(root, "PROJ-1")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if ticket != "PROJ-1" || att != "0001" {
			t.Fatalf("got %s/%s, want PROJ-1/0001", ticket, att)
		}
	})

	t.Run("explicit @attempt suffix", func(t *testing.T) {
		ticket, att, err := resolveCtlTarget(root, "PROJ-1@0001")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if ticket != "PROJ-1" || att != "0001" {
			t.Fatalf("got %s/%s, want PROJ-1/0001", ticket, att)
		}
	})

	t.Run("unknown ticket", func(t *testing.T) {
		if _, _, err := resolveCtlTarget(root, "NOPE-9"); err == nil {
			t.Fatal("expected an error for an unknown ticket")
		}
	})

	t.Run("unknown attempt", func(t *testing.T) {
		if _, _, err := resolveCtlTarget(root, "PROJ-1@9999"); err == nil {
			t.Fatal("expected an error for an unknown attempt")
		}
	})
}

// TestCtlEnableDisable drives the enable/disable verbs end to end: each appends a
// log event that flips the derived Enabled bit, the default is disabled, and the
// bit is a separate axis that leaves the control state on Running.
func TestCtlEnableDisable(t *testing.T) {
	dir := newTicket(t) // ticket PROJ-1, attempt 0001

	// Default: disabled.
	if a, err := project.LoadAttempt(store.Root{Dir: dir}, "PROJ-1", "0001"); err != nil || a.Enabled {
		t.Fatalf("fresh attempt should be disabled by default (enabled=%v err=%v)", a.Enabled, err)
	}

	// enable → Enabled, still Running.
	out, code := run(t, "--data", dir, "--actor", "human:dave", "ctl", "enable", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("ctl enable exited %d: %s", code, out)
	}
	a, err := project.LoadAttempt(store.Root{Dir: dir}, "PROJ-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Enabled {
		t.Fatal("attempt should be enabled after ctl enable")
	}
	if a.State != project.Running {
		t.Fatalf("enable must not change control state: got %q", a.State)
	}

	// disable → back to disabled.
	if out, code := run(t, "--data", dir, "--actor", "human:dave", "ctl", "disable", "PROJ-1@0001"); code != 0 {
		t.Fatalf("ctl disable exited %d: %s", code, out)
	}
	if a, err := project.LoadAttempt(store.Root{Dir: dir}, "PROJ-1", "0001"); err != nil || a.Enabled {
		t.Fatalf("attempt should be disabled after ctl disable (enabled=%v err=%v)", a.Enabled, err)
	}

	// The two events are in the hash chain like every other.
	events, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Type)
	}
	if strings.Join(kinds, ",") != "created,enable,disable" {
		t.Fatalf("unexpected log types: %v", kinds)
	}

	// An unknown target is an error, not a silent no-op.
	if _, code := run(t, "--data", dir, "ctl", "enable", "NOPE-9"); code == 0 {
		t.Fatal("expected nonzero exit enabling an unknown ticket")
	}
}

func TestContextGauge(t *testing.T) {
	cases := []struct {
		ctx, cap int
		want     string
	}{
		{0, 200_000, "ctx=-"},
		{24_000, 200_000, "ctx=12% (24,000/200,000)"},
		{1_000, 0, "ctx=1,000 tok"},
		{1_000_000, 1_000_000, "ctx=100% (1,000,000/1,000,000)"},
	}
	for _, c := range cases {
		if got := contextGauge(c.ctx, c.cap); got != c.want {
			t.Errorf("contextGauge(%d,%d) = %q, want %q", c.ctx, c.cap, got, c.want)
		}
	}
}

func TestCommas(t *testing.T) {
	cases := map[int]string{0: "0", 42: "42", 1000: "1,000", 1234567: "1,234,567", -1000: "-1,000"}
	for n, want := range cases {
		if got := commas(n); got != want {
			t.Errorf("commas(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestTailStreamNoFollow prints the on-disk stream and returns; a missing stream
// is not an error.
func TestTailStreamNoFollow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")

	// A missing stream is not an error, and the not-yet-started notice is printed
	// in both the raw (--json) and rendered modes.
	for _, render := range []bool{false, true} {
		var out bytes.Buffer
		if err := tailStream(context.Background(), &out, path, false, render); err != nil {
			t.Fatalf("missing stream should not error (render=%v): %v", render, err)
		}
		if !strings.Contains(out.String(), "no session stream yet") {
			t.Fatalf("expected the not-yet-started notice (render=%v), got %q", render, out.String())
		}
	}

	var out bytes.Buffer

	if err := os.WriteFile(path, []byte(`{"a":1}`+"\n"+`{"b":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := tailStream(context.Background(), &out, path, false, false); err != nil {
		t.Fatalf("tail: %v", err)
	}
	if !strings.Contains(out.String(), `{"a":1}`) || !strings.Contains(out.String(), `{"b":2}`) {
		t.Fatalf("stream not printed:\n%s", out.String())
	}
}

// TestTailStreamRender renders the recorded stream for a human: assistant prose
// reads as prose (not an escaped JSON string), a tool call shows its name plus a
// meaningful argument snippet, and the stream-json transport fields (the session
// id, the envelope) do not leak into the output.
func TestTailStreamRender(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")
	lines := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-secret-uuid","cwd":"/x","model":"opus"}`,
		`{"type":"assistant","session_id":"sess-secret-uuid","message":{"role":"assistant","content":[{"type":"text","text":"Looking at the code now."}]}}`,
		`{"type":"assistant","session_id":"sess-secret-uuid","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls internal/agent"}}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"DONE.","total_cost_usd":0.01,"session_id":"sess-secret-uuid"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := tailStream(context.Background(), &out, path, false, true); err != nil {
		t.Fatalf("render tail: %v", err)
	}
	got := out.String()

	// Assistant prose is rendered as prose, and it is not the raw JSON line.
	if !strings.Contains(got, "Looking at the code now.") {
		t.Errorf("assistant prose not rendered:\n%s", got)
	}
	if strings.Contains(got, `"type":"assistant"`) {
		t.Errorf("raw JSON leaked into rendered output:\n%s", got)
	}
	// Tool call shows name and a meaningful argument snippet.
	if !strings.Contains(got, "> Bash: ls internal/agent") {
		t.Errorf("tool call not rendered with arguments:\n%s", got)
	}
	// Turn boundary is surfaced.
	if !strings.Contains(got, "turn end (success)") {
		t.Errorf("turn end not rendered:\n%s", got)
	}
	// Transport fields never appear.
	if strings.Contains(got, "sess-secret-uuid") || strings.Contains(got, "session_id") {
		t.Errorf("transport session id leaked into rendered output:\n%s", got)
	}
}

// TestCtlLogsRenderAndJSON drives `ctl logs` end to end in both modes: the
// default renders a human-readable line, while --json reproduces the raw
// stream.jsonl verbatim for `| jq` pipelines.
func TestCtlLogsRenderAndJSON(t *testing.T) {
	dir := newTicket(t) // ticket PROJ-1, attempt 0001
	root := store.Root{Dir: dir}
	streamPath := root.SessionStreamPath("PROJ-1", "0001")
	if err := os.MkdirAll(filepath.Dir(streamPath), 0o755); err != nil {
		t.Fatal(err)
	}
	rawLine := `{"type":"assistant","session_id":"sess-1","message":{"role":"assistant","content":[{"type":"text","text":"Hello from the session."}]}}`
	if err := os.WriteFile(streamPath, []byte(rawLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Default: human-readable, prose not raw JSON, session id dropped.
	out, code := run(t, "--data", dir, "ctl", "logs", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("ctl logs exited %d: %s", code, out)
	}
	if !strings.Contains(out, "Hello from the session.") {
		t.Errorf("rendered output missing assistant prose:\n%s", out)
	}
	if strings.Contains(out, `"session_id"`) || strings.Contains(out, `"type":"assistant"`) {
		t.Errorf("rendered output leaked raw stream-json:\n%s", out)
	}

	// --json: byte-for-byte passthrough of the recorded line.
	out, code = run(t, "--data", dir, "ctl", "logs", "PROJ-1@0001", "--json")
	if code != 0 {
		t.Fatalf("ctl logs --json exited %d: %s", code, out)
	}
	if !strings.Contains(out, rawLine) {
		t.Errorf("--json did not reproduce the raw stream line:\n%s", out)
	}
}

// TestTailStreamFollow keeps printing appended lines until the context is
// cancelled, and picks up a line written after it started following.
func TestTailStreamFollow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(path, []byte(`{"first":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu safeBuf
	done := make(chan error, 1)
	go func() { done <- tailStream(ctx, &mu, path, true, false) }()

	waitUntil(t, "first line", func() bool { return strings.Contains(mu.String(), `{"first":1}`) })

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"second":2}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	waitUntil(t, "appended line", func() bool { return strings.Contains(mu.String(), `{"second":2}`) })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tailStream returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tailStream did not return after cancel")
	}
}

// TestTailStreamRenderFollow follows a stream in render mode: it renders what is
// already on disk and picks up an appended line, proving -f works in the
// human-readable mode too (not just raw --json).
func TestTailStreamRenderFollow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")
	first := `{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"first line here"}]}}`
	if err := os.WriteFile(path, []byte(first+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu safeBuf
	done := make(chan error, 1)
	go func() { done <- tailStream(ctx, &mu, path, true, true) }()

	waitUntil(t, "first rendered line", func() bool { return strings.Contains(mu.String(), "first line here") })

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	second := `{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"second line here"}]}}`
	if _, err := f.WriteString(second + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	waitUntil(t, "appended rendered line", func() bool { return strings.Contains(mu.String(), "second line here") })
	if strings.Contains(mu.String(), "session_id") {
		t.Errorf("transport fields leaked in render-follow output:\n%s", mu.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tailStream returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tailStream did not return after cancel")
	}
}

// safeBuf is a tiny concurrency-safe buffer so the follow goroutine and the test
// can touch the output without racing.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
