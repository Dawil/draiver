package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/Dawil/draiver/internal/project"
)

// --- view models ---

type boardVM struct {
	Running []project.Attempt
	Pending []project.Attempt
	NeedsMe []project.Attempt
	Review  []project.Attempt
	Done    []project.Attempt

	// View is the board filter in force (drv-039): "code" (the default — code
	// tickets, plus any codebase ledger in an attention state) or "ledgers" (only
	// codebase ledgers). It drives the toggle's active state and the poll URL baked
	// into board_page.html's <main hx-get>, so a 3s refresh stays in the same view.
	View string
	// LedgerCount is every non-archived codebase ledger (regardless of the current
	// view), and LedgerAttention how many of them are in an attention state — both
	// shown on the Ledgers toggle so the operator sees ledgers exist (and need them)
	// without first switching into that view.
	LedgerCount     int
	LedgerAttention int
	// StuckTotal and ReviewTotal are the GLOBAL attention tally across both views —
	// every non-archived attempt of either type, not just the view-filtered columns.
	// The favicon reads these (not len(NeedsMe)/len(Review)) so switching to the
	// ledgers view can never drop a code ticket's Stuck/Review signal from the tab
	// icon (drv-039).
	StuckTotal  int
	ReviewTotal int

	// Metrics is the flow-metrics strip (drvweb-027): four at-a-glance numbers about
	// how the flow is doing (oldest unresolved ticket, p85 dev-cycle, total
	// unresolved, 4-week throughput), computed over code tickets in board() and
	// rendered inside the polled region so they refresh on the 3s poll. See metrics.go.
	Metrics metricsVM
	// SortByAge is the viewer's age-sort toggle (drvweb-027), set by applyBoardOrder
	// from the draiver_sort cookie — it drives the Oldest tile's pressed/on state and
	// (via the same post-step) the oldest-first ordering within each favourite
	// partition. A per-viewer view preference like Favourited, not board state.
	SortByAge bool
}

// boardView is the board's type filter (drv-039). It is parsed from the `?view=`
// query and stored on boardVM as a plain string for the templates.
type boardView string

const (
	viewCode    boardView = "code"    // the default: code tickets + attention-state ledgers
	viewLedgers boardView = "ledgers" // only codebase ledgers
)

// parseView resolves the raw `?view=` query value to a known board view, defaulting
// to the code-ticket view for an absent or unrecognised value — so a hand-typed or
// stale URL degrades to the ordinary board rather than erroring.
func parseView(raw string) boardView {
	if boardView(strings.TrimSpace(raw)) == viewLedgers {
		return viewLedgers
	}
	return viewCode
}

// viewFromCurrentURL recovers the board view from htmx's HX-Current-URL header —
// the page the operator is actually looking at — for a POST that carries no view of
// its own (the archive form). Absent or unparseable, it falls back to the code view.
func viewFromCurrentURL(r *http.Request) boardView {
	cur := r.Header.Get("HX-Current-URL")
	if cur == "" {
		return viewCode
	}
	u, err := url.Parse(cur)
	if err != nil {
		return viewCode
	}
	return parseView(u.Query().Get("view"))
}

// Favicon variants. The <head> is never re-rendered by htmx, so the board
// favicon is kept in sync out of band (see FaviconHref and handleBoardPartial).
const (
	faviconPlain  = "/static/favicon.svg"
	faviconStuck  = "/static/favicon-stuck.svg"
	faviconReview = "/static/favicon-review.svg"
)

// FaviconHref is the favicon variant for the current board state. Stuck outranks
// Review — a blocked attempt is more urgent than one merely awaiting
// verification. This is the single source of truth for the precedence: it both
// server-renders the initial <link rel="icon"> href (so there is no plain→badged
// flash on load) and drives the per-refresh HX-Trigger event.
func (vm boardVM) FaviconHref() string {
	switch {
	case vm.StuckTotal > 0:
		return faviconStuck
	case vm.ReviewTotal > 0:
		return faviconReview
	default:
		return faviconPlain
	}
}

