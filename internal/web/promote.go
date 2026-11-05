package web

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/promote"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/repo"
	"github.com/Dawil/draiver/internal/systemlog"
)

// Promotion status is drv-043's new orthogonal projection of the log (DerivePromotions)
// rendered — never gated. A promotion is a separate axis from the testing-V: it informs
// the human's ratification decision, it never flips the review or done gate. Two
// surfaces share one fold here: compact chips beside the Review-column badge on the
// board, and a per-environment promote control on the attempt page in Review.

// --- board chips (part 4) ---

// promoChipVM is one per-environment promotion label on a Review-column card: the
// environment name plus a class/symbol telling a reviewer at a glance whether the
// ticket is promoted-and-current (ok), promoted at an older commit (stale), failed,
// or declared-but-never-run (none). It is the promotion twin of a pyramid rung chip —
// additive presentation. Since drv-046 the chip is also the actuating control: it
// renders as a form button posting the env-scoped promote check, so a chip appears
// for every DECLARED promotion (grey `none` until it is first run), not only once a
// result exists.
type promoChipVM struct {
	Env    string
	Slug   string
	Class  string // "none" | "ok" | "stale" | "fail" | "run"
	Symbol string // "○" | "✓" | "·" | "✕" | "" (run spins instead of a glyph)
	Label  string // screen-reader / tooltip summary
}

// promoCheckStaleAfter bounds how long an in-progress `promotion-check` marker pins
// the spinner (drv-049). A real qa check never approaches this; it exists only to
// reconcile an ABANDONED marker — a check whose process died before recording a
// terminal/cleared event — so a killed check never leaves a chip spinning forever.
// The common blocked/env-fault case is cleared promptly by its own `cleared` marker,
// not by this TTL, so the window can be generous without ever stranding a long check.
const promoCheckStaleAfter = 15 * time.Minute

// promoCheckLive reports whether an in-progress marker should still spin the chip at
// render time: it is in progress AND its start is within the staleness window. A
// marker older than promoCheckStaleAfter is treated as abandoned and reconciled to
// cleared, so the chip falls back to its last recorded verdict.
func promoCheckLive(ps project.PromotionStatus, now time.Time) bool {
	return ps.InProgress && now.Sub(ps.StartedAt) < promoCheckStaleAfter
}

// promoChips is the FuncMap entry the board card calls. It is best-effort presentation
// exactly like pyramidBadge: any resolution or I/O failure yields no chips rather than
// breaking the board, and chips appear only on positive proof of a recorded result.
//
// Chips render only for an attempt in Review (the column the spec scopes them to), and
// only for environments the pyramid still declares a promotion for — a removed/renamed
// promotion's stale events drop off, mirroring the pyramid badge's "known rung" fold.
// A pass whose Commit == live HEAD is green (ok); a pass at an older commit is muted
// (stale — promoted, but the code has moved since); a fail is a red cross.
func (s *Server) promoChips(a project.Attempt) []promoChipVM {
	if a.State != project.Review {
		return nil
	}
	st, ok := s.pyramidGit(context.Background(), a)
	if !ok || st.Pyramid == nil {
		return nil
	}
	return projectPromoChips(st.Pyramid, a.Events, st.Head, time.Now())
}

// projectPromoChips is the pure, I/O-free core of the board-chip projection (the live
// HEAD is read by the caller), so it is exhaustively unit-testable off canned inputs.
// It folds the attempt's promotion-result log (project.DerivePromotions) against the
// pyramid's declared promotions, in declaration order for a deterministic render.
//
// Since drv-046 it emits a chip for EVERY declared promotion — a declared-but-never-run
// env gets a grey `none` chip rather than being skipped — so a reviewer can see (and
// click) a pending promotion. The pyramid's Promotions stay the authority, so an env
// with a recorded result but no current declaration (removed/renamed) still folds off.
func projectPromoChips(p *pyramid.Pyramid, events []event.Event, head string, now time.Time) []promoChipVM {
	if p == nil || len(p.Promotions) == 0 {
		return nil
	}
	statuses := project.DerivePromotions(events)
	var chips []promoChipVM
	for _, pr := range p.Promotions {
		// statuses[env] is the zero PromotionStatus (Outcome == "") when nothing has been
		// run for this env yet — promoChip renders that as the grey `none` chip.
		chips = append(chips, promoChip(pr.Environment, statuses[pr.Environment], head, now))
	}
	return chips
}

