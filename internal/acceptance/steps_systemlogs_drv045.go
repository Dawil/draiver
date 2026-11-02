package acceptance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/systemlog"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/webshot"
)

// steps_systemlogs_drv045.go realises drv-045's acceptance criteria
// (features/system-logs/system-logs.feature) against a live internal/web server
// backed by a seeded attempt whose session dir carries all three per-attempt log
// files: the agent stream (stream.jsonl), draiverctld health (ctl.jsonl), and the new
// system-event journal (system.jsonl). The scenarios read the live SSE panels the
// browser opens — asserting the two-panel split and that every System Logs line
// carries its own recorded timestamp — and capture a Playwright screenshot of both
// panels side by side as evidence. The steps register themselves from an init(),
// leaving the earlier steps untouched.

const (
	sysTicket = "SYSLOG-1"
	sysAtt    = "0001"
)

func init() {
	drv045 := map[string]StepFunc{
		"an attempt with seeded agent, health, and system logs":                                          givenSysLogSeeded,
		"the attempt page shows both an Agent Logs panel and a System Logs panel on their own stream routes": thenTwoPanels,
		"I open the System Logs panel":                                                                   whenOpenSystemPanel,
		"the System Logs panel shows the provenance, git, review-env, and promotion events":              thenSystemEventsShown,
		"each System Logs line is stamped with its own recorded time, not the current clock":             thenRecordedTimes,
		"I open the Agent Logs panel":                                                                    whenOpenAgentPanel,
		"the Agent Logs panel shows the agent prose without any ctl health or session-lifecycle lines":  thenAgentClean,
		"the system.jsonl journal is deleted and a new system event is recorded":                         whenDeleteAndRecord,
		"the System Logs panel starts empty and shows only the refilled event":                           thenRefilled,
	}
	for text, fn := range drv045 {
		registry[text] = fn
	}
}

// sysLogBoard is a board plus the recorded timestamps seeded into system.jsonl, so a
// Then step can assert the rendered prefix reflects those exact times (local) rather
// than the render-time clock.
type sysLogBoard struct {
	*board
	seededTimes []string // the RFC3339 ts values written into system.jsonl, in order
}

// The five source classes drv-045 adds, each seeded with a distinct, well-in-the-past
// recorded time so the render-from-recorded-ts assertion is unambiguous (today is far
// from these dates). Class, a human message, and the ts.
var sysLogSeed = []systemlog.SystemEvent{
	{TS: "2026-01-02T01:01:01Z", Class: systemlog.ClassProvenance, Message: "provenance updated: base `old` → `main`"},
	{TS: "2026-01-02T02:02:02Z", Class: systemlog.ClassGit, Message: "git push: ok — pushed draiver/SYSLOG-1/0001", Fields: map[string]string{"verb": "push", "outcome": "ok"}},
	{TS: "2026-01-02T03:03:03Z", Class: systemlog.ClassReviewEnvUp, Message: "review-env up: `review` → starting", Fields: map[string]string{"env": "review"}},
	{TS: "2026-01-02T04:04:04Z", Class: systemlog.ClassReviewEnvDn, Message: "review-env down: `review` → down", Fields: map[string]string{"env": "review"}},
	{TS: "2026-01-02T05:05:05Z", Class: systemlog.ClassPromotion, Message: "promotion check: `local` @ `abc1234` — pass", Fields: map[string]string{"env": "local", "verdict": "pass"}},
}