// faviconTrigger builds the HX-Trigger header value that asks htmx to dispatch a
// bubbling "draiver:favicon" DOM event carrying the variant href. favicon.js
// listens for it and points <link rel="icon"> at the href — no DOM scraping.
func faviconTrigger(href string) string {
	b, _ := json.Marshal(map[string]map[string]string{
		"draiver:favicon": {"href": href},
	})
	return string(b)
}

// emitFaviconTrigger arms the post-swap favicon event carrying the current board
// variant. Shared by /board and the detail pages' /favicon-state poll so every
// page swaps to the same variant off the one Stuck>Review precedence source.
func emitFaviconTrigger(w http.ResponseWriter, vm boardVM) {
	w.Header().Set("HX-Trigger", faviconTrigger(vm.FaviconHref()))
}

// faviconHref scans the board and returns the current favicon variant so a
// detail page can server-render the right icon on load (no plain→badged flash).
// The favicon is a nicety, so a board-scan error degrades to plain rather than
// failing the whole detail page.
func (s *Server) faviconHref() string {
	// The favicon tracks GLOBAL attention (StuckTotal/ReviewTotal), which board()
	// tallies over every attempt regardless of view, so the view passed here is
	// immaterial — use the default code view.
	vm, err := s.board(viewCode)
	if err != nil {
		return faviconPlain
	}
	return vm.FaviconHref()
}

type indexVM struct {
	Ticket   string
	Title    string
	Attempts []project.Attempt
	// Edges is the ticket-level dependency-edge editor (drvweb-016) — spec.md is
	// shared across attempts, so it lives on this page, not the per-attempt detail.
	Edges edgesVM
	// FaviconHref is the current board variant, server-rendered into the index
	// page's <link rel="icon"> (see detailVM.FaviconHref).
	FaviconHref string
}

// isAttention reports whether a board state is one the human is asked to act on —
// an open escalation (Stuck) or a review claim (Review). A codebase ledger
// surfaces into the code-ticket view only in one of these states (drv-039), and
// the favicon badges on the same two.
func isAttention(st project.State) bool {
	return st == project.NeedsMe || st == project.Review
}

