package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/limit"
	"github.com/Dawil/draiver/internal/project"
)

// canEnable reports whether an attempt's card should show the green play button:
// a genuinely *parked* attempt a human could opt into daemon supervision. That is
// the effective (Control) state, not just the log state — so it gates on all four
// bits, not the durable pair alone:
//
//   - Running + !Enabled — the durable log bits (a.State, DeriveEnabled): not
//     directly enabled, and not in any other column.
//   - !Live + !Desired — the runtime bits board() folds over the same card
//     (attemptLive + DeriveDesired, drvctl-038). A wants:/after:-activated attempt
//     is supervised *through its parent* and never gets a direct enable event, so
//     a.Enabled == false; once the daemon spawns its agent it is Live and Desired.
//     Without these two the button would leak onto that already-running card
//     (drvweb-022).
//
// It gates both the rendered button and — via handleEnable's no-op — the effect
// of the POST, so the two can never disagree about which cards are actionable. The
// write path loads without the runtime bits, so handleEnable must re-derive Live/
// Desired (fillRuntime) before this check or the POST fires for a buttonless card.
//
// A codebase ledger (drv-037) is never enabled — it has no actuation loop — so it
// is excluded explicitly (drv-039). restingState already rewrites its log state
// Running→Pending, so the a.State==Running arm would never fire for one in any
// case; the type guard makes that intent robust rather than incidental, and gives
// the write path the same single gate to refuse an enable POST against a ledger.
func canEnable(a project.Attempt) bool {
	return a.Type != project.TypeCodebase &&
		a.State == project.Running && !a.Enabled && !a.Live && !a.Desired
}

// canArchive reports whether an attempt's card should show the archive affordance
// (the tick/cross): any attempt still on the board, i.e. not already archived. An
// archived attempt is emitted by no column (board() skips it), so in practice every
// rendered card is archivable; the !Archived gate is the idempotency guard the
// handler shares (handleArchive's no-op), matching canEnable — the button and the
// effect never disagree about which cards write.
func canArchive(a project.Attempt) bool {
	return !a.Archived
}

// restartCtxVM drives the board card's "Restart context window" control (decision #25):
// it carries the seq of the open escalation the control resolves-and-restarts.
type restartCtxVM struct{ Seq int }

// restartContext reports the open context-window auto-stop escalation an attempt card
// should offer a one-click "Restart context window" control for, or nil when there is
// none. It is the board twin of the attempt page's per-escalation ContextAutoStop
// button (drv-028): the control shows only when the attempt has filled its context
// window and auto-stopped (an OPEN escalation carrying limit.TagContextAutoStop), so a
// reviewer can clear it and bring the attempt back on a clean window straight from the
// dashboard. It returns the first such escalation; the handler re-validates the seq
// (classifyResolveTarget + isContextAutoStop), so the control and the POST agree.
func restartContext(a project.Attempt) *restartCtxVM {
	for _, o := range a.OpenEscalations {
		if slices.Contains(o.Tags, limit.TagContextAutoStop) {
			return &restartCtxVM{Seq: o.Seq}
		}
	}
	return nil
}

// archiveVM drives the card's archive affordance and encodes its sentiment. The
// glyph/colour is column-derived: a Done card accepts (green tick), an active card
// (Running/Stuck/Review) closes (grey cross). It keeps the template logic-free —
// the confirm copy, aria-label, colour class, and the Outcome the POST records are
// all chosen here — and it is the single source the handler reuses to stamp the
// event's Outcome, so the rendered sentiment and the recorded one cannot diverge.
type archiveVM struct {
	Outcome string // "accepted" (Done) | "abandoned" (active) — the event's sentiment
	Class   string // colour-selection class: archive-accept | archive-close
	Accept  bool   // true => tick (Done); false => cross (active columns)
	Aria    string // aria-label — the action's meaning, not colour alone
	Title   string // hover tooltip
	Confirm string // hx-confirm copy, sentiment-specific
}

// archiveAction is the colour-selection + copy helper for the card's archive
// button: green tick + accept copy for a Done card, grey cross + close copy for a
// Running/Stuck/Review one. The colours themselves are not chosen here — the Class
// maps to a CSS rule that pulls from the --dot-* palette custom properties (green
// var(--dot-running), muted grey var(--dot-disabled)), so no hex is hand-typed.
func archiveAction(a project.Attempt) archiveVM {
	if a.State == project.Done {
		return archiveVM{
			Outcome: "accepted", Class: "archive-accept", Accept: true,
			Aria: "accept and archive", Title: "Accept and archive",
			Confirm: "Accept this attempt and archive it?",
		}
	}
	return archiveVM{
		Outcome: "abandoned", Class: "archive-close", Accept: false,
		Aria: "archive attempt", Title: "Close and remove from board",
		Confirm: "Close this attempt and remove it from the board?",
	}
}

