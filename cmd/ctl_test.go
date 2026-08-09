package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/event"
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

// TestCtlStatusHidesDone drives the status filter end to end: the default list
// view omits terminal Done attempts, --all restores them, and an explicit target
// prints its Done attempt regardless — plus the empty-view hint counts what it
// hid.
func TestCtlStatusHidesDone(t *testing.T) {
	dir := newTicket(t) // PROJ-1/0001, Running

	// A second ticket driven to Done via a `done` lifecycle event.
	if _, code := run(t, "--data", dir, "--actor", "human:test", "new", "PROJ-2", "--title", "Two", "--repo", dir); code != 0 {
		t.Fatalf("new PROJ-2 exited %d", code)
	}
	root := store.Root{Dir: dir}
	if _, err := ticketlog.Append(root, "PROJ-2", "0001", event.Event{Type: "done", Actor: "human:test", Body: "closed"}); err != nil {
		t.Fatalf("append done: %v", err)
	}
	if a, err := project.LoadAttempt(root, "PROJ-2", "0001"); err != nil || a.State != project.Done {
		t.Fatalf("PROJ-2 should be Done (state=%q err=%v)", a.State, err)
	}

	// Default list view: Running shows, Done is hidden.
	out, code := run(t, "--data", dir, "ctl", "status")
	if code != 0 {
		t.Fatalf("ctl status exited %d: %s", code, out)
	}
	if !strings.Contains(out, "PROJ-1/0001") {
		t.Errorf("default view should list the Running attempt:\n%s", out)
	}
	if strings.Contains(out, "PROJ-2/0001") {
		t.Errorf("default view should hide the Done attempt:\n%s", out)
	}

	// --all restores the Done attempt.
	for _, flag := range []string{"--all", "-a"} {
		out, code := run(t, "--data", dir, "ctl", "status", flag)
		if code != 0 {
			t.Fatalf("ctl status %s exited %d: %s", flag, code, out)
		}
		if !strings.Contains(out, "PROJ-2/0001") {
			t.Errorf("%s should include the Done attempt:\n%s", flag, out)
		}
	}

	// An explicit Done target prints regardless of the filter.
	out, code = run(t, "--data", dir, "ctl", "status", "PROJ-2@0001")
	if code != 0 {
		t.Fatalf("ctl status PROJ-2@0001 exited %d: %s", code, out)
	}
	if !strings.Contains(out, "PROJ-2/0001") {
		t.Errorf("an explicitly named Done attempt should print:\n%s", out)
	}
}