// promoChip classifies one environment's latest promotion status against HEAD into its
// rendered chip. No result yet is a grey `none` (declared but never checked); a fail is
// always a cross regardless of commit (the latest verdict was red); a pass is green only
// when its commit still describes the tip, else muted-stale. The glyph (○/✓/·/✕) plus
// the title/aria-label keep every state legible without colour alone.
func promoChip(env string, ps project.PromotionStatus, head string, now time.Time) promoChipVM {
	c := promoChipVM{Env: env, Slug: reviewEnvSlug(env)}
	switch {
	case promoCheckLive(ps, now):
		// drv-049: a durable in-progress marker spins the chip from server state, so the
		// 3s morph poll keeps redrawing the spinning chip until a terminal verdict lands.
		// No glyph — the CSS spinner carries the "in flight" meaning (plus the aria-label).
		c.Class, c.Symbol = "run", ""
		c.Label = "promotion to " + env + " — check in progress"
	case ps.Outcome == "":
		c.Class, c.Symbol = "none", "○"
		c.Label = "promotion to " + env + " not yet checked"
	case ps.Outcome == "fail":
		c.Class, c.Symbol = "fail", "✕"
		c.Label = "promotion to " + env + " failed at " + shortSHA(ps.Commit)
	case ps.Commit == head:
		c.Class, c.Symbol = "ok", "✓"
		c.Label = "promoted to " + env + " at HEAD " + shortSHA(head)
	default:
		c.Class, c.Symbol = "stale", "·"
		c.Label = "promoted to " + env + " at " + shortSHA(ps.Commit) +
			", now stale — HEAD is " + shortSHA(head)
	}
	return c
}

// canMarkDone reports whether a Review card should show the green "mark done" tick that
// ratifies the ticket straight into Done (drv-046) — true iff the attempt is in Review,
// is not a codebase ledger (a ledger cannot be closed via `done`; restingState rewrites
// its Done back to Pending, so the tick must never appear on one — mirroring canEnable),
// and declares ≥1 promotion with EVERY declared promotion green-at-HEAD.
//
// It reuses the §1 board-chip projection (so the tick and the chips can never disagree:
// all chips Class == "ok") and therefore reads live HEAD via promoChips — hence a Server
// method, unlike the pure canEnable/canArchive. It gates both the rendered tick and
// handleMarkDone's POST, re-deriving HEAD each call, so the button and the effect agree.
func (s *Server) canMarkDone(a project.Attempt) bool {
	if a.Type == project.TypeCodebase || a.State != project.Review {
		return false
	}
	chips := s.promoChips(a) // nil off-Review / no declared promotion; git HEAD re-read
	return allPromoGreen(chips)
}

// allPromoGreen is the pure gate canMarkDone folds: at least one declared promotion, and
// every chip green-at-HEAD. An empty set (no declared promotion) is never ratifiable.
func allPromoGreen(chips []promoChipVM) bool {
	if len(chips) == 0 {
		return false
	}
	for _, c := range chips {
		if c.Class != "ok" {
			return false
		}
	}
	return true
}

// --- attempt-page promote controls (part 3) ---

// promoteSurfaceVM is the attempt page's whole promotion surface: one control per
// declared promotion, offered ONLY on an attempt in Review (matching the review-env
// controls — promotion is a Review-time activity). A nil *promoteSurfaceVM renders
// nothing; the tab/panel is absent on every other attempt.
type promoteSurfaceVM struct {
	Attempt project.Attempt
	Panels  []*promotePanelVM
}

// promotePanelVM drives one "promote-panel" partial: a single environment's promote
// button and its current status line, derived from the latest promotion-result folded
// against HEAD. It is the UI face of one promotion — the promotion twin of a review-env
// panel, but stateless (a promotion is a one-shot verify, not a lifecycle), so there is
// no poll, no up/down, only Promote.
type promotePanelVM struct {
	Attempt project.Attempt
	EnvName string
	EnvSlug string
	Base    string // the branch the environment tracks (descriptive, v1)
	// Class is the status styling hook and State the human-facing phase:
	// "none" (not yet promoted) | "ok" (promoted @ HEAD) | "stale" | "fail" | "run".
	Class      string
	StatusLine string
	// Commit/Head are short SHAs for the status line (populated once a result exists).
	Commit string
	Head   string
}

