package acceptance

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/limit"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

// steps_restartcontext_drv046.go realises decision #25's board-card "Restart context
// window" control: a card whose attempt auto-stopped on a full context window (an open
// escalation tagged limit.TagContextAutoStop) shows a one-click control that resolves the
// escalation and restarts the attempt on a clean window — the board twin of the attempt
// page's drv-028 button — without opening the attempt. A card with no such escalation
// shows no control. It drives the LIVE webui end to end (real handleRestartContext with
// ?surface=card re-rendering the board) and screenshots the control.
//
// The acceptance server wires no WithRestart, so the restart itself reports "unavailable"
// (the default) — but the DURABLE half (resolving the escalation) still lands, which is
// exactly what the board re-render reflects: the card leaves Needs-me and the control
// disappears. That degraded-restart path is the same one drv-028 pins at the unit layer.

const (
	rcStopTicket  = "RST-STOP"  // attempt auto-stopped on a full context window
	rcPlainTicket = "RST-PLAIN" // an ordinary escalation — no restart-context control
	rcEscSeq      = 2           // created (#1) then the escalation (#2)
)

func init() {
	drv046rc := map[string]StepFunc{
		"a card whose attempt auto-stopped on a full context window":                                         givenRCAutoStopCard,
		"the card shows a restart-context control, absent on a card with an ordinary escalation":              thenRCControlShownNotOnPlain,
		"I click the restart-context control":                                                                whenRCClickControl,
		"the escalation is resolved and the card no longer shows the control, without opening the attempt page": thenRCResolvedControlGone,
	}
	for text, fn := range drv046rc {
		registry[text] = fn
	}
}

// givenRCAutoStopCard seeds a live board with two escalated cards: RST-STOP carries a
// context-window auto-stop escalation (the restart-context control shows), RST-PLAIN an
// ordinary one (it does not).
func givenRCAutoStopCard(w *World, sr *StepRun) error {
	b, err := seedLedgerBoard(w, func(root store.Root, repo string) error {
		if err := addTypedTicket(root, repo, rcStopTicket, ""); err != nil {
			return err
		}
		if err := cbAppend(root, rcStopTicket, event.Event{
			Type: "escalation",
			Body: "Context-window auto-stop: the session's context fill reached its cap.",
			Tags: []string{limit.TagContextAutoStop},
		}); err != nil {
			return err
		}
		if err := addTypedTicket(root, repo, rcPlainTicket, ""); err != nil {
			return err
		}
		return cbAppend(root, rcPlainTicket, event.Event{Type: "escalation", Body: "which trunk is authoritative?"})
	})
	if err != nil {
		return err
	}
	w.set("board", b)
	w.set("rcboard", b)
	return nil
}

func currentRCBoard(w *World) (*board, error) {
	b, _ := w.get("rcboard").(*board)
	if b == nil {
		return nil, fmt.Errorf("no restart-context board set up in scope")
	}
	return b, nil
}

func thenRCControlShownNotOnPlain(w *World, sr *StepRun) error {
	b, err := currentRCBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.url("/"))
	if err != nil {
		return err
	}
	stop := "restart-context-btn-" + rcStopTicket + "-" + cbAtt
	plain := "restart-context-btn-" + rcPlainTicket + "-" + cbAtt
	if !strings.Contains(body, stop) {
		return fmt.Errorf("the auto-stopped card should show the restart-context control (data-testid %q)\n%s", stop, body)
	}
	if strings.Contains(body, plain) {
		return fmt.Errorf("a card with an ordinary escalation must NOT show the restart-context control (%q)", plain)
	}
	// The control posts the escalation seq to the existing route with ?surface=card.
	if !strings.Contains(body, "/ticket/"+rcStopTicket+"/"+cbAtt+"/restart-context?surface=card") {
		return fmt.Errorf("the control must post to /restart-context?surface=card")
	}
	return shoot(w, sr, "/", "board-controls-restart-context", true, "", "")
}

// whenRCClickControl drives the real board-origin restart: a same-origin POST (no Origin
// header passes the guard) to the restart-context route with ?surface=card and the
// escalation seq, exactly as the card's form does.
func whenRCClickControl(w *World, sr *StepRun) error {
	b, err := currentRCBoard(w)
	if err != nil {
		return err
	}
	resp, err := http.Post(
		b.url("/ticket/"+rcStopTicket+"/"+cbAtt+"/restart-context?surface=card"),
		"application/x-www-form-urlencoded",
		strings.NewReader("seq="+fmt.Sprint(rcEscSeq)),
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST restart-context?surface=card: status %d\n%s", resp.StatusCode, raw)
	}
	w.set("rcresp", string(raw))
	return nil
}

func thenRCResolvedControlGone(w *World, sr *StepRun) error {
	b, err := currentRCBoard(w)
	if err != nil {
		return err
	}
	// Durable half: the escalation is resolved, so the attempt has no open escalation.
	a, err := project.LoadAttempt(b.root, rcStopTicket, cbAtt)
	if err != nil {
		return err
	}
	if len(a.OpenEscalations) != 0 {
		return fmt.Errorf("the restart-context click must resolve the escalation; still %d open", len(a.OpenEscalations))
	}
	// The board-origin response re-rendered the whole board (not the attempt page), and
	// the resolved card no longer carries the control.
	body, _ := w.get("rcresp").(string)
	if !strings.Contains(body, "card-"+rcStopTicket+"-"+cbAtt) {
		return fmt.Errorf("the ?surface=card response should re-render the board card, got:\n%s", body)
	}
	if strings.Contains(body, "restart-context-btn-"+rcStopTicket+"-"+cbAtt) {
		return fmt.Errorf("the resolved card must no longer show the restart-context control")
	}
	if strings.Contains(body, `data-testid="restart-result"`) {
		return fmt.Errorf("a board-origin restart must not emit the attempt-page restart-result banner")
	}
	return nil
}