func (s *Server) board(view boardView) (boardVM, error) {
	attempts, err := project.LoadAll(s.root)
	if err != nil {
		return boardVM{}, err
	}
	// Pending is a read-time projection folded over the log state (drvctl-039): a
	// desired attempt with no live agent is not being worked, it is a self-resolving
	// wait. Desiredness is fleet-wide (a via-parent child may be pulled in by a
	// parent under another ticket down wants:), so it and the edges are computed once
	// over every attempt, not per-card. Switching on a.Control() (not a.State) is the
	// same overlay cmd/status.printBoard applies; every non-Pending state passes
	// through unchanged.
	edges, err := project.LoadAllEdges(s.root)
	if err != nil {
		return boardVM{}, err
	}
	desired := project.DeriveDesired(attempts, edges)
	// The pre-digest move-up (drvctl-050): a Needs-me child whose escalation was
	// absorbed by an enabled pre-digest parent projects Pending "escalated to P",
	// off the Needs-me column and attributed to the parent. Fleet-wide like desired,
	// folded under the same config default the capability panel resolves against.
	escalated := project.DeriveEscalated(attempts, edges, s.supDefault)
	var vm boardVM
	vm.View = string(view)
	// Flow metrics (drvweb-027) are a pure projection over the attempts just loaded —
	// no new disk read — and are computed over code tickets regardless of the current
	// view, so switching to the ledgers view never blanks the flow numbers. The clock
	// is threaded in via s.now so the math stays unit-testable (see metrics.go).
	vm.Metrics = computeMetrics(attempts, s.now())
	for _, a := range attempts {
		// An archived attempt lands in no column — that is the whole board effect of
		// archive: the card stops being emitted (see DeriveArchived / handleArchive).
		if a.Archived {
			continue
		}
		a.Desired = desired[project.Ref{Ticket: a.Ticket, Attempt: a.ID}]
		a.Live = s.attemptLive(a)
		a.EscalatedTo = escalated[project.Ref{Ticket: a.Ticket, Attempt: a.ID}]
		bs := a.BoardState()
		isLedger := a.Type == project.TypeCodebase

		// Global tallies, computed over EVERY non-archived attempt before the view
		// filter drops any — so the favicon (StuckTotal/ReviewTotal) and the Ledgers
		// toggle (LedgerCount/LedgerAttention) read the whole fleet, not the subset
		// the current view happens to render (drv-039).
		if isAttention(bs) {
			if bs == project.NeedsMe {
				vm.StuckTotal++
			} else {
				vm.ReviewTotal++
			}
		}
		if isLedger {
			vm.LedgerCount++
			if isAttention(bs) {
				vm.LedgerAttention++
			}
		}

		// View filter (drv-039). Ledgers view: codebase ledgers only. Code view: code
		// tickets always, plus a ledger only once it hits an attention state — so a
		// resting Parked ledger stays off the code board but an open-escalation one
		// still surfaces through the normal Stuck path.
		if view == viewLedgers {
			if !isLedger {
				continue
			}
		} else if isLedger && !isAttention(bs) {
			continue
		}

		switch bs {
		case project.Running:
			// A Running coordinator (a `wants:` ticket) renders as a distinct
			// coordinator card: surface the control plane's own wake reason (drvweb-023).
			// The activation event and child doorbells are already on the loaded logs, so
			// this is a pure projection — no extra read, folded on like WaitingReason.
			if project.IsCoordinator(a.Ticket, edges) {
				sig := project.DeriveCoordinatorSignal(a, attempts, edges)
				a.Coordinator = true
				a.WakeChild = sig.WakeChild
				a.QueueDepth = sig.QueueDepth
			}
			vm.Running = append(vm.Running, a)
		case project.Pending:
			// The Pending reason is either the move-up attribution ("escalated to P")
			// or the forward-gate "waiting on X" peek — the two ways an attempt lands
			// Pending. A moved-up child is Running-in-log, so WaitingReason would read
			// "waiting to start"; the escalated reason is the truthful one.
			//
			// A codebase ledger is a third case (drv-039): it is log-Pending by its
			// resting state (drv-037 restingState), not waiting on any gate, so the
			// "waiting on X" peek would be a lie — label it as the Parked rest instead.
			//
			// An interrupt hold (drvctl-060) is a fourth: the human interrupted the
			// session and the supervisor is holding it until they act, so say so rather
			// than imply a gate will open on its own.
			switch {
			case isLedger:
				a.WaitingReason = "parked — codebase ledger"
			case a.Interrupted():
				a.WaitingReason = project.InterruptedReason
			case a.EscalatedTo != "":
				a.WaitingReason = project.EscalatedReason(a.EscalatedTo)
			default:
				a.WaitingReason = project.WaitingReason(a.Ticket, attempts, edges)
			}
			vm.Pending = append(vm.Pending, a)
		case project.NeedsMe:
			vm.NeedsMe = append(vm.NeedsMe, a)
		case project.Review:
			vm.Review = append(vm.Review, a)
		case project.Done:
			vm.Done = append(vm.Done, a)
		}
	}
	return vm, nil
}

func (s *Server) handleBoardPage(w http.ResponseWriter, r *http.Request) {
	vm, err := s.board(parseView(r.URL.Query().Get("view")))
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store") // defeat bfcache: Back re-fetches fresh
	applyBoardOrder(&vm, parseFaves(r), parseSort(r))
	s.render(w, "board_page.html", vm)
}

func (s *Server) handleBoardPartial(w http.ResponseWriter, r *http.Request) {
	vm, err := s.board(parseView(r.URL.Query().Get("view")))
	if err != nil {
		s.fail(w, err)
		return
	}
	// Keep the favicon in sync without coupling it to the board's data-testid
	// counts: emit the variant as a custom event htmx fires after the swap.
	emitFaviconTrigger(w, vm)
	// Apply the viewer's favourites so each 3s poll re-emits the same favourites-first
	// order (drv-050): the order is derived from the cookie the server reads every
	// render, so the morph swap keeps pinned cards on top with nothing client-side.
	applyBoardOrder(&vm, parseFaves(r), parseSort(r))
	s.render(w, "board.html", vm)
}