// projectPromoteSurface folds an attempt, its loaded pyramid, and the live HEAD into
// the surface VM. It is the pure gate+projection (the Server method does the git read),
// so the enumeration is unit-testable off canned inputs, mirroring projectReviewEnvs.
// It returns nil — render nothing — for a non-Review attempt or one whose pyramid
// declares no promotion.
func projectPromoteSurface(a project.Attempt, p *pyramid.Pyramid, head string, now time.Time) *promoteSurfaceVM {
	if a.State != project.Review || p == nil || len(p.Promotions) == 0 {
		return nil
	}
	statuses := project.DerivePromotions(a.Events)
	vm := &promoteSurfaceVM{Attempt: a}
	for _, pr := range p.Promotions {
		vm.Panels = append(vm.Panels, projectPromotePanel(a, pr, statuses[pr.Environment], head, now))
	}
	return vm
}

// projectPromotePanel projects one declared promotion plus its latest recorded status
// into a panel VM. A zero PromotionStatus (no result yet) is the resting "not yet
// promoted" state.
func projectPromotePanel(a project.Attempt, pr pyramid.Promotion, ps project.PromotionStatus, head string, now time.Time) *promotePanelVM {
	vm := &promotePanelVM{
		Attempt: a,
		EnvName: pr.Environment,
		EnvSlug: reviewEnvSlug(pr.Environment),
		Base:    pr.Base,
		Head:    shortSHA(head),
	}
	switch {
	case promoCheckLive(ps, now):
		// drv-049: a durable in-progress marker also drives the attempt-page panel, so a
		// fresh load mid-check (e.g. a board-origin check running for this env) shows the
		// same spinning state the board chip does.
		vm.Class = "run"
		vm.StatusLine = "check in progress…"
	case ps.Outcome == "":
		vm.Class = "none"
		vm.StatusLine = "not yet promoted"
	case ps.Outcome == "fail":
		vm.Class = "fail"
		vm.Commit = shortSHA(ps.Commit)
		vm.StatusLine = "failed @ " + vm.Commit
	case ps.Commit == head:
		vm.Class = "ok"
		vm.Commit = shortSHA(ps.Commit)
		vm.StatusLine = "promoted @ " + vm.Commit
	default:
		vm.Class = "stale"
		vm.Commit = shortSHA(ps.Commit)
		vm.StatusLine = "promoted @ " + vm.Commit + " — stale, HEAD is " + vm.Head
	}
	return vm
}

// promoteSurface projects an attempt into its promotion surface VM. It is best-effort
// presentation like reviewEnvSurface/pyramidBadge — a nil result renders nothing. The
// git read behind it is bounded by a background context, matching pyramidBadge.
func (s *Server) promoteSurface(ctx context.Context, a project.Attempt) *promoteSurfaceVM {
	if a.State != project.Review {
		return nil
	}
	st, ok := s.pyramidGit(ctx, a)
	if !ok {
		return nil
	}
	return projectPromoteSurface(a, st.Pyramid, st.Head, time.Now())
}

// promoteResultVM drives the OOB "promote-result" banner, mirroring rerunResultVM:
// Status is the executor's verdict, Class maps it to a styling hook, Message is the
// joined human-readable report lines.
type promoteResultVM struct {
	Status  string
	Class   string
	Message string
}

// promoteClass maps an executor status to a banner styling hook, mirroring rerunClass:
// a green qa is "ok", a red healthcheck or an environment that declares no promotion is
// a neutral "warn" (the ball is back with the human, not a crash), and an env-up fault
// or a failed qa is "bad".
func promoteClass(st promote.Status) string {
	switch st {
	case promote.StatusOK:
		return "ok"
	case promote.StatusBlocked, promote.StatusNoPromotion:
		return "warn"
	default:
		return "bad"
	}
}

// promoteSrc is the POST URL of the promote action for one env on one attempt.
func promoteSrc(ticket, att, env string) string {
	return "/ticket/" + url.PathEscape(ticket) + "/" + url.PathEscape(att) + "/promote/" + url.PathEscape(env)
}