// TestCtlStatusEmptyHint shows the one-line hint (with the hidden count) when the
// default view filters everything away.
func TestCtlStatusEmptyHint(t *testing.T) {
	dir := newTicket(t) // PROJ-1/0001, Running
	root := store.Root{Dir: dir}
	if _, err := ticketlog.Append(root, "PROJ-1", "0001", event.Event{Type: "done", Actor: "human:test", Body: "closed"}); err != nil {
		t.Fatalf("append done: %v", err)
	}

	out, code := run(t, "--data", dir, "ctl", "status")
	if code != 0 {
		t.Fatalf("ctl status exited %d: %s", code, out)
	}
	if !strings.Contains(out, "no active attempts — 1 Done hidden; --all to show") {
		t.Errorf("expected the empty-view hint with a hidden count:\n%s", out)
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
	// Tool call shows name and a meaningful argument snippet, under the tool role.
	if !strings.Contains(got, "Bash: ls internal/agent") {
		t.Errorf("tool call not rendered with arguments:\n%s", got)
	}
	// Turn boundary is surfaced.
	if !strings.Contains(got, "turn end (success)") {
		t.Errorf("turn end not rendered:\n%s", got)
	}
	// Every rendered line carries the greppable `[<time>  <tokens>  <role>]: `
	// prefix, and the role vocabulary replaces the old glyph markers (drvctl-025).
	if !strings.Contains(got, "assistant]: Looking at the code now.") {
		t.Errorf("assistant prefix not rendered:\n%s", got)
	}
	if !strings.Contains(got, "tool     ]: Bash: ls internal/agent") {
		t.Errorf("tool prefix not rendered:\n%s", got)
	}
	// Non-TTY render must not leak ANSI escape sequences into a piped consumer.
	if strings.Contains(got, "\033[") {
		t.Errorf("ANSI escapes leaked into non-TTY render:\n%q", got)
	}
	// Transport fields never appear.
	if strings.Contains(got, "sess-secret-uuid") || strings.Contains(got, "session_id") {
		t.Errorf("transport session id leaked into rendered output:\n%s", got)
	}
}

// TestStreamRendererPrefixAndState locks the new render shape: a greppable
// `[<datetime>  <tokens>  <role>]: ` prefix per line, the role vocabulary, the
// dropped usage-only frame, and the token tally that carries the last real
// context snapshot forward — even across a cost-only turn-end frame that reports
// ContextTokens 0 (drvctl-025 #3/#4/#8).
func TestStreamRendererPrefixAndState(t *testing.T) {
	var buf bytes.Buffer
	sr := newStreamRenderer(&buf) // non-TTY: no ANSI, notty markdown
	fixed := time.Date(2026, 8, 9, 14, 5, 6, 0, time.UTC)
	sr.now = func() time.Time { return fixed }

	sr.renderEvent(agent.Event{Kind: agent.EventAssistant, Text: "hello world"})
	sr.renderEvent(agent.Event{Kind: agent.EventUsage, Usage: &agent.Usage{ContextTokens: 12345}})
	sr.renderEvent(agent.Event{Kind: agent.EventToolCall, Tool: &agent.ToolEvent{Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}})
	sr.renderEvent(agent.Event{Kind: agent.EventTurnEnd, Turn: "success", Usage: &agent.Usage{ContextTokens: 0, CostUSD: 0.5}})

	got := buf.String()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 { // the usage-only frame prints nothing
		t.Fatalf("expected 3 rendered lines, got %d:\n%s", len(lines), got)
	}
	// Tokens show '-' before any usage; every line is prefix-then-content.
	if !strings.HasPrefix(lines[0], "[2026-08-09 14:05:06") || !strings.Contains(lines[0], "-  assistant]: hello world") {
		t.Errorf("assistant line = %q", lines[0])
	}
	// After the usage frame the tally shows, and the tool role/summary render.
	if !strings.Contains(lines[1], "12,345  tool     ]: Bash: ls") {
		t.Errorf("tool line = %q", lines[1])
	}
	// The cost-only turn-end frame (ContextTokens 0) must not reset the tally.
	if !strings.Contains(lines[2], "12,345  system   ]: turn end (success)") {
		t.Errorf("turn-end line = %q", lines[2])
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("non-TTY render leaked ANSI escapes:\n%q", got)
	}
}

// TestStreamRendererRoleOnlyColor locks drvctl-025 #17: on a TTY only the role
// token is colour-tinted — the datetime, tokens, brackets and delimiter stay
// uncoloured, and the closing bracket still aligns because the role column is
// padded past its colour escapes.
func TestStreamRendererRoleOnlyColor(t *testing.T) {
	var buf bytes.Buffer
	sr := newStreamRenderer(&buf)
	sr.styled = true // force the TTY styling path without a real terminal
	fixed := time.Date(2026, 8, 9, 14, 5, 6, 0, time.UTC)
	sr.now = func() time.Time { return fixed }

	got := sr.prefix(roleTool)
	// The bracket, datetime and token column are outside any colour escape.
	if !strings.HasPrefix(got, "[2026-08-09 14:05:06  ") {
		t.Fatalf("prefix opening is coloured or malformed: %q", got)
	}
	// Colour opens immediately before the role word and resets immediately after,
	// leaving the trailing pad and the `]: ` delimiter uncoloured.
	want := roleColor(roleTool) + "tool" + ansiReset + "     ]: "
	if !strings.HasSuffix(got, want) {
		t.Errorf("role token not the only coloured span: %q (want suffix %q)", got, want)
	}
	// Total visible width is unchanged from the uncoloured prefix.
	if visible := stripANSI(got); len(visible) != prefixWidth {
		t.Errorf("visible prefix width = %d, want %d: %q", len(visible), prefixWidth, visible)
	}
}

// stripANSI removes CSI escape sequences so a coloured string's visible width
// can be measured.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			if i < len(s) {
				i++ // final byte
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// TestTrimTrailingBlank covers the ANSI-aware right-trim that sees through the
// per-cell colour escapes glamour pads lines with (drvctl-025 #5).
func TestTrimTrailingBlank(t *testing.T) {
	if got := trimTrailingBlank("hello   "); got != "hello" {
		t.Errorf("plain trim = %q, want %q", got, "hello")
	}
	// Content wrapped in a colour escape, then colour-wrapped padding cells: the
	// padding is dropped and a single reset closes the surviving colour.
	padded := "\x1b[38;5;252mhi\x1b[0m\x1b[38;5;252m \x1b[0m\x1b[38;5;252m \x1b[0m"
	if got := trimTrailingBlank(padded); !strings.Contains(got, "hi") || !strings.HasSuffix(got, ansiReset) || strings.HasSuffix(strings.TrimSuffix(got, ansiReset), " ") {
		t.Errorf("ansi-padded trim = %q", got)
	}
	// A line that is only escapes and spaces collapses to empty (and is skipped).
	if got := trimTrailingBlank("\x1b[38;5;252m \x1b[0m\x1b[38;5;252m \x1b[0m"); got != "" {
		t.Errorf("blank-only trim = %q, want empty", got)
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
