package acceptance

import (
	"context"
	"encoding/base64"
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
	"github.com/Dawil/draiver/internal/limit"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/webshot"
)

// steps_restart.go realises drv-028's acceptance criteria
// (features/attempt-page/restart-context.feature) against a live internal/web server.
// The "Restart context window" button appears only on a context-window auto-stop
// escalation and, clicked, both resolves that escalation and hands the attempt to a
// restart on a clean context window. The restart is injected (web.WithRestart) with a
// spy so a scenario can drive the real button click in a browser and then verify both
// halves on disk — mirroring the drv-022 Git-controls steps. The steps register
// themselves from an init(), leaving the earlier registries untouched.

const (
	rsTicket = "RST-1"
	rsAtt    = "0001"
	rsSeq    = 2 // the escalation seq seedRestartBoard appends after #1 created
)

func init() {
	drv028 := map[string]StepFunc{
		"a board attempt stuck on a context-window auto-stop escalation":                            givenAutoStopBoard,
		"a board attempt stuck on an ordinary escalation":                                           givenOrdinaryEscBoard,
		"a board attempt stuck on a context-window auto-stop escalation with no running controller": givenAutoStopBoardNoController,
		"I click Restart context window":                                                            whenClickRestart,
		"the escalation offers both Resolve and Restart context window":                             thenOffersResolveAndRestart,
		"the escalation offers Resolve without a Restart context window button":                     thenOffersResolveOnly,
		"the escalation is resolved and the attempt is restarted on a clean context window":         thenResolvedAndRestarted,
		"the escalation is resolved durably and the restart failure is reported":                    thenResolvedDespiteFailure,
	}
	for text, fn := range drv028 {
		registry[text] = fn
	}
}

// restartSpy records the restart calls the button triggers, so a Then step can prove
// the restart half ran. restartErr lets a scenario simulate "no running controller".
type restartSpy struct {
	calls      int
	restartErr error
}

// restartBoard is a live board over a Stuck RST-1/0001 attempt plus the spy wired into
// its web server via WithRestart, and whether its escalation is the auto-stop kind.
type restartBoard struct {
	*board
	spy     *restartSpy
	autoTag bool
}

