package web

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/systemlog"
)

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

// TestGitPushEmitsSystemEvent is the drv-045 git source: a push appends exactly one
// system.jsonl record (class git, outcome ok) — the operational view, alongside the
// durable `note` the push already records.
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
	if len(evs) != 1 {
		t.Fatalf("want exactly one system event for the push, got %d: %+v", len(evs), evs)
	}
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