// handleCacheRollup renders the cross-attempt, per-repo prompt-cache rollup with
// A/B cohorts (GET /cache) — the cross-ticket amortisation view the per-attempt
// panel cannot show. It loads every attempt and folds their metrics into per-repo
// aggregates (repoRollups); a static report, not a live region, since the totals are
// dominated by retired attempts and move slowly.
func (s *Server) handleCacheRollup(w http.ResponseWriter, r *http.Request) {
	attempts, err := project.LoadAll(s.root)
	if err != nil {
		s.fail(w, err)
		return
	}
	vm := repoRollups(attempts)
	vm.FaviconHref = s.faviconHref()
	s.render(w, "cache.html", vm)
}

// handleFaviconState answers the detail pages' favicon poll: it scans the board
// and emits the same draiver:favicon HX-Trigger as /board with an empty body.
// Detail pages poll this from a hidden element so their tab icon tracks live
// board state — a channel of its own, independent of the log poll (which
// self-cancels on a Done attempt and does not exist on the attempts index).
func (s *Server) handleFaviconState(w http.ResponseWriter, r *http.Request) {
	vm, err := s.board(viewCode)
	if err != nil {
		s.fail(w, err)
		return
	}
	emitFaviconTrigger(w, vm)
	// Empty 200; the client polls with hx-swap="none", so nothing is swapped —
	// only the HX-Trigger favicon event is processed.
	w.WriteHeader(http.StatusOK)
}

// handleStar toggles this card's per-viewer favourite (POST
// /ticket/{id}/{attempt}/star, drv-050) and re-renders the board region so the card
// relocates to its new in-column position in one htmx swap. Favourite is a view
// preference of the person looking at the board, NOT ticket state: the server
// persists nothing — the whole effect is a cookie read-modify-write. It reads the
// favourites cookie, toggles this card's ref in the set, writes the updated cookie
// back on the response, then renders the board with the just-updated set applied
// (so the moved card and its lit star reflect the toggle immediately, before the
// next 3s poll). The route is keyed by the same `{ticket}/{id}` ref every other
// card action uses, so two attempts of one ticket are independently starrable.
//
// Unlike archive/enable/done it writes no durable event and needs no attempt load
// beyond an existence check — the toggle is purely a cookie edit — but it keeps the
// same same-origin guard and NotFound-on-unknown behaviour as the other writes, and
// honours the operator's current ?view= (HX-Current-URL) so a star on the ledgers
// view re-renders the ledgers view, mirroring handleArchive.
func (s *Server) handleStar(w http.ResponseWriter, r *http.Request) {
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
	// Toggle this card's ref in the viewer's favourite set and write the cookie back.
	faves := parseFaves(r)
	ref := faveRef(id, att)
	if faves[ref] {
		delete(faves, ref)
	} else {
		faves[ref] = true
	}
	writeFavesCookie(w, faves)
	// Re-render the whole board region with the just-updated set applied, so the card
	// moves to (or leaves) the top of its column in the same swap. Keep the favicon in
	// sync like the other board-region writers, even though a star changes no attention
	// state — it is cheap and keeps every board-region handler uniform.
	vm, err := s.board(viewFromCurrentURL(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	emitFaviconTrigger(w, vm)
	applyBoardOrder(&vm, faves, parseSort(r))
	s.render(w, "board.html", vm)
}

// handleSort toggles this viewer's board-wide age-sort preference (POST /board/sort,
// drvweb-027) — the control is the Oldest metric tile. Like the favourite star it is a
// view preference of the person looking at the board, NOT ticket state: the server
// persists nothing, the whole effect is a cookie read-modify-write. It flips the
// draiver_sort cookie, writes it back, then re-renders the board region with the new
// order applied so the columns re-sort (oldest-first within each favourite partition)
// and the tile flips to its pressed state in one htmx swap, before the next 3s poll.
//
// Board-scoped (not per-card): there is no {id}/{attempt}. It keeps handleStar's
// same-origin guard and honours the operator's current ?view= (HX-Current-URL) so a
// toggle on the ledgers view re-renders the ledgers view, and keeps the favicon
// trigger uniform even though a sort changes no attention state.
func (s *Server) handleSort(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	// Flip the current preference and persist it in the viewer's cookie.
	on := !parseSort(r)
	writeSortCookie(w, on)
	vm, err := s.board(viewFromCurrentURL(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	emitFaviconTrigger(w, vm)
	applyBoardOrder(&vm, parseFaves(r), on)
	s.render(w, "board.html", vm)
}
