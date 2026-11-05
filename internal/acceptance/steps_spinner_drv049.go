package acceptance

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// steps_spinner_drv049.go realises drv-049's acceptance criteria
// (features/promotions/spinner-durable.feature): the promotion-check spinner is a
// DURABLE, server-rendered state, not a transient htmx-request class the 3s morph poll
// reconciles away. Each scenario stands up the same real managed-worktree board the
// drv-046 steps use (seedBCBoard), seeds a durable `promotion-check` started marker on
// the attempt log, and asserts the live server re-renders the spinning `run` chip — the
// fragment every poll's morph reconciles TO. A terminal verdict settles it; an abandoned
// marker past the TTL does not spin; and the attempt-page Check button runs confirm-free
// with its own in-progress spinner mechanism. Steps register from init().

const spnTicket = "SPN-1" // a Review attempt whose repo declares a single dev promotion

func init() {
	drv049 := map[string]StepFunc{
		"a Review attempt whose repo declares a dev promotion":                       givenSPNDevPromotion,
		"a promotion check for dev is in progress":                                   givenSPNInProgress,
		"an abandoned promotion check for dev whose process died before any verdict": givenSPNAbandoned,
		"the dev chip renders the spinning run state from server markup":             thenSPNChipRun,
		"the spinner reveal does not rely on the transient htmx-request class":       thenSPNServerDrivenReveal,
		"every board poll re-render keeps drawing the spinning dev chip":             thenSPNSurvivesPoll,
		"the dev check records a passing verdict at HEAD":                            whenSPNPassVerdict,
		"the dev chip settles to green and no longer spins":                          thenSPNSettlesGreen,
		"the dev chip does not spin and settles to its not-yet-checked state":        thenSPNAbandonedSettles,
		"I open the attempt page promotion tab":                                      noopPromote,
		"the dev Check button carries no confirmation dialog":                        thenSPNNoConfirm,
		"the dev Check button carries the in-progress spinner mechanism":             thenSPNCheckSpinner,
	}
	for text, fn := range drv049 {
		registry[text] = fn
	}
	// "When I open the board" is already registered by drv-043 (noopPromote) — the board
	// is served live, so opening it is a no-op in-process; the Then steps fetch it.
}

func givenSPNDevPromotion(w *World, sr *StepRun) error {
	_, err := seedBCBoard(w, spnTicket, bcPyramidYAML("dev"))
	return err
}

// givenSPNInProgress appends the durable, server-rendered in-progress marker the board
// chip spinner folds from: a `promotion-check` started event at HEAD, stamped now, so
// the live server renders the dev chip in its spinning `run` state.
func givenSPNInProgress(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	_, err = ticketlog.Append(b.root, b.ticket, bcAtt, event.Event{
		Type: "promotion-check", Actor: "agent:acceptance", Environment: "dev",
		Commit: b.head, Outcome: "started", Body: "Promotion check to `dev` started.",
	})
	return err
}

// givenSPNAbandoned appends a started marker with a timestamp older than
// promoCheckStaleAfter — the crashed-check case: a marker with no terminal/cleared event
// whose process died. ticketlog.Append honours a pre-set TS, so the marker lands in the
// past and the render-time TTL must reconcile it to cleared.
func givenSPNAbandoned(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	old := time.Now().Add(-30 * time.Minute) // well past the 15m TTL
	_, err = ticketlog.Append(b.root, b.ticket, bcAtt, event.Event{
		Type: "promotion-check", Actor: "agent:acceptance", Environment: "dev",
		Commit: b.head, Outcome: "started", TS: old, Body: "Promotion check to `dev` started (abandoned).",
	})
	return err
}

func whenSPNPassVerdict(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	_, err = ticketlog.Append(b.root, b.ticket, bcAtt, event.Event{
		Type: "promotion-result", Actor: "agent:acceptance", Environment: "dev",
		Commit: b.head, Outcome: "pass", Body: "Promotion to `dev` **verified** at HEAD.",
	})
	return err
}

func thenSPNChipRun(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.url("/"))
	if err != nil {
		return err
	}
	cls, err := spnChipClass(body, b.ticket, "dev")
	if err != nil {
		return err
	}
	if cls != "run" {
		return fmt.Errorf("an in-progress dev check should render the spinning run chip, got data-promo-class=%q\n%s", cls, body)
	}
	// The spinner element must still ship (the CSS reveals it on the run class).
	if !strings.Contains(body, "promo-spin-"+b.ticket+"-"+bcAtt+"-dev") {
		return fmt.Errorf("the run chip must still carry its inline spinner element")
	}
	attachText(sr, "spinner-durable-run-chip",
		"in-progress dev promotion renders data-promo-class=\"run\" from server markup (morph reconciles TO the spinning chip)")
	return shoot(w, sr, "/", "spinner-durable-run-chip", true, "", "")
}

// thenSPNServerDrivenReveal proves the spinner reveal has a server-driven path that does
// not depend on the transient client class: the stylesheet reveals the spinner on the
// durable `.promo-run` chip, so a poll re-render (which strips .htmx-request) keeps it.
func thenSPNServerDrivenReveal(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	if err := bcAssertStyleContains(b, ".promo-run .promo-spin"); err != nil {
		return err
	}
	attachText(sr, "spinner-durable-server-reveal",
		".promo-run .promo-spin reveals the spinner from durable server state — independent of the transient .htmx-request class the morph poll reconciles away")
	return nil
}