// handleEnable is the board's enable write (POST /ticket/{id}/{attempt}/enable): it
// opts a Running, disabled attempt into daemon supervision through the in-process
// gateway (s.gw.Enable, drv-008) — the same operation `draiver ctl enable` runs, so
// the enable is not reimplemented in the web layer — then re-renders the card so
// htmx swaps the play button away and the grey session-dot flips. The
// bare enable is purely declarative: no control socket, no reconciler; a running
// `ctl up` brings the attempt up on its next tick, and with no daemon the enable
// simply waits, matching the grey-dot semantics the button sits on.
//
// It is idempotent: an already-enabled (or non-Running) attempt — e.g. a
// double-click racing the 3s board poll — is a no-op that just re-renders the
// current card, so the chain gains exactly one enable per enable. The gate is
// canEnable, checked here before the gateway write (the gateway's `Enable` would
// append unconditionally), so the button and the effect can never disagree. The
// state-changing POST carries a same-origin guard so a cross-site page in a
// browser cannot drive it, proportionate to a localhost dev tool.
func (s *Server) handleEnable(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request blocked", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	// LoadAttempt leaves Live/Desired at their false defaults, but canEnable now
	// gates on them (drvweb-022): re-derive the runtime bits as board() does so the
	// write path sees the same effective state the card render did — otherwise a
	// via-parent-activated (Desired+Live) attempt's POST would still fire though its
	// card shows no button.
	if err := s.fillRuntime(&a); err != nil {
		s.fail(w, err)
		return
	}
	// Only the disabled→enabled transition writes; canEnable is the same gate the
	// button renders behind, so a POST for a card that shows no button is a no-op.
	if canEnable(a) {
		if _, err := s.gw.Enable(id, att); err != nil {
			s.fail(w, err)
			return
		}
		if a, err = project.LoadAttempt(s.root, id, att); err != nil {
			s.fail(w, err)
			return
		}
	}
	// Re-render just this card; htmx swaps it in place (outerHTML on .card-wrap),
	// dropping the button and recomputing the session-dot off the fresh state. Carry
	// the viewer's favourite bit (drv-050) so the star glyph/label stay correct on the
	// swapped-in card fragment.
	a.Favourited = parseFaves(r)[faveRef(a.Ticket, a.ID)]
	s.render(w, "card", a)
}