// handlePromote verifies one environment's promotion from the attempt page (POST
// /ticket/{id}/{attempt}/promote/{env}) — drv-043 part 3. It mirrors handleReportRerun
// (the BDD-rerun precedent): a same-origin guard, an attempt-exists check, a Review
// gate, an in-process delegate (promote.Run → internal/promote, never a shell-out in
// the handler), and a double swap — the re-rendered live log fragment plus an OOB
// result banner and a refreshed promotion surface.
//
// Like rerun this action is non-terminal: it produces evidence and never flips a gate.
// Unlike the CLI's `--log` (which records only passes, since --detach is deferred), the
// interactive button records the verdict it observed — a pass OR a fail — as one
// commit-bound promotion-result, so the board's dev✓/dev✕ chip reflects what the
// reviewer just saw. A blocked/env-fault/no-promotion outcome records nothing ("pass
// the ball"), exactly as rerun leaves the report untouched on those.
func (s *Server) handlePromote(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	envName := r.PathValue("env")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Re-check the gate server-side: the control only renders behind it, so a POST for
	// an attempt not in Review, or naming an env with no declared promotion, is a bad
	// request (writing nothing), not a 500.
	st, ok := s.pyramidGit(r.Context(), a)
	if a.State != project.Review || !ok || st.Pyramid == nil {
		http.Error(w, "draiver: promotions are offered only on an attempt in Review", http.StatusBadRequest)
		return
	}
	if _, has := st.Pyramid.Promotion(envName); !has {
		http.Error(w, "draiver: no promotion declares that environment for this attempt", http.StatusBadRequest)
		return
	}

	// drv-046 part 4: record the START of the check as a system event, immediately
	// before promote.Run, naming the env and the commit about to be checked — the
	// durable, real-timestamped twin of the board spinner. It is emitted for every
	// origin (board or attempt page) so the System Logs stream shows the full
	// started → pass|fail arc, not only the terminal verdict below.
	s.emitSystem(id, att, promotionStartedSystemEvent(envName, st.Head))

	// drv-049: record the durable, server-rendered "check in progress" marker on the
	// attempt log — the state the board chip spinner folds from (project.DerivePromotions),
	// so every 3s morph re-render keeps drawing the spinner until the terminal verdict
	// below clears it, instead of the poll reconciling a transient client class away. It
	// is distinct from the system-log twin emitted just above (an operational stream the
	// promotion fold does not read). The terminal branch below clears it on every outcome.
	if _, err := s.gw.AppendAt(id, att, event.Event{
		Type:        "promotion-check",
		Environment: envName,
		Commit:      st.Head,
		Outcome:     "started",
		Body:        "Promotion check to `" + envName + "` started @ `" + shortSHA(st.Head) + "`.",
	}); err != nil {
		s.fail(w, err)
		return
	}

	// drv-048: relay the check's own stdout/stderr into the System Logs stream instead
	// of discarding it, so the qa/healthcheck output (and the reason a promotion was
	// blocked) interleaves, in real recorded time, between the started line above and
	// the pass|fail verdict below. stderr lines take the error role so they read red.
	// Best-effort like emitSystem: a relay write never fails the check it observes.
	relay := func(line string, isErr bool) {
		s.emitSystem(id, att, systemlog.SystemEvent{Class: systemlog.ClassPromotion, Message: line, Error: isErr})
	}
	relayOut := &systemRelay{relay: relay}
	relayErr := &systemRelay{relay: relay, isErr: true}
	outcome, err := promote.Run(r.Context(), s.root, a.Repo, id, att, envName, relayOut, relayErr)
	relayOut.flush()
	relayErr.flush()
	if err != nil {
		s.fail(w, err)
		return
	}

	// If the promotion came from a test-pyramid overlay, record the same provenance the
	// CLI's recordOverlayProvenance writes (overlay repo + the exact commit used), so a
	// board-triggered overlay promotion is as auditable as a CLI one. Best-effort and
	// independent of the verdict — exactly like the CLI, which records on resolution
	// whether or not qa then passes; a write failure never fails the promotion.
	if outcome.Overlay != nil {
		// Best-effort, like the CLI's recordOverlayProvenance: a provenance-write failure
		// is swallowed rather than failing the promotion the reviewer just ran.
		_, _ = s.gw.SetProvenance(id, att, repo.Provenance{
			Overlay:       &outcome.Overlay.Repo,
			OverlayCommit: &outcome.Overlay.Commit,
		})
	}

	// Record the verdict for a pass or a fail — both are commit-bound evidence the
	// board chip reads. A blocked/env-fault/no-promotion outcome is not a promotion
	// verdict, so it writes nothing (the ball is back with the human).
	switch outcome.Status {
	case promote.StatusOK, promote.StatusQAFailed:
		result := "pass"
		if outcome.Status == promote.StatusQAFailed {
			result = "fail"
		}
		if _, err := s.gw.AppendAt(id, att, event.Event{
			Type:        "promotion-result",
			Environment: envName,
			Commit:      outcome.Commit,
			Outcome:     result,
			Body:        webPromotionResultBody(envName, outcome.Base, outcome.Commit, result == "pass"),
		}); err != nil {
			s.fail(w, err)
			return
		}
		// Reload so the re-rendered fragments reflect the just-appended event (the
		// promotion-result also clears the drv-049 in-progress marker via the fold).
		if a, err = project.LoadAttempt(s.root, id, att); err != nil {
			s.fail(w, err)
			return
		}
	default:
		// drv-049: a blocked/env-fault/no-promotion outcome records no durable verdict,
		// but the in-progress marker appended before the run must still be cleared so the
		// chip settles back to its prior resting state (never spins until the TTL). The
		// clear leaves the last recorded verdict untouched (see DerivePromotions).
		if _, err := s.gw.AppendAt(id, att, event.Event{
			Type:        "promotion-check",
			Environment: envName,
			Outcome:     "cleared",
			Body:        "Promotion check to `" + envName + "` ended with no verdict (`" + string(outcome.Status) + "`).",
		}); err != nil {
			s.fail(w, err)
			return
		}
		if a, err = project.LoadAttempt(s.root, id, att); err != nil {
			s.fail(w, err)
			return
		}
	}

	// drv-045: record the *act* of running the promotion check as a system event —
	// env, commit, and the verdict observed — distinct from (and in addition to) the
	// durable promotion-result the pass/fail branch above may have recorded. It is the
	// operational view: it fires for every outcome the check produced, including a
	// blocked/env-fault one that records no durable verdict.
	s.emitSystem(id, att, promotionSystemEvent(envName, outcome))

	// drv-046: a board-origin check (the chip carries ?surface=card) re-renders the whole
	// card so the clicked chip flips grey→green/red and the all-green "mark done" tick
	// appears/disappears in one swap — mirroring handleEnable's closest-.card-wrap /
	// outerHTML swap. The attempt-page origin keeps the original log+OOB fragments below.
	if r.URL.Query().Get("surface") == "card" {
		s.render(w, "card", a)
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
	var buf bytes.Buffer
	buf.Write(live)
	// Refresh the promotion surface OOB so the status line (and any new chip) updates
	// without a full reload.
	if err := s.tmpl.ExecuteTemplate(&buf, "promote-surface-slot", vm.Promote); err != nil {
		s.fail(w, err)
		return
	}
	banner := promoteResultVM{
		Status:  string(outcome.Status),
		Class:   promoteClass(outcome.Status),
		Message: strings.Join(outcome.Lines, "\n"),
	}
	if err := s.tmpl.ExecuteTemplate(&buf, "promote-result", banner); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// promotionSystemEvent composes the drv-045 system-log record for the *act* of a
// promotion check: a ClassPromotion event naming the env, the commit, and the verdict
// the check observed (pass/fail/blocked/env-fault/none). It carries the overlay repo
// as a structured field when the promotion came from a test-pyramid overlay, so a
// board-triggered overlay check is as legible in the stream as a CLI one. A
// qa-failed/env-fault/blocked verdict takes the error role so it reads red.
func promotionSystemEvent(env string, outcome promote.Outcome) systemlog.SystemEvent {
	verdict := string(outcome.Status)
	switch outcome.Status {
	case promote.StatusOK:
		verdict = "pass"
	case promote.StatusQAFailed:
		verdict = "fail"
	}
	fields := map[string]string{"env": env, "verdict": verdict}
	if sha := strings.TrimSpace(outcome.Commit); sha != "" {
		fields["commit"] = shortSHA(sha)
	}
	msg := "promotion check: `" + env + "`"
	if sha := strings.TrimSpace(outcome.Commit); sha != "" {
		msg += " @ `" + shortSHA(sha) + "`"
	}
	msg += " — " + verdict
	if outcome.Overlay != nil {
		fields["overlay"] = outcome.Overlay.Repo
		msg += " (overlay `" + outcome.Overlay.Repo + "`)"
	}
	return systemlog.SystemEvent{
		Class:   systemlog.ClassPromotion,
		Message: msg,
		Fields:  fields,
		Error:   outcome.Status == promote.StatusQAFailed || outcome.Status == promote.StatusEnvFault || outcome.Status == promote.StatusBlocked,
	}
}

// promotionStartedSystemEvent composes the drv-046 "check started" system-log record:
// a ClassPromotion event naming the env and the commit about to be checked, emitted just
// before promote.Run so the stream shows the start of the arc. It is never an error (a
// start carries no verdict) and carries no overlay field — overlay provenance is known
// only after the run, so it rides the terminal promotionSystemEvent, not this one.
func promotionStartedSystemEvent(env, head string) systemlog.SystemEvent {
	fields := map[string]string{"env": env, "verdict": "started"}
	msg := "promotion check: `" + env + "`"
	if sha := strings.TrimSpace(head); sha != "" {
		fields["commit"] = shortSHA(sha)
		msg += " @ `" + shortSHA(sha) + "`"
	}
	msg += " — started"
	return systemlog.SystemEvent{
		Class:   systemlog.ClassPromotion,
		Message: msg,
		Fields:  fields,
	}
}

// handleMarkDone is the board's one-click ratify (POST /ticket/{id}/{attempt}/done,
// drv-046): on an all-green Review card it appends a `done` event through the SAME
// gateway the attempt page's compose box uses (s.gw.Done → drv-008), moving the ticket
// to Done without opening the attempt. A dedicated board route (rather than the generic
// /log) exists only so the response re-renders the WHOLE board region — the card
// relocates from Review to Done in one htmx swap, exactly as handleArchive does — where
// handleLogAppend returns the attempt-page log fragment.
//
// canMarkDone is the authoritative gate (gw.Done enforces no state precondition of its
// own — repo.go just appends): it is re-checked here, re-deriving HEAD, so a POST for a
// card showing no tick (any promotion non-green, or not in Review, or a ledger) is
// refused with a 400 and writes nothing. The same-origin guard matches the other writes.
func (s *Server) handleMarkDone(w http.ResponseWriter, r *http.Request) {
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
	// The gate the tick renders behind, re-checked server-side (it re-reads HEAD via
	// promoChips) so the button and the effect can never disagree: a POST for a
	// non-green / non-Review / ledger card writes nothing and is a bad request, not a 500.
	if !s.canMarkDone(a) {
		http.Error(w, "draiver: mark done is offered only on a Review attempt whose promotions are all green", http.StatusBadRequest)
		return
	}
	if _, err := s.gw.Done(id, att, "Ratified from the board: all promotions green."); err != nil {
		s.fail(w, err)
		return
	}
	// Re-render the whole board region so the card relocates Review → Done in one swap,
	// and keep the favicon in sync like handleArchive. Render in the operator's CURRENT
	// view (HX-Current-URL), since the done POST carries no view of its own.
	vm, err := s.board(viewFromCurrentURL(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	emitFaviconTrigger(w, vm)
	s.render(w, "board.html", vm)
}

// webPromotionResultBody renders a promotion-result event body for the web-recorded
// verdict (the board renders bodies as Markdown). It mirrors cmd.promotionResultBody;
// the web layer cannot import package cmd, so the one-line renderer is re-expressed
// here, deliberately identical in shape.
func webPromotionResultBody(env, base, sha string, pass bool) string {
	verb := "**verified**"
	if !pass {
		verb = "**failed**"
	}
	if strings.TrimSpace(base) != "" {
		return "Promotion to `" + env + "` " + verb + " @ `" + shortSHA(sha) + "` (tracks `" + base + "`)."
	}
	return "Promotion to `" + env + "` " + verb + " @ `" + shortSHA(sha) + "`."
}