// thenSPNSurvivesPoll fetches the board twice — standing in for successive 3s poll ticks —
// and asserts the dev chip renders the spinning run state BOTH times. Because the spinning
// chip is in the server fragment, every morph reconciles to it; the pre-drv-049 transient
// spinner would have been clobbered by the second tick.
func thenSPNSurvivesPoll(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	for tick := 1; tick <= 2; tick++ {
		body, err := promGetBody(b.url("/"))
		if err != nil {
			return err
		}
		cls, err := spnChipClass(body, b.ticket, "dev")
		if err != nil {
			return err
		}
		if cls != "run" {
			return fmt.Errorf("poll re-render #%d dropped the spinning dev chip (data-promo-class=%q) — the morph clobbered it", tick, cls)
		}
	}
	attachText(sr, "spinner-durable-survives-poll",
		"two successive /board renders (standing in for 3s poll ticks) both carry the run chip — the spinner is morph-invariant because it is server state")
	return nil
}

func thenSPNSettlesGreen(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.url("/"))
	if err != nil {
		return err
	}
	cls, err := spnChipClass(body, b.ticket, "dev")
	if err != nil {
		return err
	}
	if cls != "ok" {
		return fmt.Errorf("after a passing verdict the dev chip should settle to green (ok), got %q\n%s", cls, body)
	}
	return shoot(w, sr, "/", "spinner-durable-settled-green", true, "", "")
}

func thenSPNAbandonedSettles(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.url("/"))
	if err != nil {
		return err
	}
	cls, err := spnChipClass(body, b.ticket, "dev")
	if err != nil {
		return err
	}
	if cls == "run" {
		return fmt.Errorf("an abandoned marker older than the TTL must not keep the chip spinning\n%s", body)
	}
	// With no prior verdict it falls back to the grey not-yet-checked chip.
	if cls != "none" {
		return fmt.Errorf("an abandoned first-ever check should settle to the not-run (none) chip, got %q", cls)
	}
	attachText(sr, "spinner-durable-abandoned-settled",
		"an in-progress marker older than promoCheckStaleAfter is reconciled to cleared at render time: the chip falls back to its not-yet-checked state, never spins forever")
	return nil
}

func thenSPNNoConfirm(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.url("/ticket/" + b.ticket + "/" + bcAtt))
	if err != nil {
		return err
	}
	// The Check button must render for dev...
	tag, err := spnPromoteButtonTag(body)
	if err != nil {
		return err
	}
	// ...with no confirm dialog (decision #25, mirroring drv-046's board-chip removal).
	// Scope the check to THIS button's opening tag — the attempt page carries hx-confirm
	// on other controls (e.g. the review-env lifecycle), which is not what drv-049 touches.
	if strings.Contains(tag, "hx-confirm") {
		return fmt.Errorf("the attempt-page Check button must carry no hx-confirm (a check is a read-only verify)\n%s", tag)
	}
	attachText(sr, "spinner-durable-no-confirm",
		"attempt-page dev Check button renders with no hx-confirm — a promotion check is a read-only verify (no deploy/merge/push), decision #25")
	return nil
}

func thenSPNCheckSpinner(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.url("/ticket/" + b.ticket + "/" + bcAtt))
	if err != nil {
		return err
	}
	needles := []string{
		`hx-indicator="find .promo-spin"`, // the Check button binds its spinner to the in-flight POST
		"promote-spin-dev",                // the inline spinner element inside the button
	}
	for _, n := range needles {
		if !strings.Contains(body, n) {
			return fmt.Errorf("the attempt-page Check button is missing its in-progress mechanism %q\n%s", n, body)
		}
	}
	return shoot(w, sr, "/ticket/"+b.ticket+"/"+bcAtt, "spinner-durable-check-button", true, "", "")
}

// spnPromoteButtonTag returns the opening <button …> tag of the dev Check button, so an
// attribute assertion is scoped to that control alone. It slices from the `<button` that
// encloses the action-promote-dev testid to the first `>` that closes the opening tag.
func spnPromoteButtonTag(body string) (string, error) {
	i := strings.Index(body, "action-promote-dev")
	if i < 0 {
		return "", fmt.Errorf("the attempt page should render the dev Check button (action-promote-dev)\n%s", body)
	}
	start := strings.LastIndex(body[:i], "<button")
	if start < 0 {
		return "", fmt.Errorf("could not find the opening <button tag of the dev Check button")
	}
	rest := body[start:]
	end := strings.IndexByte(rest, '>')
	if end < 0 {
		return "", fmt.Errorf("the dev Check button's opening tag is unterminated")
	}
	return rest[:end+1], nil
}

// spnChipClass extracts the data-promo-class value of the promotion chip for env on the
// given attempt's card. It locates the chip by its data-testid (promo-TICKET-ATT-ENV) and
// reads the data-promo-class attribute the template emits immediately after it.
func spnChipClass(body, ticket, env string) (string, error) {
	testid := "promo-" + ticket + "-" + bcAtt + "-" + env
	marker := `data-testid="` + testid + `"`
	i := strings.Index(body, marker)
	if i < 0 {
		return "", fmt.Errorf("board should render the %q promotion chip (data-testid %q)\n%s", env, testid, body)
	}
	rest := body[i+len(marker):]
	const attr = `data-promo-class="`
	j := strings.Index(rest, attr)
	if j < 0 {
		return "", fmt.Errorf("the %q chip is missing its data-promo-class attribute", env)
	}
	rest = rest[j+len(attr):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return "", fmt.Errorf("the %q chip's data-promo-class is unterminated", env)
	}
	return rest[:end], nil
}
