package streamlog

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTailSystemLogRecordedTimestamps is the drv-045 timestamp-truth AC: System Logs
// lines are stamped with the event's own recorded `ts`, never render-time now(). It
// writes system.jsonl records with KNOWN PAST ts values and asserts the rendered
// prefix reflects those times — proving a replay shows the same timestamps a live
// follow showed, not the clock.
func TestTailSystemLogRecordedTimestamps(t *testing.T) {
	dir := t.TempDir()
	systemPath := filepath.Join(dir, "system.jsonl")
	ctlPath := filepath.Join(dir, "ctl.jsonl")

	// Two system events with distinct, well-in-the-past recorded times.
	if err := os.WriteFile(systemPath, []byte(strings.Join([]string{
		`{"ts":"2026-01-02T03:04:05Z","class":"provenance","message":"provenance updated: base set to ` + "`main`" + `"}`,
		`{"ts":"2026-01-02T08:09:10Z","class":"git","message":"git push: ok — pushed 1 ref","fields":{"verb":"push","outcome":"ok"}}`,
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := TailSystemLog(context.Background(), &out, systemPath, ctlPath, false, -1); err != nil {
		t.Fatalf("render system tail: %v", err)
	}
	got := out.String()

	// Each message renders under the system role with its own recorded time, shown
	// local — NOT the wallclock.
	t1, _ := time.Parse(time.RFC3339, "2026-01-02T03:04:05Z")
	t2, _ := time.Parse(time.RFC3339, "2026-01-02T08:09:10Z")
	wantLine1 := "[" + t1.Local().Format(tsLayout)
	wantLine2 := "[" + t2.Local().Format(tsLayout)
	if !strings.Contains(got, wantLine1) {
		t.Errorf("first system line did not carry its recorded timestamp %q:\n%s", wantLine1, got)
	}
	if !strings.Contains(got, wantLine2) {
		t.Errorf("second system line did not carry its recorded timestamp %q:\n%s", wantLine2, got)
	}
	if !strings.Contains(got, "system   ]: provenance updated: base set to `main`") {
		t.Errorf("provenance event not rendered under the system role:\n%s", got)
	}
	if !strings.Contains(got, "system   ]: git push: ok — pushed 1 ref") {
		t.Errorf("git event not rendered:\n%s", got)
	}
	// Belt-and-braces: the current year/day render-time stamp must NOT appear — the
	// line's time is the record's, not the clock's. (The recorded times are in the
	// far past relative to any plausible test run.)
	nowStamp := time.Now().Format("2006-01-02 15")
	if strings.Contains(got, nowStamp) {
		t.Errorf("system line leaked a render-time stamp %q; must use the recorded ts:\n%s", nowStamp, got)
	}
}

// TestTailSystemLogFoldsHealth: health transitions (ctl.jsonl) move into the System
// Logs stream (drv-045) and keep their own recorded ts and error/system roles — the
// same rendering the old blended agent stream gave them, now in their proper home.
func TestTailSystemLogFoldsHealth(t *testing.T) {
	dir := t.TempDir()
	systemPath := filepath.Join(dir, "system.jsonl")
	ctlPath := filepath.Join(dir, "ctl.jsonl")

	if err := os.WriteFile(ctlPath, []byte(strings.Join([]string{
		`{"ts":"2026-08-10T09:00:00Z","class":"worktree-clash","event":"start","message":"git worktree add failed"}`,
		`{"ts":"2026-08-10T09:05:00Z","class":"worktree-clash","event":"end"}`,
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := TailSystemLog(context.Background(), &out, systemPath, ctlPath, false, -1); err != nil {
		t.Fatalf("render system tail: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "error    ]: ctl: worktree-clash started: git worktree add failed") {
		t.Errorf("health start not rendered under the error role in the system stream:\n%s", got)
	}
	if !strings.Contains(got, "system   ]: ctl: worktree-clash cleared") {
		t.Errorf("health end not rendered under the system role in the system stream:\n%s", got)
	}
}

// TestTailSystemLogErrorRole: a system event marked Error renders under the error
// role so a failed action reads red in the panel, like a health error-start.
func TestTailSystemLogErrorRole(t *testing.T) {
	dir := t.TempDir()
	systemPath := filepath.Join(dir, "system.jsonl")
	if err := os.WriteFile(systemPath, []byte(
		`{"ts":"2026-01-02T03:04:05Z","class":"git","message":"git sync: fail — merge conflict","error":true}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := TailSystemLog(context.Background(), &out, systemPath, filepath.Join(dir, "ctl.jsonl"), false, -1); err != nil {
		t.Fatalf("render system tail: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "error    ]: git sync: fail — merge conflict") {
		t.Errorf("failed system event not rendered under the error role:\n%s", got)
	}
}

// TestTailSystemLogEmpty: a missing/empty system.jsonl (the rebuildable-deletable AC)
// is not an error — the stream simply reports it is empty.
func TestTailSystemLogEmpty(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := TailSystemLog(context.Background(), &out, filepath.Join(dir, "system.jsonl"), filepath.Join(dir, "ctl.jsonl"), false, -1); err != nil {
		t.Fatalf("render empty system tail: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "no system events yet") {
		t.Errorf("empty system stream should show a quiet placeholder, got:\n%q", got)
	}
}

// TestTailAgentStreamOmitsSystemLines is the other half of the split (drv-045): the
// Agent Logs source renders the agent stream ALONE — no synthetic `session online`
// / `turn end` system-role lines, and no ctl.jsonl health (it is never passed) — so
// the agent column is purely the model/tools talking.
func TestTailAgentStreamOmitsSystemLines(t *testing.T) {
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(streamPath, []byte(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"s"}`,
		`{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"working on it"}]}}`,
		`{"type":"result","subtype":"success","session_id":"s"}`,
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := TailAgentStream(context.Background(), &out, streamPath, false, -1); err != nil {
		t.Fatalf("render agent tail: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "working on it") {
		t.Errorf("agent prose not rendered:\n%s", got)
	}
	if strings.Contains(got, "session online") {
		t.Errorf("Agent Logs must not splice in the synthetic `session online` line:\n%s", got)
	}
	if strings.Contains(got, "turn end") {
		t.Errorf("Agent Logs must not splice in the synthetic `turn end` line:\n%s", got)
	}
}