// handleArchive takes an attempt off the board (POST
// /ticket/{id}/{attempt}/archive): the card's tick (Done → accepted) or cross
// (active → abandoned) both post here, and the sentiment is derived from the
// attempt's current State so the client never chooses the outcome. Like every
// other web write it goes through the in-process gateway (s.gw.Archive, drv-008) —
// the single implementation of "what an archive is" the `draiver archive` CLI verb
// also calls (drvctl-043) — which folds in the board actor, derives the
// accepted|abandoned Outcome from State (outcome ""), and owns the idempotency
// no-op. So the event is byte-identical whether authored from the board or a
// terminal, and metrics can tell an accepted close from an abandoned one.
//
// It is idempotent like handleEnable: canArchive (!Archived) is the same gate the
// button renders behind, so a re-POST racing the 3s board poll is a no-op that
// still re-renders the board — the shell-out only fires on an on-board attempt.
// Archive is orthogonal to lifecycle state — it does not close or reopen the
// attempt, only removes it from board(); an `unarchive` event (DeriveArchived,
// last-wins) brings it back. The response is the re-rendered board region
// (board.html) so htmx swaps the card away in place and the column counts update in
// the same swap; the same-origin guard matches the other writes.
func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Only an on-board attempt writes; canArchive is the same gate the button renders
	// behind, so a POST for an already-archived card (a double-submit) is a no-op that
	// just re-renders the board. The sentiment is server-derived from State inside the
	// gateway (outcome ""), never the posted form — the same single implementation of
	// "what an archive is" the CLI verb calls. The library also owns the idempotency
	// no-op, so canArchive here is just the render/effect-agreement gate.
	if canArchive(a) {
		if _, err := s.gw.Archive(id, att, ""); err != nil {
			s.fail(w, err)
			return
		}
	}
	// Re-render the whole board region: the archived card is gone (board() skips it)
	// and the column counts reflect the removal in one htmx swap. Keep the favicon in
	// sync like handleBoardPartial — archiving a Stuck card can change the variant.
	// Render in the operator's CURRENT view (drv-039) — read off HX-Current-URL, since
	// the archive POST carries no view of its own — so archiving a ledger from the
	// ledgers view does not flip the board back to code tickets under the operator.
	vm, err := s.board(viewFromCurrentURL(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	emitFaviconTrigger(w, vm)
	applyBoardOrder(&vm, parseFaves(r), parseSort(r))
	s.render(w, "board.html", vm)
}

// refusalMessage trims a shelled verb's error to the human-facing sentence: cobra
// prefixes its RunE errors with "Error: " on stderr, which is noise once the text
// is shown in the panel's own error row.
func refusalMessage(err error) string {
	return strings.TrimSpace(strings.TrimPrefix(err.Error(), "Error:"))
}

// handleResolve answers an open escalation from the attempt timeline (POST
// /ticket/{id}/{attempt}/resolve) and returns the re-rendered live fragment, so
// the escalation flips to "resolved by #N", the state badge leaves Stuck, and the
// count bump all land from one swap. It is the board affordance for `draiver
// resolve`: the write goes through the in-process gateway (s.gw.Resolve, drv-008),
// not a re-appended event here, so the board and a terminal share one resolution
// path.
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	seq, err := strconv.Atoi(strings.TrimSpace(r.FormValue("seq")))
	if err != nil {
		http.Error(w, "draiver: escalation seq must be an integer", http.StatusBadRequest)
		return
	}
	answer := strings.TrimSpace(r.FormValue("answer"))
	if answer == "" {
		http.Error(w, "draiver: a resolution needs an answer", http.StatusBadRequest)
		return
	}
	// Guard against the derived truth, never the posted seq: it must name an open
	// escalation on THIS attempt. This rejects an unknown/non-escalation seq and an
	// already-resolved one (a double-submit or forged post) before the shell-out —
	// the resolve box only renders for open escalations, and `draiver resolve`
	// re-checks seq+type once more when it runs.
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	if ok, status, msg := classifyResolveTarget(a, seq); !ok {
		http.Error(w, "draiver: "+msg, status)
		return
	}
	if _, err := s.gw.Resolve(id, att, seq, answer); err != nil {
		s.fail(w, err)
		return
	}
	vm, err := s.detail(id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	live, err := s.executeLive(vm)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(live)
}

// errRestartUnavailable is the default restart outcome when no implementation was
// injected (WithRestart) — the board was built without a controller to hand off
// to. It reads like the no-daemon case because the human-facing effect is the
// same: the escalation is resolved, but the attempt was not brought back up.
var errRestartUnavailable = errors.New("restart unavailable: no running `ctl up` to bring the attempt back up")

// restartAnswer is the resolution body the "Restart context window" button writes.
// The resolve path requires a non-empty answer; this records *why* the escalation
// was cleared so the log reads as a deliberate operator action, not an empty ack.
const restartAnswer = "Cleared via **Restart context window** — resuming the attempt on a clean context window (cold-start brief)."

// restartResultVM drives the "restart-result" banner the restart handler OOB-swaps
// alongside the attempt-live fragment (drv-028), mirroring gitResultVM. Kind —
// "ok" (resolved and restart scheduled) or "fail" (resolved durably, but the
// restart could not be scheduled) — styles the banner; Message is plain text,
// HTML-escaped by the template.
type restartResultVM struct {
	Kind    string
	Message string
}

// handleRestartContext backs the board's "Restart context window" button on a
// context-window auto-stop escalation (drv-028). It is modelled on handleResolve:
// same-origin guard, attempt-exists check, and the open-escalation validation of
// classifyResolveTarget — plus the one extra guard that the target actually carries
// limit.TagContextAutoStop, since the button only renders for that escalation kind.
// It resolves the escalation first (the durable half, via the same gateway Resolve
// handleResolve uses) and only then restarts the attempt on a clean context window.
// A failed restart does NOT reopen the escalation — resolution has already landed —
// it is reported in the OOB restart-result banner, mirroring how the limit gate
// writes the escalation before it halts. The primary swap is the attempt-live
// fragment into [data-testid=log-region], exactly as handleResolve returns.
func (s *Server) handleRestartContext(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	seq, err := strconv.Atoi(strings.TrimSpace(r.FormValue("seq")))
	if err != nil {
		http.Error(w, "draiver: escalation seq must be an integer", http.StatusBadRequest)
		return
	}
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	// The seq must name an open escalation on THIS attempt (same guard as resolve)...
	if ok, status, msg := classifyResolveTarget(a, seq); !ok {
		http.Error(w, "draiver: "+msg, status)
		return
	}
	// ...and it must be a context-window auto-stop (the only kind this button serves).
	// The button never renders elsewhere; this rejects a forged/misrouted post.
	if !isContextAutoStop(a, seq) {
		http.Error(w, fmt.Sprintf("draiver: escalation #%d on %s/%s is not a context-window auto-stop", seq, id, att), http.StatusBadRequest)
		return
	}
	// Resolve first: this is the durable half. Even if the restart cannot be
	// scheduled, the escalation must not be left open (AC).
	if _, err := s.gw.Resolve(id, att, seq, restartAnswer); err != nil {
		s.fail(w, err)
		return
	}
	// Now restart on a clean window. A failure here is an expected outcome (no
	// `ctl up`), not a server fault: report it in the banner, do not 500 and do not
	// reopen the already-resolved escalation.
	result := restartResultVM{Kind: "ok", Message: "Restarting the attempt on a clean context window — cold-starting from brief. Watch it come back up on the board."}
	restartErr := s.restart(r.Context(), id, att)
	if restartErr != nil {
		result = restartResultVM{Kind: "fail", Message: "Resolved, but the restart could not be scheduled — " + cleanRestartErr(restartErr)}
	}
	// drv-046 (decision #25): a board-origin click (?surface=card) re-renders the whole
	// board so the card leaves the Needs-me column as the escalation clears — mirroring
	// handleMarkDone. The escalation is already resolved durably above, so the restart's
	// own success/failure only affects whether the attempt then comes back up; either
	// way the board reflects the resolved state. The attempt-page origin keeps the
	// attempt-live + restart-result banner fragments below.
	if r.URL.Query().Get("surface") == "card" {
		bvm, err := s.board(viewFromCurrentURL(r))
		if err != nil {
			s.fail(w, err)
			return
		}
		emitFaviconTrigger(w, bvm)
		applyBoardOrder(&bvm, parseFaves(r), parseSort(r))
		s.render(w, "board.html", bvm)
		return
	}
	vm, err := s.detail(id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	live, err := s.executeLive(vm)
	if err != nil {
		s.fail(w, err)
		return
	}
	banner, err := s.renderFragment("restart-result", result)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(live)
	w.Write(banner)
}

// cleanRestartErr renders a restart error for the failure banner, trimming the
// internal "reconcile: " plumbing prefix so the message leads with the cause.
func cleanRestartErr(err error) string {
	return strings.TrimPrefix(err.Error(), "reconcile: ")
}

// isContextAutoStop reports whether seq names an escalation on a carrying the
// limit gate's context-auto-stop tag — the one escalation kind the restart button
// serves. It reads the same durable tags the view model keys ContextAutoStop off.
func isContextAutoStop(a project.Attempt, seq int) bool {
	for i := range a.Events {
		if a.Events[i].Seq == seq {
			return a.Events[i].Type == "escalation" && slices.Contains(a.Events[i].Tags, limit.TagContextAutoStop)
		}
	}
	return false
}

// classifyResolveTarget validates a posted seq against the derived attempt before
// the resolve shell-out, without trusting the form. It mirrors the CLI's own
// checks (cmd/resolve.go) and adds the one the CLI lacks: an unknown or
// non-escalation seq is a bad request, an escalation that already has a later
// resolution is a conflict (a double-submit or forged post), and an open
// escalation passes. It reads derived state (Events for seq/type, OpenEscalations
// for openness) — it does not re-append; the write itself stays in the CLI.
func classifyResolveTarget(a project.Attempt, seq int) (ok bool, status int, msg string) {
	var esc *event.Event
	for i := range a.Events {
		if a.Events[i].Seq == seq {
			esc = &a.Events[i]
			break
		}
	}
	if esc == nil {
		return false, http.StatusBadRequest, fmt.Sprintf("no event #%d on %s/%s", seq, a.Ticket, a.ID)
	}
	if esc.Type != "escalation" {
		return false, http.StatusBadRequest, fmt.Sprintf("event #%d on %s/%s is a %q, not an escalation", seq, a.Ticket, a.ID, esc.Type)
	}
	for _, o := range a.OpenEscalations {
		if o.Seq == seq {
			return true, 0, ""
		}
	}
	return false, http.StatusConflict, fmt.Sprintf("escalation #%d on %s/%s is already resolved", seq, a.Ticket, a.ID)
}