// seedRestartBoard stands up a live web server over an attempt that is Stuck on a
// single open escalation — tagged as a context-window auto-stop when autoTag is set.
// restartErr seeds the injected restart's outcome (nil = schedules cleanly, non-nil =
// could not be scheduled). The restart is a spy so the Then steps verify it ran
// without needing a real daemon.
func seedRestartBoard(w *World, autoTag bool, restartErr error) (*restartBoard, error) {
	dataDir, err := os.MkdirTemp("", "rst-data-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(rsTicket, rsAtt); err != nil {
		return nil, err
	}
	spec := "---\nid: " + rsTicket + "\ntitle: drv-028 restart on auto-stop\n---\n\n# drv-028\n"
	if err := os.WriteFile(root.SpecPath(rsTicket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + rsAtt + "\nticket: " + rsTicket + "\nrepo: " + dataDir + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(rsTicket, rsAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	if _, err := ticketlog.Append(root, rsTicket, rsAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return nil, err
	}
	esc := event.Event{Type: "escalation", Actor: "agent:claude-code", Body: "Context-window auto-stop: the session's context fill reached 151,000 tokens…"}
	if autoTag {
		esc.Tags = []string{limit.TagContextAutoStop}
	} else {
		esc.Body = "which base image should CI use?"
	}
	if _, err := ticketlog.Append(root, rsTicket, rsAtt, esc); err != nil {
		return nil, err
	}

	spy := &restartSpy{restartErr: restartErr}
	srv, err := web.New(root, web.WithRestart(func(_ context.Context, _, _ string) error {
		spy.calls++
		return spy.restartErr
	}))
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	return &restartBoard{board: &board{root: root, wd: dataDir, srv: ts}, spy: spy, autoTag: autoTag}, nil
}

func setRestartBoard(w *World, autoTag bool, restartErr error) error {
	b, err := seedRestartBoard(w, autoTag, restartErr)
	if err != nil {
		return err
	}
	w.set("restartboard", b)
	return nil
}

func givenAutoStopBoard(w *World, sr *StepRun) error   { return setRestartBoard(w, true, nil) }
func givenOrdinaryEscBoard(w *World, sr *StepRun) error { return setRestartBoard(w, false, nil) }
func givenAutoStopBoardNoController(w *World, sr *StepRun) error {
	return setRestartBoard(w, true, errors.New("no running `ctl up`"))
}

func currentRestartBoard(w *World) (*restartBoard, error) {
	b, _ := w.get("restartboard").(*restartBoard)
	if b == nil {
		return nil, errors.New("no restart board set up in scope")
	}
	return b, nil
}

func fetchAttemptPage(b *restartBoard) (string, error) {
	resp, err := http.Get(b.url("/ticket/" + rsTicket + "/" + rsAtt))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("attempt page status = %d, want 200", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	return string(raw), err
}

// shootRestart screenshots the live attempt page (the log region carrying the
// escalation is always visible — no tab to select) and attaches it as the artefact.
func shootRestart(w *World, sr *StepRun, name, waitSel, clickSel, afterWait string) error {
	b, err := currentRestartBoard(w)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	png, err := webshot.Capture(ctx, b.url("/ticket/"+rsTicket+"/"+rsAtt), webshot.Options{
		Width: 1280, Height: 1000, FullPage: true,
		WaitSelector:           waitSel,
		ClickSelector:          clickSel,
		AfterClickWaitSelector: afterWait,
	})
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png",
		Name:     name,
		Data:     base64.StdEncoding.EncodeToString(png),
	})
	return nil
}

func thenOffersResolveAndRestart(w *World, sr *StepRun) error {
	b, err := currentRestartBoard(w)
	if err != nil {
		return err
	}
	body, err := fetchAttemptPage(b)
	if err != nil {
		return err
	}
	for _, want := range []string{
		fmt.Sprintf(`data-testid="resolve-submit-%d"`, rsSeq),
		fmt.Sprintf(`data-testid="restart-context-submit-%d"`, rsSeq),
	} {
		if !strings.Contains(body, want) {
			return fmt.Errorf("auto-stop escalation page missing %q", want)
		}
	}
	return shootRestart(w, sr, "auto-stop-both-actions", fmt.Sprintf(`[data-testid="restart-context-submit-%d"]`, rsSeq), "", "")
}

func thenOffersResolveOnly(w *World, sr *StepRun) error {
	b, err := currentRestartBoard(w)
	if err != nil {
		return err
	}
	body, err := fetchAttemptPage(b)
	if err != nil {
		return err
	}
	if !strings.Contains(body, fmt.Sprintf(`data-testid="resolve-submit-%d"`, rsSeq)) {
		return errors.New("ordinary escalation should still offer Resolve")
	}
	if strings.Contains(body, fmt.Sprintf(`data-testid="restart-context-submit-%d"`, rsSeq)) {
		return errors.New("ordinary escalation must NOT offer the Restart context window button")
	}
	return shootRestart(w, sr, "ordinary-resolve-only", fmt.Sprintf(`[data-testid="resolve-submit-%d"]`, rsSeq), "", "")
}

// whenClickRestart drives the real button: in a live browser it clicks "Restart
// context window" and waits for the restart-result banner to swap in. Waiting on the
// banner both lets the POST finish (its durable resolution is then on disk for the
// Then step) and guarantees the shot shows the outcome.
func whenClickRestart(w *World, sr *StepRun) error {
	return shootRestart(w, sr, "restart-clicked",
		fmt.Sprintf(`[data-testid="restart-context-submit-%d"]`, rsSeq),
		fmt.Sprintf(`[data-testid="restart-context-submit-%d"]`, rsSeq),
		`#restart-result[data-kind]`)
}

func thenResolvedAndRestarted(w *World, sr *StepRun) error {
	b, err := currentRestartBoard(w)
	if err != nil {
		return err
	}
	// Durable half: the escalation is resolved, so the attempt has left Stuck.
	a, err := project.LoadAttempt(b.root, rsTicket, rsAtt)
	if err != nil {
		return err
	}
	if a.State != project.Running {
		return fmt.Errorf("after restart, state = %q, want Running (left Stuck)", a.State)
	}
	if len(a.OpenEscalations) != 0 {
		return fmt.Errorf("restart should resolve the escalation, but %d remain open", len(a.OpenEscalations))
	}
	// Restart half: the injected restart was handed this attempt exactly once.
	if b.spy.calls != 1 {
		return fmt.Errorf("restart calls = %d, want exactly 1", b.spy.calls)
	}
	return nil
}

func thenResolvedDespiteFailure(w *World, sr *StepRun) error {
	b, err := currentRestartBoard(w)
	if err != nil {
		return err
	}
	// The restart was attempted and failed...
	if b.spy.calls != 1 {
		return fmt.Errorf("restart calls = %d, want exactly 1 (it must be attempted)", b.spy.calls)
	}
	// ...yet the durable half still landed: the escalation is resolved, never left open.
	a, err := project.LoadAttempt(b.root, rsTicket, rsAtt)
	if err != nil {
		return err
	}
	if a.State != project.Running {
		return fmt.Errorf("state = %q, want Running — the escalation must resolve even on a failed restart", a.State)
	}
	if len(a.OpenEscalations) != 0 {
		return fmt.Errorf("a failed restart must not leave the escalation open (%d open)", len(a.OpenEscalations))
	}
	// The page reports the failure in the restart-result banner.
	body, err := fetchAttemptPage(b)
	if err != nil {
		return err
	}
	if !strings.Contains(body, fmt.Sprintf(`data-testid="resolved-%d"`, rsSeq)) {
		return errors.New("the resolved escalation should show its resolved note")
	}
	return nil
}
