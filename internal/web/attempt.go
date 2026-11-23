package web

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"time"

	"github.com/Dawil/draiver/internal/agentmsg"
	"github.com/Dawil/draiver/internal/kb"
	"github.com/Dawil/draiver/internal/limit"
	"github.com/Dawil/draiver/internal/project"
)

type eventVM struct {
	Seq   int
	Type  string
	Actor string
	// TSRel is the server-rendered relative age ("3 minutes ago"), a fallback
	// that renders without JS and paints before reltime.js takes over. TSISO is
	// the machine-readable RFC3339 stamp reltime.js reads to keep the age live
	// (a Done attempt does not poll, so a frozen server string would go stale).
	// TSFull is the precise UTC timestamp surfaced as the hover tooltip.
	TSRel     string
	TSISO     string
	TSFull    string
	BodyHTML  template.HTML
	Refs      []int
	Artefacts []string
	Links     []linkVM
	// Tags are the event's opaque retrieval labels, rendered as small chips (drv-026).
	Tags       []string
	IsEsc      bool
	Resolved   bool
	ResolvedBy int
	// ContextAutoStop marks an OPEN escalation recorded by the context-window limit
	// gate (it carries limit.TagContextAutoStop, drv-028). The template keys the extra
	// "Restart context window" button off it, so the affordance shows only on this one
	// escalation kind and never on an ordinary/permission escalation or a resolved one.
	ContextAutoStop bool
	// Superseded marks a retired (non-live) non-escalation fact; SupersededBy is the
	// seq of the event that retired it. A resolved escalation uses Resolved/ResolvedBy
	// instead, so the two are mutually exclusive on any one event.
	Superseded   bool
	SupersededBy int
	// ToAgent marks a human→agent message (event `to: agent`, drvctl-059): the
	// entry the in-session unread notice counts. Rendered as a "→ agent" badge so
	// the human can see which entries the agent will be told about.
	ToAgent bool
}

type detailVM struct {
	Attempt  project.Attempt
	SpecHTML template.HTML
	Events   []eventVM
	// Polls is true while the attempt is non-terminal: the page arms htmx
	// polling on the log region. A Done attempt renders without a trigger so
	// polling never starts (and the live fragment returns 286 to self-cancel).
	Polls bool
	// FaviconHref is the current board variant, server-rendered into the detail
	// page's <link rel="icon"> so the tab icon reflects live board state on load.
	FaviconHref string
	// Capability drives the capability panel (drvweb-017): the `wants:` sub-fleet
	// glance plus the supervision dial. It is non-nil only when the attempt's ticket
	// is a Capability (carries `wants:` edges), so the panel renders on a coordinator
	// and is absent on an ordinary ticket. Built by s.capability.
	Capability *capabilityVM
	// Report drives the BDD-report embed (drv-018): the latest captured run shown
	// inline plus a selector among the side-by-side regenerations. It is always
	// non-nil; its HasRuns is false (and the section hidden) until drv-017's capture
	// has written a run for the attempt. Built by s.reportPanel.
	Report *reportPanelVM
	// ReviewEnv drives the review-environment surface (drv-020, drv-034): one
	// launch/teardown panel per review-ready environment the attempt's pyramid declares,
	// each a live, clickable instance of the feature under review. It is non-nil only on
	// an attempt in Review with at least one review-ready env to offer (or a
	// misconfigured reviewEnvironment default to flag), so the surface is absent on every
	// other attempt. Built by s.reviewEnvSurface.
	ReviewEnv *reviewEnvsVM
	// Promote drives the promotion surface (drv-043): one promote control per declared
	// `promotions:` entry, each verifying (never performing) that the reviewed code is
	// promoted-and-healthy in that environment. It is non-nil only on an attempt in
	// Review whose pyramid declares at least one promotion, so the surface is absent on
	// every other attempt. Built by s.promoteSurface.
	Promote *promoteSurfaceVM
	// ForkCommand is the ready-to-paste "fork this session" shell command
	// (drvweb-029), built server-side by forkCommand from session.json. Empty — and
	// the copy button hidden — unless the attempt has a claude-code session with a
	// session id and an absolute worktree. Rendering it writes no log event.
	ForkCommand string
}

