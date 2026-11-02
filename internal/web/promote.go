package web

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/promote"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/repo"
)

// Promotion status is drv-043's new orthogonal projection of the log (DerivePromotions)
// rendered — never gated. A promotion is a separate axis from the testing-V: it informs
// the human's ratification decision, it never flips the review or done gate. Two
// surfaces share one fold here: compact chips beside the Review-column badge on the
// board, and a per-environment promote control on the attempt page in Review.

// --- board chips (part 4) ---

// promoChipVM is one per-environment promotion label on a Review-column card: the
// environment name plus a class/symbol telling a reviewer at a glance whether the
// ticket is promoted-and-current (ok), promoted at an older commit (stale), or failed.
// It is the promotion twin of a pyramid rung chip — additive presentation that only
// appears on a recorded promotion-result, never a gate.
type promoChipVM struct {
	Env    string
	Slug   string
	Class  string // "ok" | "stale" | "fail"
	Symbol string // "✓" | "·" | "✕"
	Label  string // screen-reader / tooltip summary
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
	return projectPromoChips(st.Pyramid, a.Events, st.Head)
}

// projectPromoChips is the pure, I/O-free core of the board-chip projection (the live
// HEAD is read by the caller), so it is exhaustively unit-testable off canned inputs.
// It folds the attempt's promotion-result log (project.DerivePromotions) against the
// pyramid's declared promotions, in declaration order for a deterministic render.
func projectPromoChips(p *pyramid.Pyramid, events []event.Event, head string) []promoChipVM {
	if p == nil || len(p.Promotions) == 0 {
		return nil
	}
	statuses := project.DerivePromotions(events)
	if len(statuses) == 0 {
		return nil
	}
	var chips []promoChipVM
	for _, pr := range p.Promotions {
		ps, ok := statuses[pr.Environment]
		if !ok {
			continue
		}
		chips = append(chips, promoChip(pr.Environment, ps, head))
	}
	return chips
}

// promoChip classifies one environment's latest promotion status against HEAD into its
// rendered chip. A fail is always a cross regardless of commit (the latest verdict was
// red); a pass is green only when its commit still describes the tip, else muted-stale.
func promoChip(env string, ps project.PromotionStatus, head string) promoChipVM {
	c := promoChipVM{Env: env, Slug: reviewEnvSlug(env)}
	switch {
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
	// "none" (not yet promoted) | "ok" (promoted @ HEAD) | "stale" | "fail".
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
func projectPromoteSurface(a project.Attempt, p *pyramid.Pyramid, head string) *promoteSurfaceVM {
	if a.State != project.Review || p == nil || len(p.Promotions) == 0 {
		return nil
	}
	statuses := project.DerivePromotions(a.Events)
	vm := &promoteSurfaceVM{Attempt: a}
	for _, pr := range p.Promotions {
		vm.Panels = append(vm.Panels, projectPromotePanel(a, pr, statuses[pr.Environment], head))
	}
	return vm
}

// projectPromotePanel projects one declared promotion plus its latest recorded status
// into a panel VM. A zero PromotionStatus (no result yet) is the resting "not yet
// promoted" state.
func projectPromotePanel(a project.Attempt, pr pyramid.Promotion, ps project.PromotionStatus, head string) *promotePanelVM {
	vm := &promotePanelVM{
		Attempt: a,
		EnvName: pr.Environment,
		EnvSlug: reviewEnvSlug(pr.Environment),
		Base:    pr.Base,
		Head:    shortSHA(head),
	}
	switch {
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
	return projectPromoteSurface(a, st.Pyramid, st.Head)
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

	outcome, err := promote.Run(r.Context(), s.root, a.Repo, id, att, envName, io.Discard, io.Discard)
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
		// Reload so the re-rendered fragments reflect the just-appended event.
		if a, err = project.LoadAttempt(s.root, id, att); err != nil {
			s.fail(w, err)
			return
		}
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
