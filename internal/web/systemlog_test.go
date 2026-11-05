package web

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/systemlog"
)

// TestSystemRelayBuffersLines pins the drv-048 streaming relay writer: bytes are
// buffered until a newline and relayed one physical line at a time (a trailing CR
// trimmed, a blank line dropped), carrying the isErr role; a final partial line is
// released only by flush, so no output is lost or duplicated across Writes split
// mid-line.
func TestSystemRelayBuffersLines(t *testing.T) {
	type line struct {
		text  string
		isErr bool
	}
	var got []line
	w := &systemRelay{isErr: true, relay: func(s string, isErr bool) {
		got = append(got, line{s, isErr})
	}}
	// A write ending mid-line holds the tail; the newline arrives in the next write.
	w.Write([]byte("first line\r\nsecond "))
	w.Write([]byte("half\n\n  \nthird line"))
	// No newline terminates "third line": it is held until flush.
	if want := []line{{"first line", true}, {"second half", true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pre-flush lines = %+v, want %+v", got, want)
	}
	w.flush()
	want := []line{{"first line", true}, {"second half", true}, {"third line", true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("post-flush lines = %+v, want %+v", got, want)
	}
	// flush is idempotent on an already-drained buffer.
	w.flush()
	if len(got) != 3 {
		t.Fatalf("flush on an empty buffer relayed extra lines: %+v", got)
	}
}

// TestTrimFirstLine pins the git body-relay split: everything after the first line,
// surrounding blank lines trimmed; a single-line (or empty) report yields "" so the
// summary headline is never duplicated.
func TestTrimFirstLine(t *testing.T) {
	cases := map[string]string{
		"only one line":          "",
		"head\nbody":             "body",
		"head\nbody\nmore":       "body\nmore",
		"head\n\nbody\n":         "body",
		"head\r\nbody\r\nmore\n": "body\r\nmore",
		"":                       "",
	}
	for in, want := range cases {
		if got := trimFirstLine(in); got != want {
			t.Errorf("trimFirstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// systemEventsOf reads every system.jsonl record written for an attempt (drv-045),
// so a handler test can assert it journaled exactly one system event with the
// expected class/message. A missing file is treated as "no events" rather than an
// error — the journal is created lazily on the first append.
func systemEventsOf(t *testing.T, root store.Root, id, att string) []systemlog.SystemEvent {
	t.Helper()
	f, err := os.Open(root.SessionSystemLogPath(id, att))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open system.jsonl %s/%s: %v", id, att, err)
	}
	defer f.Close()
	var evs []systemlog.SystemEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev systemlog.SystemEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshal %q: %v", sc.Text(), err)
		}
		evs = append(evs, ev)
	}
	return evs
}

// TestSystemLogsPanelRenders pins drv-045's new panel onto the attempt page: a
// second live-log <details> beside Agent Logs, pointing at the system-logs SSE
// route, with its own empty placeholder — and the Agent Logs panel still present and
// pointing at its own route.
func TestSystemLogsPanelRenders(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	body := get(t, h, "/ticket/PROJ-1/0001").Body.String()

	for _, want := range []string{
		`data-testid="system-logs"`,
		`data-testid="system-logs-details"`,
		`data-testid="system-logs-stream"`,
		`/ticket/PROJ-1/0001/system-logs`,
		// Agent Logs stays, on its own route.
		`data-testid="agent-logs"`,
		`/ticket/PROJ-1/0001/agent-logs`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("attempt page missing %q", want)
		}
	}
}

// TestProvenanceEmitsSystemEvent is the drv-045 provenance source: editing provenance
// appends exactly one system.jsonl record with the provenance class and an old→new
// message, in addition to the durable attempt.md write.
func TestProvenanceEmitsSystemEvent(t *testing.T) {
	root := seedBoard(t)
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/orig", "orig-base")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	if rr := postProvenance(t, h, "PROJ-3", "0001", "", "release/v2"); rr.Code != 200 {
		t.Fatalf("POST provenance = %d, want 200\n%s", rr.Code, rr.Body.String())
	}

	evs := systemEventsOf(t, root, "PROJ-3", "0001")
	if len(evs) != 1 {
		t.Fatalf("want exactly one system event, got %d: %+v", len(evs), evs)
	}
	ev := evs[0]
	if ev.Class != systemlog.ClassProvenance {
		t.Errorf("class = %q, want %q", ev.Class, systemlog.ClassProvenance)
	}
	if !strings.Contains(ev.Message, "base") || !strings.Contains(ev.Message, "release/v2") {
		t.Errorf("message should name the base change: %q", ev.Message)
	}
	// It carried the from→to and the stamped actor.
	if ev.Fields["base_from"] != "orig-base" || ev.Fields["base_to"] != "release/v2" {
		t.Errorf("from→to not recorded: %+v", ev.Fields)
	}
	if ev.TS == "" {
		t.Errorf("system event was not timestamped")
	}
}

// TestGitPushEmitsSystemEvent is the drv-045 git source, extended by drv-048: a push
// appends the one-line summary record (class git, outcome ok) — the operational
// headline, alongside the durable `note` the push already records — FOLLOWED BY the
// rest of the push report relayed line by line, so the full output (not just the
// truncated first line) reaches the System Logs panel.
func TestGitPushEmitsSystemEvent(t *testing.T) {
	root, _, _, _ := remoteReviewAttempt(t, false)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if rr := post(t, s.Handler(), "/ticket/PROJ-1/0001/git/push"); rr.Code != 200 {
		t.Fatalf("POST git/push = %d, want 200\n%s", rr.Code, rr.Body.String())
	}

	evs := systemEventsOf(t, root, "PROJ-1", "0001")
	if len(evs) < 1 {
		t.Fatalf("want at least the summary system event for the push, got %d: %+v", len(evs), evs)
	}
	// The first event is the one-line summary headline (drv-045), unchanged.
	ev := evs[0]
	if ev.Class != systemlog.ClassGit {
		t.Errorf("class = %q, want %q", ev.Class, systemlog.ClassGit)
	}
	if ev.Fields["verb"] != "push" || ev.Fields["outcome"] != "ok" {
		t.Errorf("git fields wrong: %+v", ev.Fields)
	}
	if ev.Error {
		t.Errorf("a successful push must not be flagged as an error")
	}
	// drv-048: the report body (everything after the first line — here the recorded-note
	// line) is relayed as its own git-class line(s), so the panel shows the full output.
	if len(evs) < 2 {
		t.Fatalf("want the push report body relayed after the summary, got only %+v", evs)
	}
	for _, body := range evs[1:] {
		if body.Class != systemlog.ClassGit {
			t.Errorf("relayed body line class = %q, want %q: %+v", body.Class, systemlog.ClassGit, body)
		}
		if body.Error {
			t.Errorf("a successful push's relayed body must not be flagged as an error: %+v", body)
		}
		if strings.TrimSpace(body.Message) == "" {
			t.Errorf("relayed body line has an empty message: %+v", body)
		}
	}
}

// TestGitSyncFailureEmitsErrorSystemEvent: a failing sync records a system event
// flagged as an error (so it reads red), even though the failure is a handled
// outcome (HTTP 200 banner), not a server fault.
func TestGitSyncFailureEmitsErrorSystemEvent(t *testing.T) {
	root, _, checkout, _ := remoteReviewAttempt(t, false)
	// Dirty the attempt's checkout so worktree.Sync refuses the back-merge
	// (ErrCheckoutDirty) — the drv-022 handled-failure path.
	if err := os.WriteFile(filepath.Join(checkout, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rr := post(t, s.Handler(), "/ticket/PROJ-1/0001/git/sync")
	if rr.Code != 200 {
		t.Fatalf("POST git/sync = %d, want 200 (handled failure banner)\n%s", rr.Code, rr.Body.String())
	}

	evs := systemEventsOf(t, root, "PROJ-1", "0001")
	if len(evs) != 1 {
		t.Fatalf("want exactly one system event for the sync, got %d: %+v", len(evs), evs)
	}
	if ev := evs[0]; ev.Class != systemlog.ClassGit || ev.Fields["outcome"] != "fail" || !ev.Error {
		t.Errorf("a failed sync should log a git error event: %+v", ev)
	}
}