// cacheVM drives the "cache-panel" partial on the attempt detail page: the
// per-attempt prompt-cache telemetry folded in from drvctl-031. It is a display
// projection of agent.Metrics — every number is pre-formatted here so the
// template stays logic-free. Present is false when the attempt has no metrics
// yet (Metrics nil, the common Running case), so the panel renders a quiet "not
// recorded yet" note rather than a grid of zeros.
type cacheVM struct {
	Present bool
	// Active mirrors caching_active: the fast "is caching even on?" signal. When
	// false the prefix never cached (below-min-prefix or a silent invalidator) and
	// the panel styles itself distinctly.
	Active         bool
	HitRatioPct    string // cache_hit_ratio as a percentage, e.g. "73.2%"
	CacheRead      string // cache_read tokens, thousands-grouped
	CacheCreation  string // cache_creation tokens (written), thousands-grouped
	InputTokens    string // uncached input tokens, thousands-grouped
	OutputTokens   string // output tokens, thousands-grouped
	NormalizedWork string // all tokens at 1x — the caching-agnostic work measure
	// Billed vs Uncached are input-rate token equivalents (output excluded): Billed
	// applies the cache multipliers (reads 0.1x, writes 1.25x), Uncached prices the
	// same prompt tokens at full rate. Saved is how much the cache took off that
	// bill, or "—" when there were no prompt tokens to bill.
	BilledInput   string
	UncachedInput string
	SavedPct      string

	// Cost-avoided is the drvweb-018 business headline: the input-token saving vs.
	// the no-cache baseline expressed as a modelled "equivalent API cost". HasRate
	// is true when the attempt's model has a known $/token — then CostAvoided and
	// the billed-vs-uncached bar carry dollar figures; otherwise they fall back to
	// token-equivalents with no "$". CostAvoidedPct (share of prompt spend avoided)
	// and the reuse factor are rate-independent and always populated.
	HasRate     bool
	CostAvoided string // "$0.42" (HasRate) or "6,950" — the headline figure only
	// CostAvoidedUnit is the small unit suffix rendered beside CostAvoided: empty in
	// the dollar case (the "$" is the unit), "input-equiv" in the token-equivalent
	// fallback so the words do not ride at the 28px headline size.
	CostAvoidedUnit string
	CostAvoidedPct  string // "69.5%" — PctCostAvoided, the bounded share avoided
	BilledDollars   string // "$0.02" — only meaningful when HasRate
	UncachedDollars string // "$0.05" — only meaningful when HasRate
	// ReuseFactor is the honest ">100%" reuse stat R/C: "×8.0" with ReusePct
	// "800%", or "—" for both when nothing was written (C == 0, ratio undefined).
	ReuseFactor string
	ReusePct    string
}

// cachePanel projects an attempt's folded-in metrics into the cache-panel view.
// A nil Metrics (no metered retire yet) yields a zero cacheVM whose Present is
// false, so the template shows the quiet placeholder instead of a grid of zeros.
func cachePanel(a project.Attempt) cacheVM {
	m := a.Metrics
	if m == nil {
		return cacheVM{}
	}
	// Uncached-equivalent prompt cost and the saved fraction — the same projection
	// the rollup aggregates use (cacheCost), so "saved" reads identically on the
	// per-attempt panel and the /cache page.
	uncached, saved := cacheCost(*m)
	vm := cacheVM{
		Present:        true,
		Active:         m.CachingActive,
		HitRatioPct:    pct(m.CacheHitRatio),
		CacheRead:      groupInt(m.CacheReadTokens),
		CacheCreation:  groupInt(m.CacheCreationTokens),
		InputTokens:    groupInt(m.InputTokens),
		OutputTokens:   groupInt(m.OutputTokens),
		NormalizedWork: groupInt(m.NormalizedWork),
		BilledInput:    groupInt(roundTokens(m.BilledInputTokens)),
		UncachedInput:  groupInt(uncached),
		SavedPct:       saved,
		CostAvoidedPct: pct(m.PctCostAvoided),
	}

	// Reuse factor R/C — the ">100%" stat. Nil pointer (nothing written) → "—".
	vm.ReuseFactor, vm.ReusePct = "—", "—"
	if m.ReadCreationRatio != nil {
		vm.ReuseFactor = fmt.Sprintf("×%.1f", *m.ReadCreationRatio)
		vm.ReusePct = fmt.Sprintf("%.0f%%", *m.ReadCreationRatio*100)
	}

	// Dollar figures when the model's input rate is known; otherwise fall back to
	// input-token-equivalents (no "$"), so an unknown-model attempt still renders.
	if rate, ok := inputRatePerToken(a.Model); ok {
		vm.HasRate = true
		vm.CostAvoided = dollars(m.CostAvoidedInputTokens * rate)
		vm.BilledDollars = dollars(m.BilledInputTokens * rate)
		vm.UncachedDollars = dollars(float64(uncached) * rate)
	} else {
		vm.CostAvoided = groupInt(roundTokens(m.CostAvoidedInputTokens))
		vm.CostAvoidedUnit = "input-equiv"
	}
	return vm
}

