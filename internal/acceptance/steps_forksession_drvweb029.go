package acceptance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/webshot"
)

// ---- fork-session.feature: copy a "fork this session" command (drvweb-029) ----

const forkSessionID = "0f1e2d3c-fork-demo-session"

func init() {
	drvweb029 := map[string]StepFunc{
		"a board attempt with a seeded claude-code session.json":                                      givenForkSession,
		"a board attempt whose session.json has no session id":                                        givenForkNoSessionID,
		"the attempt page shows the fork-session copy button":                                         thenForkButtonShown,
		"the rendered fork command cds into the absolute worktree and forks the session in plan mode": thenForkCommandRendered,
		"the copy button sits inline in the Agent Logs section header":                                thenForkButtonInAgentLogs,
		"the attempt page shows no fork-session copy button":                                          thenForkButtonAbsent,
	}
	for k, v := range drvweb029 {
		registry[k] = v
	}
}

// forkWorktree is the absolute worktree the seeded session.json names — a
// subdirectory with a space in it, so the screenshot shows the quoting at work.
func forkWorktree(b *board) string { return filepath.Join(b.wd, "my worktree") }

func seedForkSession(w *World, sid string) error {
	b, err := seedBoard(w, greenPyramid)
	if err != nil {
		return err
	}
	if err := b.root.EnsureSessionDir(accTicket, accAtt); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session.Identity{
		Adapter: "claude-code", SessionID: sid, Worktree: forkWorktree(b),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(b.root.SessionMetaPath(accTicket, accAtt), data, 0o644); err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

func givenForkSession(w *World, sr *StepRun) error     { return seedForkSession(w, forkSessionID) }
func givenForkNoSessionID(w *World, sr *StepRun) error { return seedForkSession(w, "") }

func forkPage(w *World) (string, *board, error) {
	b, err := currentBoard(w)
	if err != nil {
		return "", nil, err
	}
	page, err := getBody(b.url("/ticket/" + accTicket + "/" + accAtt))
	return page, b, err
}

func thenForkButtonShown(w *World, sr *StepRun) error {
	page, _, err := forkPage(w)
	if err != nil {
		return err
	}
	for _, want := range []string{`data-testid="fork-copy"`, `data-testid="fork-command"`, "plan mode"} {
		if !strings.Contains(page, want) {
			return fmt.Errorf("attempt page missing %q", want)
		}
	}
	return nil
}

// forkCommandText extracts the unescaped text of the <code data-testid="fork-command">.
func forkCommandText(page string) (string, bool) {
	const open = `data-testid="fork-command">`
	i := strings.Index(page, open)
	if i < 0 {
		return "", false
	}
	rest := page[i+len(open):]
	j := strings.Index(rest, "</code>")
	if j < 0 {
		return "", false
	}
	return html.UnescapeString(rest[:j]), true
}

func thenForkCommandRendered(w *World, sr *StepRun) error {
	page, b, err := forkPage(w)
	if err != nil {
		return err
	}
	cmd, ok := forkCommandText(page)
	if !ok {
		return fmt.Errorf("no fork command <code> on the attempt page")
	}
	wantCd := "cd '" + forkWorktree(b) + "' && "
	if !strings.HasPrefix(cmd, wantCd) || !filepath.IsAbs(forkWorktree(b)) {
		return fmt.Errorf("fork command %q does not start with absolute cd %q", cmd, wantCd)
	}
	for _, want := range []string{"claude --resume '" + forkSessionID + "'", "--fork-session", "--permission-mode plan"} {
		if !strings.Contains(cmd, want) {
			return fmt.Errorf("fork command %q missing %q", cmd, want)
		}
	}
	// Click the copy button and wait for its status feedback ("Copied", or the
	// manual-copy hint if headless Chromium refuses both clipboard paths), then shoot.
	return shootForkCopy(w, sr, b)
}

func thenForkButtonAbsent(w *World, sr *StepRun) error {
	page, _, err := forkPage(w)
	if err != nil {
		return err
	}
	if strings.Contains(page, `data-testid="fork-session"`) {
		return fmt.Errorf("fork-session button rendered for an attempt with no session id")
	}
	// Evidence: the same header with no copy button or command.
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt, "fork-session-absent", false, `[data-testid="ticket-detail"]`, "")
}

// shootForkCopy clicks the real copy button in a browser and waits for its status
// line to fill before capturing the header (button, feedback and command).
func shootForkCopy(w *World, sr *StepRun, b *board) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	png, err := webshot.Capture(ctx, b.url("/ticket/"+accTicket+"/"+accAtt), webshot.Options{
		Width: 1280, Height: 900,
		WaitSelector:           `[data-testid="fork-copy"]`,
		ClickSelector:          `[data-testid="fork-copy"]`,
		AfterClickWaitSelector: `.fork-copied:not(:empty)`,
	})
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png",
		Name:     "fork-session-copy",
		Data:     base64.StdEncoding.EncodeToString(png),
	})
	return nil
}

// thenForkButtonInAgentLogs pins the placement the reviewer asked for (decision
// #18): the button is inside the Agent Logs <summary> row, styled as the "Add
// entry" pill (compose-submit), not in the page header.
func thenForkButtonInAgentLogs(w *World, sr *StepRun) error {
	page, _, err := forkPage(w)
	if err != nil {
		return err
	}
	const open = `data-testid="agent-logs-summary"`
	i := strings.Index(page, open)
	if i < 0 {
		return fmt.Errorf("no Agent Logs summary on the attempt page")
	}
	j := strings.Index(page[i:], "</summary>")
	if j < 0 {
		return fmt.Errorf("unterminated Agent Logs summary")
	}
	summary := page[i : i+j]
	if !strings.Contains(summary, `data-testid="fork-copy"`) {
		return fmt.Errorf("fork-copy button is not inside the Agent Logs summary")
	}
	if !strings.Contains(summary, `class="compose-submit fork-copy"`) {
		return fmt.Errorf("fork-copy button does not use the Add entry (compose-submit) style")
	}
	if header, ok := sliceBetween(page, "<header", "</header>"); ok && strings.Contains(header, "fork-copy") {
		return fmt.Errorf("fork-copy button still rendered in the page header")
	}
	return nil
}