// seedSysLogBoard stands up a live web server over a Running SYSLOG-1/0001 attempt and
// writes its three session logs: an agent stream with prose + the synthetic
// system/turn-end frames (to prove Agent Logs drops them), a health edge in ctl.jsonl
// (to prove it moves to System Logs), and the five seeded system events.
func seedSysLogBoard(w *World) (*sysLogBoard, error) {
	dataDir, err := os.MkdirTemp("", "syslog-data-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(sysTicket, sysAtt); err != nil {
		return nil, err
	}
	spec := "---\nid: " + sysTicket + "\ntitle: drv-045 system logs\n---\n\n# drv-045 system logs\n\n## Acceptance criteria\n\n- Two panels: Agent Logs and System Logs.\n"
	if err := os.WriteFile(root.SpecPath(sysTicket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + sysAtt + "\nticket: " + sysTicket + "\nrepo: /tmp/syslog-repo\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(sysTicket, sysAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	if _, err := ticketlog.Append(root, sysTicket, sysAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return nil, err
	}
	if err := root.EnsureSessionDir(sysTicket, sysAtt); err != nil {
		return nil, err
	}

	// Agent stream: a system/init frame, assistant prose, and a result/turn-end frame
	// — so Agent Logs can be shown to render the prose while dropping the synthetic
	// `session online` / `turn end` system-role lines.
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"s"}`,
		`{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"wiring the second panel"}]}}`,
		`{"type":"result","subtype":"success","session_id":"s"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(root.SessionStreamPath(sysTicket, sysAtt), []byte(stream), 0o644); err != nil {
		return nil, err
	}

	// Health edge: one error-start/error-end pair with its own recorded ts — it moves
	// into the System Logs stream with the rest of the system signals.
	ctl := strings.Join([]string{
		`{"ts":"2026-01-02T00:30:00Z","class":"worktree-clash","event":"start","message":"git worktree add failed"}`,
		`{"ts":"2026-01-02T00:35:00Z","class":"worktree-clash","event":"end"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(root.SessionCtlLogPath(sysTicket, sysAtt), []byte(ctl), 0o644); err != nil {
		return nil, err
	}

	// The five seeded system events, written with their recorded ts preserved (not via
	// Append, which would re-stamp them — the point is to replay known past times).
	var times []string
	var lines []string
	for _, ev := range sysLogSeed {
		times = append(times, ev.TS)
		b, err := json.Marshal(ev)
		if err != nil {
			return nil, err
		}
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(root.SessionSystemLogPath(sysTicket, sysAtt), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return nil, err
	}

	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	return &sysLogBoard{board: &board{root: root, wd: dataDir, srv: ts}, seededTimes: times}, nil
}

func givenSysLogSeeded(w *World, sr *StepRun) error {
	b, err := seedSysLogBoard(w)
	if err != nil {
		return err
	}
	w.set("syslogboard", b)
	return nil
}

func currentSysLogBoard(w *World) (*sysLogBoard, error) {
	b, _ := w.get("syslogboard").(*sysLogBoard)
	if b == nil {
		return nil, errors.New("no system-logs board set up in scope")
	}
	return b, nil
}

// readSSEBacklog opens the SSE route and reads for a short window, returning the
// backlog the follow emits before it blocks waiting for new lines. The System/Agent
// log follows never end on their own, so the read is bounded by a context deadline;
// the bytes read so far are what the panel would have shown on connect.
func readSSEBacklog(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// ReadAll returns the bytes read so far when the context deadline cancels the
	// streaming body — exactly the backlog we want.
	data, _ := io.ReadAll(resp.Body)
	return string(data), nil
}

func thenTwoPanels(w *World, sr *StepRun) error {
	b, err := currentSysLogBoard(w)
	if err != nil {
		return err
	}
	resp, err := http.Get(b.url("/ticket/" + sysTicket + "/" + sysAtt))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	body := string(raw)
	for _, want := range []string{
		`data-testid="agent-logs"`,
		`/ticket/` + sysTicket + `/` + sysAtt + `/agent-logs`,
		`data-testid="system-logs"`,
		`/ticket/` + sysTicket + `/` + sysAtt + `/system-logs`,
	} {
		if !strings.Contains(body, want) {
			return fmt.Errorf("attempt page missing %q", want)
		}
	}
	// Evidence: both panels expanded, side by side.
	png, err := captureBothPanels(b)
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png", Name: "system-logs-both-panels", Data: base64.StdEncoding.EncodeToString(png),
	})
	return nil
}

// captureBothPanels expands both live-log panels and screenshots the full page, so a
// reviewer sees Agent Logs and System Logs with content, side by side.
func captureBothPanels(b *sysLogBoard) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return webshot.Capture(ctx, b.url("/ticket/"+sysTicket+"/"+sysAtt), webshot.Options{
		Width: 1280, Height: 1200, FullPage: true,
		ClickSelector:          `[data-testid="agent-logs-summary"]`,
		ClickSelector2:         `[data-testid="system-logs-summary"]`,
		AfterClickWaitSelector: `[data-testid="system-logs-stream"]`,
	})
}

func whenOpenSystemPanel(w *World, sr *StepRun) error {
	b, err := currentSysLogBoard(w)
	if err != nil {
		return err
	}
	text, err := readSSEBacklog(b.url("/ticket/" + sysTicket + "/" + sysAtt + "/system-logs"))
	if err != nil {
		return err
	}
	w.set("systemstream", text)
	// Evidence: the System Logs panel expanded with events present.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	png, err := webshot.Capture(ctx, b.url("/ticket/"+sysTicket+"/"+sysAtt), webshot.Options{
		Width: 1280, Height: 1200, FullPage: true,
		ClickSelector:          `[data-testid="system-logs-summary"]`,
		AfterClickWaitSelector: `[data-testid="system-logs-stream"]`,
	})
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png", Name: "system-logs-panel", Data: base64.StdEncoding.EncodeToString(png),
	})
	return nil
}

func thenSystemEventsShown(w *World, sr *StepRun) error {
	text, _ := w.get("systemstream").(string)
	if text == "" {
		return errors.New("no System Logs backlog captured")
	}
	// Each of the five seeded sources renders its message, plus the health edge.
	for _, want := range []string{
		"provenance updated: base `old` → `main`",
		"git push: ok",
		"review-env up: `review` → starting",
		"review-env down: `review` → down",
		"promotion check: `local`",
		"ctl: worktree-clash started",
	} {
		if !strings.Contains(text, want) {
			return fmt.Errorf("System Logs panel missing %q:\n%s", want, text)
		}
	}
	return nil
}

func thenRecordedTimes(w *World, sr *StepRun) error {
	b, err := currentSysLogBoard(w)
	if err != nil {
		return err
	}
	text, _ := w.get("systemstream").(string)
	if text == "" {
		return errors.New("no System Logs backlog captured")
	}
	// Every seeded record's own recorded time appears in the rendered prefix (local),
	// proving the panel used the record's ts, not render-time now().
	for _, rfc := range b.seededTimes {
		t, perr := time.Parse(time.RFC3339, rfc)
		if perr != nil {
			return perr
		}
		stamp := t.Local().Format("2006-01-02 15:04:05")
		if !strings.Contains(text, stamp) {
			return fmt.Errorf("System Logs line did not carry its recorded time %q (expected local %q):\n%s", rfc, stamp, text)
		}
	}
	// And the render-time clock must NOT have leaked in: no line is stamped with the
	// current minute (the seeded times are in a far-past month).
	nowStamp := time.Now().Format("2006-01-02 15:04")
	if strings.Contains(text, nowStamp) {
		return fmt.Errorf("a System Logs line leaked a render-time stamp %q; must use the recorded ts:\n%s", nowStamp, text)
	}
	return nil
}

func whenOpenAgentPanel(w *World, sr *StepRun) error {
	b, err := currentSysLogBoard(w)
	if err != nil {
		return err
	}
	text, err := readSSEBacklog(b.url("/ticket/" + sysTicket + "/" + sysAtt + "/agent-logs"))
	if err != nil {
		return err
	}
	w.set("agentstream", text)
	return nil
}

func thenAgentClean(w *World, sr *StepRun) error {
	b, err := currentSysLogBoard(w)
	if err != nil {
		return err
	}
	text, _ := w.get("agentstream").(string)
	if text == "" {
		return errors.New("no Agent Logs backlog captured")
	}
	if !strings.Contains(text, "wiring the second panel") {
		return fmt.Errorf("Agent Logs missing the agent prose:\n%s", text)
	}
	for _, forbidden := range []string{"ctl:", "session online", "turn end"} {
		if strings.Contains(text, forbidden) {
			return fmt.Errorf("Agent Logs must not carry %q (it moved to System Logs):\n%s", forbidden, text)
		}
	}
	// Evidence: the Agent Logs panel expanded, showing the agent prose alone.
	png, err := capturePanel(b, "agent-logs-summary", "agent-logs-stream")
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png", Name: "agent-logs-panel", Data: base64.StdEncoding.EncodeToString(png),
	})
	return nil
}

func whenDeleteAndRecord(w *World, sr *StepRun) error {
	b, err := currentSysLogBoard(w)
	if err != nil {
		return err
	}
	if err := os.Remove(b.root.SessionSystemLogPath(sysTicket, sysAtt)); err != nil {
		return err
	}
	// A new action records one fresh system event; system.jsonl is rebuildable, so
	// the deletion does not error the attempt and the stream refills.
	return systemlog.Append(b.root, sysTicket, sysAtt, time.Now, systemlog.SystemEvent{
		Class:   systemlog.ClassGit,
		Message: "git sync: ok — refilled after delete",
		Fields:  map[string]string{"verb": "sync", "outcome": "ok"},
	})
}

func thenRefilled(w *World, sr *StepRun) error {
	b, err := currentSysLogBoard(w)
	if err != nil {
		return err
	}
	// The attempt page still serves 200 (a deleted journal does not error the page).
	resp, err := http.Get(b.url("/ticket/" + sysTicket + "/" + sysAtt))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("attempt page status = %d, want 200 after deleting system.jsonl", resp.StatusCode)
	}
	text, err := readSSEBacklog(b.url("/ticket/" + sysTicket + "/" + sysAtt + "/system-logs"))
	if err != nil {
		return err
	}
	if !strings.Contains(text, "refilled after delete") {
		return fmt.Errorf("System Logs did not show the refilled event:\n%s", text)
	}
	// The old provenance line is gone — the journal started empty and holds only the
	// refilled record (the health ctl.jsonl line still shows; it is a separate file).
	if strings.Contains(text, "provenance updated") {
		return fmt.Errorf("deleted system events should not survive; got:\n%s", text)
	}
	// Evidence: the System Logs panel expanded after the journal was deleted and
	// refilled — only the fresh event remains.
	png, err := capturePanel(b, "system-logs-summary", "system-logs-stream")
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png", Name: "system-logs-refilled", Data: base64.StdEncoding.EncodeToString(png),
	})
	return nil
}

// capturePanel expands one live-log panel (by its summary testid) and screenshots the
// full attempt page once its stream element is present, so a scenario that asserts a
// single panel's content carries that panel as rendered evidence.
func capturePanel(b *sysLogBoard, summaryTestid, streamTestid string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return webshot.Capture(ctx, b.url("/ticket/"+sysTicket+"/"+sysAtt), webshot.Options{
		Width: 1280, Height: 1200, FullPage: true,
		ClickSelector:          `[data-testid="` + summaryTestid + `"]`,
		AfterClickWaitSelector: `[data-testid="` + streamTestid + `"]`,
	})
}
