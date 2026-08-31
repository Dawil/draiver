package streamlog

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
)

// TestTailStreamNoFollow prints the on-disk stream and returns; a missing stream
// is not an error.
func TestTailStreamNoFollow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")

	// A missing stream is not an error, and the not-yet-started notice is printed
	// in both the raw (--json) and rendered modes.
	for _, render := range []bool{false, true} {
		var out bytes.Buffer
		if err := TailStream(context.Background(), &out, path, filepath.Join(dir, "ctl.jsonl"), false, render, -1); err != nil {
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
	if err := TailStream(context.Background(), &out, path, filepath.Join(dir, "ctl.jsonl"), false, false, -1); err != nil {
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
	if err := TailStream(context.Background(), &out, path, filepath.Join(dir, "ctl.jsonl"), false, true, -1); err != nil {
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

// TestTailStreamMergesHealth is the drvctl-027 surfacing: `ctl logs` (human render)
// interleaves draiverctld's own operational health (session/ctl.jsonl) with the
// agent stream, so a wedged attempt's error-start/error-end show up beside the
// agent's prose — with each health line carrying its own recorded timestamp, under
// the error/system roles. The --json passthrough is unaffected (asserted separately).
func TestTailStreamMergesHealth(t *testing.T) {
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "stream.jsonl")
	ctlPath := filepath.Join(dir, "ctl.jsonl")

	if err := os.WriteFile(streamPath, []byte(
		`{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"working on it"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctlPath, []byte(strings.Join([]string{
		`{"ts":"2026-08-10T09:00:00Z","class":"worktree-clash","event":"start","message":"git worktree add failed: already checked out"}`,
		`{"ts":"2026-08-10T09:05:00Z","class":"worktree-clash","event":"end"}`,
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := TailStream(context.Background(), &out, streamPath, ctlPath, false, true, -1); err != nil {
		t.Fatalf("render tail: %v", err)
	}
	got := out.String()

	// The agent stream still renders.
	if !strings.Contains(got, "working on it") {
		t.Errorf("agent stream not rendered:\n%s", got)
	}
	// The health start renders as an error-role line, carrying the class and message.
	if !strings.Contains(got, "error    ]: ctl: worktree-clash started: git worktree add failed") {
		t.Errorf("health start not rendered under the error role:\n%s", got)
	}
	// The health end renders as a system-role line.
	if !strings.Contains(got, "system   ]: ctl: worktree-clash cleared") {
		t.Errorf("health end not rendered under the system role:\n%s", got)
	}
	// The health line uses its own recorded timestamp (rendered in local time, like
	// the stream's wallclock), not render-time wallclock.
	start, _ := time.Parse(time.RFC3339, "2026-08-10T09:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-08-10T09:05:00Z")
	if !strings.Contains(got, start.Local().Format(tsLayout)) || !strings.Contains(got, end.Local().Format(tsLayout)) {
		t.Errorf("health line did not carry its recorded timestamp:\n%s", got)
	}
}

// TestTailStreamJSONIgnoresHealth: the raw --json passthrough is unchanged — it
// emits stream.jsonl byte-for-byte and never folds in ctl.jsonl health (that is a
// human-render-only concern), so `| jq` pipelines keep working (drvctl-027 non-goal).
func TestTailStreamJSONIgnoresHealth(t *testing.T) {
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "stream.jsonl")
	ctlPath := filepath.Join(dir, "ctl.jsonl")
	if err := os.WriteFile(streamPath, []byte(`{"a":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctlPath, []byte(`{"ts":"2026-08-10T09:00:00Z","class":"worktree-clash","event":"start"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := TailStream(context.Background(), &out, streamPath, ctlPath, false, false, -1); err != nil {
		t.Fatalf("raw tail: %v", err)
	}
	got := out.String()
	if got != `{"a":1}`+"\n" {
		t.Errorf("raw --json must be the stream verbatim with no health folded in, got:\n%q", got)
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
	go func() { done <- TailStream(ctx, &mu, path, filepath.Join(dir, "ctl.jsonl"), true, false, -1) }()

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
			t.Fatalf("TailStream returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TailStream did not return after cancel")
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
	go func() { done <- TailStream(ctx, &mu, path, filepath.Join(dir, "ctl.jsonl"), true, true, -1) }()

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
			t.Fatalf("TailStream returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TailStream did not return after cancel")
	}
}

// TestTailOffset locks the backlog-seek arithmetic: for N complete lines it returns
// the byte offset of the start of the last N (N < 0 = the whole file, N == 0 = the
// current end), and a trailing partial line (no newline yet) is kept within the
// window rather than counted as its own line (drvctl-030).
func TestTailOffset(t *testing.T) {
	whole := "aaa\nbbb\nccc\nddd\n"
	cases := []struct {
		name    string
		content string
		n       int
		want    string // bytes from the returned offset to EOF
	}{
		{"full", whole, -1, whole},
		{"none", whole, 0, ""},
		{"last-one", whole, 1, "ddd\n"},
		{"last-two", whole, 2, "ccc\nddd\n"},
		{"exactly-all", whole, 4, whole},
		{"more-than-present", whole, 9, whole},
		{"empty-file", "", 3, ""},
		// A half-written trailing record (no newline) rides along in the window.
		{"trailing-partial", "aaa\nbbb\nccc", 2, "bbb\nccc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			off, err := tailOffset(f, c.n)
			if err != nil {
				t.Fatalf("tailOffset(%d): %v", c.n, err)
			}
			buf := make([]byte, len(c.content)+1)
			m, _ := f.ReadAt(buf, off)
			if got := string(buf[:m]); got != c.want {
				t.Errorf("tailOffset(%d) off=%d read %q, want %q", c.n, off, got, c.want)
			}
		})
	}
}

// TestTailStreamFollowWindow: -f starts from the last N stream records (the tail
// window) instead of replaying the whole recorded history, then follows live — the
// hang the webui's Agent Logs panel hit was exactly this full-history replay. The
// dropped backlog head never prints; a line appended after the follow starts still
// does. Raw (--json) path, so the assertion is byte-exact.
func TestTailStreamFollowWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join([]string{
		`{"n":1}`, `{"n":2}`, `{"n":3}`, `{"n":4}`, `{"n":5}`,
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu safeBuf
	done := make(chan error, 1)
	go func() { done <- TailStream(ctx, &mu, path, filepath.Join(dir, "ctl.jsonl"), true, false, 2) }()

	waitUntil(t, "windowed backlog tip", func() bool { return strings.Contains(mu.String(), `{"n":5}`) })
	got := mu.String()
	if !strings.Contains(got, `{"n":4}`) {
		t.Errorf("the last 2 records should print, missing n=4:\n%s", got)
	}
	for _, dropped := range []string{`{"n":1}`, `{"n":2}`, `{"n":3}`} {
		if strings.Contains(got, dropped) {
			t.Errorf("record outside the last-2 window should not replay, saw %s:\n%s", dropped, got)
		}
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"n":6}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	waitUntil(t, "line appended after follow started", func() bool { return strings.Contains(mu.String(), `{"n":6}`) })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("TailStream returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TailStream did not return after cancel")
	}
}

// TestTailStreamFollowTailZero: --tail 0 shows no backlog at all — only lines
// appended after the follow starts. (--tail -1 restoring full replay is covered by
// the follow tests that pass -1.)
func TestTailStreamFollowTailZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(path, []byte(`{"old":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu safeBuf
	done := make(chan error, 1)
	go func() { done <- TailStream(ctx, &mu, path, filepath.Join(dir, "ctl.jsonl"), true, false, 0) }()

	// Let the follower open the file and seek to its current end before appending,
	// so the appended line is unambiguously "after the follow started" (the first
	// drain runs immediately; the poll loop only sleeps between passes).
	time.Sleep(300 * time.Millisecond)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"new":2}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	waitUntil(t, "line appended after follow", func() bool { return strings.Contains(mu.String(), `{"new":2}`) })
	if strings.Contains(mu.String(), `{"old":1}`) {
		t.Errorf("--tail 0 must not replay any backlog:\n%s", mu.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("TailStream returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TailStream did not return after cancel")
	}
}

// TestTailStreamNoFollowIgnoresTail: the window is a follow-mode concern. A plain
// `logs` (no -f) prints the whole history regardless of the tail value passed —
// `--tail` is ignored without `-f` (drvctl-030).
func TestTailStreamNoFollowIgnoresTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join([]string{
		`{"n":1}`, `{"n":2}`, `{"n":3}`,
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	// tail=1 would keep only the last record if it applied — but without -f it must
	// not, so all three still print.
	if err := TailStream(context.Background(), &out, path, filepath.Join(dir, "ctl.jsonl"), false, false, 1); err != nil {
		t.Fatalf("tail: %v", err)
	}
	for _, want := range []string{`{"n":1}`, `{"n":2}`, `{"n":3}`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("non-follow must print the full history, missing %s:\n%s", want, out.String())
		}
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