// handleAttemptIndex lists a ticket's attempts (GET /ticket/{id}).
func (s *Server) handleAttemptIndex(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.root.Exists(id) {
		http.NotFound(w, r)
		return
	}
	ids, err := s.root.ListAttempts(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	vm := indexVM{Ticket: id, Title: id, FaviconHref: s.faviconHref()}
	for _, aid := range ids {
		a, err := project.LoadAttempt(s.root, id, aid)
		if err != nil {
			s.fail(w, err)
			return
		}
		vm.Title = a.Title
		vm.Attempts = append(vm.Attempts, a)
	}
	edges, err := project.LoadEdges(s.root, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	vm.Edges = edgesPanel(id, edges)
	s.render(w, "attempts.html", vm)
}

// detail builds the view model for one attempt. Events are kept oldest-first in
// the DOM; the detail view flips the *visual* order with CSS (flex-direction) so
// an htmx innerHTML swap never disturbs the reader's chosen order.
func (s *Server) detail(id, att string) (detailVM, error) {
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		return detailVM{}, err
	}

	// The knowledge fold (drv-026) is the single source of the supersedes edge the
	// board renders: it generalises the old resolution→escalation map to every
	// retirement (explicit `supersedes` and the subsumed resolution bridge alike), so
	// a superseded fact dims and links to the event that retired it exactly as a
	// resolved escalation always has.
	view := kb.Fold(a.Events)

	now := time.Now()
	vm := detailVM{Attempt: a, SpecHTML: s.renderSpec(id), Polls: a.State != project.Done}
	for _, e := range a.Events {
		ts := e.TS.UTC()
		ev := eventVM{
			Seq:       e.Seq,
			Type:      e.Type,
			Actor:     e.Actor,
			TSRel:     relativeAge(now.Sub(e.TS)),
			TSISO:     ts.Format(time.RFC3339),
			TSFull:    ts.Format("2006-01-02 15:04:05 UTC"),
			BodyHTML:  s.toHTML(e.Body),
			Refs:      e.Refs,
			Artefacts: e.Artefacts,
			Links:     safeLinks(e.Links),
			Tags:      e.Tags,
			ToAgent:   agentmsg.IsMessage(e),
		}
		if f, ok := view.Get(e.Seq); ok && !f.Live {
			// An escalation retired by its resolution keeps the familiar "resolved by
			// #N" affordance (and drops the resolve form); any other retired fact is a
			// generic "superseded by #N" demotion.
			if e.Type == "escalation" {
				ev.Resolved = true
				ev.ResolvedBy = f.SupersededBy
			} else {
				ev.Superseded = true
				ev.SupersededBy = f.SupersededBy
			}
		}
		if e.Type == "escalation" {
			ev.IsEsc = true
			// An OPEN auto-stop escalation (tagged by the limit gate) earns the extra
			// restart affordance; a resolved one falls through to the plain "resolved by
			// #N" note like any other escalation.
			if !ev.Resolved && slices.Contains(e.Tags, limit.TagContextAutoStop) {
				ev.ContextAutoStop = true
			}
		}
		vm.Events = append(vm.Events, ev)
	}
	cap, err := s.capability(a)
	if err != nil {
		return detailVM{}, err
	}
	vm.Capability = cap
	rp, err := s.reportPanel(id, att)
	if err != nil {
		return detailVM{}, err
	}
	vm.Report = rp
	// Review-environment panel (drv-020): best-effort like the pyramid badge — a nil
	// result (gate not held, or an I/O hiccup reading config/pyramid) simply renders
	// nothing. The git read behind it is bounded by a background context, matching
	// pyramidBadge.
	vm.ReviewEnv = s.reviewEnvSurface(context.Background(), a)
	// Promotion surface (drv-043): best-effort like the review-env surface — a nil
	// result (not in Review, no pyramid, or no declared promotion) renders nothing. The
	// git read behind it is bounded by a background context, matching pyramidBadge.
	vm.Promote = s.promoteSurface(context.Background(), a)
	vm.ForkCommand = s.forkCommandFor(id, att)
	return vm, nil
}

// handleAttempt renders one attempt's detail (GET /ticket/{id}/{attempt}).
func (s *Server) handleAttempt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	vm, err := s.detail(id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	vm.FaviconHref = s.faviconHref()
	s.render(w, "ticket.html", vm)
}

// handleAttemptLive renders the htmx polling fragment (GET
// /ticket/{id}/{attempt}/live): the log <ol> as the primary swap plus the state
// badge and log count as hx-swap-oob copies, so one poll updates every live
// region. A Done attempt answers 286 so htmx stops polling.
func (s *Server) handleAttemptLive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
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
	if !vm.Polls {
		// 286 tells htmx to cancel the polling trigger on a terminal attempt.
		w.WriteHeader(286)
	}
	w.Write(live)
}

// executeLive renders the attempt-live fragment (the log <ol> plus the OOB state
// badge and log count) for one attempt. It is shared by the live poll and the
// log-append POST so both update every live region from one response body.
func (s *Server) executeLive(vm detailVM) ([]byte, error) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "attempt-live", vm); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
