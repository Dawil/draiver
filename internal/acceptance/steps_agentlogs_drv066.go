package acceptance

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/webshot"
)

// steps_agentlogs_drv066.go realises drv-066's acceptance criteria
// (features/agent-logs/recorded-times.feature): a recorded agent stream replayed via
// `draiver ctl logs` (the real built binary, non-follow) and via the web Agent Logs
// panel renders each line under its record's own recorded timestamp, never the
// render-time clock.

const (
	alTicket = "AGENTLOG-1"
	alAtt    = "0001"
)

// alRecords are the seeded timestamped records: the body each renders to and the
// recorded RFC3339 time it must be stamped with. Far-past so "now" can't collide.
var alRecords = []struct{ ts, body string }{
	{"2026-01-05T09:10:11.000Z", "reading the renderer"},
	{"2026-01-05T09:20:22.000Z", "Bash: go test ./internal/streamlog"},
	{"2026-01-05T09:30:33.000Z", "the fix is in"},
}

func init() {
	drv066 := map[string]StepFunc{
		"a finished attempt whose agent stream carries recorded timestamps": givenAgentLogRecorded,
		"I replay it with draiver ctl logs":                                 whenCtlLogsReplay,
		"I open the Agent Logs panel for the recorded attempt":              whenOpenRecordedAgentPanel,
		"every agent line shows its recorded time, not the current time":    thenAgentLinesRecorded,
	}
	for text, fn := range drv066 {
		registry[text] = fn
	}
}

func givenAgentLogRecorded(w *World, sr *StepRun) error {
	dataDir, err := os.MkdirTemp("", "agentlog-data-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(alTicket, alAtt); err != nil {
		return err
	}
	spec := "---\nid: " + alTicket + "\ntitle: drv-066 recorded agent times\n---\n\n# drv-066 recorded agent times\n"
	if err := os.WriteFile(root.SpecPath(alTicket), []byte(spec), 0o644); err != nil {
		return err
	}
	meta := "---\nid: " + alAtt + "\nticket: " + alTicket + "\nrepo: /tmp/agentlog-repo\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(alTicket, alAtt), []byte(meta), 0o644); err != nil {
		return err
	}
	if _, err := ticketlog.Append(root, alTicket, alAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return err
	}
	if err := root.EnsureSessionDir(alTicket, alAtt); err != nil {
		return err
	}
	// Shaped like a real Claude Code stream.jsonl: init and result carry no
	// timestamp; every assistant record does.
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"s"}`,
		`{"type":"assistant","timestamp":"` + alRecords[0].ts + `","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"` + alRecords[0].body + `"}]}}`,
		`{"type":"assistant","timestamp":"` + alRecords[1].ts + `","session_id":"s","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./internal/streamlog"}}]}}`,
		`{"type":"assistant","timestamp":"` + alRecords[2].ts + `","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"` + alRecords[2].body + `"}]}}`,
		`{"type":"result","subtype":"success","session_id":"s"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(root.SessionStreamPath(alTicket, alAtt), []byte(stream), 0o644); err != nil {
		return err
	}
	srv, err := web.New(root)
	if err != nil {
		return err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	w.set("agentlogboard", &board{root: root, wd: dataDir, srv: ts})
	return nil
}

func currentAgentLogBoard(w *World) (*board, error) {
	b, _ := w.get("agentlogboard").(*board)
	if b == nil {
		return nil, errors.New("no recorded agent-log attempt set up in scope")
	}
	return b, nil
}

func whenCtlLogsReplay(w *World, sr *StepRun) error {
	b, err := currentAgentLogBoard(w)
	if err != nil {
		return err
	}
	bin, err := overlayBin()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "--data", b.root.Dir, "ctl", "logs", alTicket+"@"+alAtt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("draiver ctl logs: %w\n%s", err, stderr.String())
	}
	w.set("agentlogtext", stdout.String())
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "text/plain", Name: "ctl-logs-output", Data: base64.StdEncoding.EncodeToString(stdout.Bytes()),
	})
	return nil
}

func whenOpenRecordedAgentPanel(w *World, sr *StepRun) error {
	b, err := currentAgentLogBoard(w)
	if err != nil {
		return err
	}
	text, err := readSSEBacklog(b.url("/ticket/" + alTicket + "/" + alAtt + "/agent-logs"))
	if err != nil {
		return err
	}
	w.set("agentlogtext", text)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	png, err := webshot.Capture(ctx, b.url("/ticket/"+alTicket+"/"+alAtt), webshot.Options{
		Width: 1280, Height: 1000, FullPage: true,
		ClickSelector:          `[data-testid="agent-logs-summary"]`,
		AfterClickWaitSelector: `[data-testid="agent-logs-stream"]`,
	})
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png", Name: "agent-logs-recorded-times", Data: base64.StdEncoding.EncodeToString(png),
	})
	return nil
}

func thenAgentLinesRecorded(w *World, sr *StepRun) error {
	text, _ := w.get("agentlogtext").(string)
	if text == "" {
		return errors.New("no agent log output captured")
	}
	for _, rec := range alRecords {
		t, err := time.Parse(time.RFC3339Nano, rec.ts)
		if err != nil {
			return err
		}
		want := "[" + t.Local().Format("2006-01-02 15:04:05")
		found := false
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, want) && strings.Contains(line, rec.body) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no line %q stamped with its recorded time %s:\n%s", rec.body, want, text)
		}
	}
	// Today's date must appear on no line: init precedes any recorded time only in
	// the CLI render (it falls back to now there), so judge the lines after it.
	today := time.Now().Format("2006-01-02")
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, today) && !strings.Contains(line, "session online") {
			return fmt.Errorf("a line was stamped with the current date %s, not its recorded time:\n%s", today, text)
		}
	}
	// The untimestamped result inherits the last recorded time (CLI shows turn end).
	if strings.Contains(text, "turn end") {
		t, _ := time.Parse(time.RFC3339Nano, alRecords[len(alRecords)-1].ts)
		want := "[" + t.Local().Format("2006-01-02 15:04:05")
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "turn end") && !strings.Contains(line, want) {
				return fmt.Errorf("turn end did not inherit the last recorded time %s: %q", want, line)
			}
		}
	}
	return nil
}
