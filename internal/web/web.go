// Package web serves the Draiver board: a self-contained HTMX server that
// renders the four control states per ATTEMPT from the filesystem log. Each
// attempt is its own card; one ticket can appear several times. It is
// read-mostly but for four write affordances, none reimplemented here: the
// attempt detail page appends a typed log event (POST /ticket/{id}/{attempt}/log
// — a note/gotcha/decision, or a Review action: Decision to reopen, Done to
// close), answers an open escalation (POST /ticket/{id}/{attempt}/resolve — the
// board affordance for `draiver resolve`), and closes a Review attempt landed by
// an external PR while pulling its base (POST /ticket/{id}/{attempt}/merge-remote
// — the "Merged elsewhere" action, `draiver ctl merge --remote`); the board's
// green play button (POST /ticket/{id}/{attempt}/enable) opts a parked attempt
// into daemon supervision; and the attempt's provenance panel (POST
// /ticket/{id}/{attempt}/provenance) sets the repo/base that gate merge/sync, so a
// human can unblock a base-less Review attempt from the UI. Each write shells the
// same verb a human runs at a terminal (draiver log / draiver done / draiver
// resolve / draiver ctl merge --remote / draiver ctl enable / draiver attempt set
// — see runDraiverLog, runDraiverResolve, runDraiverMergeRemote, runDraiverEnable,
// runDraiverAttemptSet), so there is a single code path per write.
package web

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Server renders the board and attempt detail from a data root, appends
// human-composed log events from the detail page, and serves the board's enable
// button.
type Server struct {
	root store.Root
	tmpl *template.Template
	md   goldmark.Markdown
	// alive probes whether a recorded session pid is still running. It defaults to
	// a signal-0 OS probe (pidAlive); tests inject a deterministic stub.
	alive func(pid int) bool
	// actor is the identity stamped on both web writes — a composed log event and
	// a board enable. The board has no CLI actor context, so it is configured at
	// construction (see WithActor); it defaults to human:webui.
	actor string
}

// Option configures a Server at construction. It keeps New's zero-config form
// (New(root)) working for the read-only paths while letting the webui command
// thread in the write actor.
type Option func(*Server)

// WithActor sets the identity stamped on the enable event a board click appends,
// so a board-originated write is attributable in the hash chain. An empty actor
// is ignored, leaving the human:webui default.
func WithActor(actor string) Option {
	return func(s *Server) {
		if actor != "" {
			s.actor = actor
		}
	}
}

// composeTypes is the curated allow-list of event types the web compose box may
// append. draiver log accepts a free-form --type, but the web form must never
// let a human hand-type a lifecycle type that owns a dedicated flow —
// escalation/resolution (the escalate/resolve pair), review (the agent's own
// claim), enable/disable (supervision), or created. Only note and gotcha (plain
// context), decision (which also reopens a Review attempt to Running), and done
// (the Review terminal action) are allowed; any other type is a 400.
var composeTypes = map[string]bool{
	"note":     true,
	"gotcha":   true,
	"decision": true,
	"done":     true,
}

// draiverBinOverride points the web writes' shell-outs (runDraiverLog and
// runDraiverEnable) at a specific draiver binary. It is empty in production,
// where those resolve the running executable via os.Executable() (under
// `draiver webui`, that is draiver itself). Tests set it to a freshly built
// binary, since under `go test` os.Executable() is the test binary, not draiver.
var draiverBinOverride string

// draiverExe resolves the draiver binary the write shell-outs invoke:
// draiverBinOverride when set (tests, where os.Executable() is the test binary,
// not draiver), else the running executable — under `draiver webui` that is
// draiver itself.
func draiverExe() (string, error) {
	if draiverBinOverride != "" {
		return draiverBinOverride, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate draiver binary: %w", err)
	}
	return exe, nil
}

// runDraiverLog appends a web-composed event by invoking the draiver CLI rather
// than reimplementing the append in the web layer: note/gotcha/decision go
// through `draiver log --type T`, and the terminal action through `draiver done`
// — the same verbs a human runs at a terminal, so there is a single write path.
// It passes the server's resolved data root, actor, and target attempt as
// explicit flags (env-independent), and ends with "--" so a body beginning with
// "-" is never parsed as a flag. On failure it surfaces the CLI's combined
// output for a legible error.
func (s *Server) runDraiverLog(typ, id, att, body string) error {
	exe, err := draiverExe()
	if err != nil {
		return err
	}
	var args []string
	if typ == "done" {
		args = []string{"done"}
	} else {
		args = []string{"log", "--type", typ}
	}
	args = append(args, "--data", s.root.Dir, "--actor", s.actor, "--attempt", att, "--", id, body)
	cmd := exec.Command(exe, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("draiver %s: %w: %s", typ, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runDraiverAttemptSet writes an attempt's provenance (repo/base) by invoking
// `draiver attempt set` rather than reimplementing the attempt.md write in the
// web layer — the same verb a human runs at a terminal, so the board and the CLI
// share one write path (see runDraiverLog). Only a non-empty field is passed as a
// flag: a blank input omits the flag so `attempt set` leaves that field untouched
// (drvweb-009 maps a blank input to "no change", never to "clear"; the caller has
// already rejected the both-blank case). The data root goes as an explicit flag
// and the ticket@attempt target after "--" so an id beginning with "-" is never
// parsed as a flag. No --actor: `attempt set` appends no hash-chained event, so
// the write carries no actor to attribute. On failure it surfaces the CLI's
// combined output for a legible error.
func (s *Server) runDraiverAttemptSet(id, att, repo, base string) error {
	exe, err := draiverExe()
	if err != nil {
		return err
	}
	args := []string{"attempt", "set", "--data", s.root.Dir}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	if base != "" {
		args = append(args, "--base", base)
	}
	args = append(args, "--", id+"@"+att)
	cmd := exec.Command(exe, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("draiver attempt set: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runDraiverResolve answers an escalation by invoking `draiver resolve` rather
// than reimplementing the resolution append in the web layer — the same verb a
// human runs at a terminal, so the board and the CLI share one write path (see
// runDraiverLog). The escalation seq and the answer go as positional args after
// "--" so an answer beginning with "-" is never parsed as a flag; the server's
// data root, actor, and target attempt go as explicit flags. On failure it
// surfaces the CLI's combined output for a legible error.
func (s *Server) runDraiverResolve(id, att string, seq int, answer string) error {
	exe, err := draiverExe()
	if err != nil {
		return err
	}
	args := []string{"resolve", "--data", s.root.Dir, "--actor", s.actor, "--attempt", att,
		"--", id, strconv.Itoa(seq), answer}
	cmd := exec.Command(exe, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("draiver resolve: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// stateLabels overrides how a control state is shown in the human-facing web UI.
// The domain vocabulary (project.State, surfaced by the CLI, state.md, and brief)
// is unchanged; only the board column and the detail badge read differently.
var stateLabels = map[project.State]string{
	project.NeedsMe: "Stuck",
}

// stateLabel is the presentation label for a state on the board and detail views.
func stateLabel(s project.State) string {
	if l, ok := stateLabels[s]; ok {
		return l
	}
	return string(s)
}

// badgeVM drives the reusable "state-badge" partial. OOB marks the copy the live
// fragment emits with hx-swap-oob so a single poll updates the header badge that
// lives outside the swapped log region.
type badgeVM struct {
	State project.State
	OOB   bool
}

// cardHref is a board card's link target. A Stuck or Review card deep-links to
// the log entry that put it there — the latest event, which is the open
// escalation (Stuck) or the review claim (Review) — so one click lands the human
// on and highlights the exact entry needing attention. Running/Done cards link
// to the attempt with no fragment.
func cardHref(a project.Attempt) string {
	base := "/ticket/" + a.Ticket + "/" + a.ID
	switch a.State {
	case project.NeedsMe, project.Review:
		if n := len(a.Events); n > 0 {
			return fmt.Sprintf("%s#event-%d", base, a.Events[n-1].Seq)
		}
	}
	return base
}

// canEnable reports whether an attempt's card should show the green play button:
// a Running attempt that is not yet enabled (the supervision axis — the same bit
// the grey session-dot reads, DeriveEnabled). Enabled attempts, and attempts in
// any other column, get no button. It gates both the rendered button and — via
// handleEnable's idempotent no-op — the effect of the POST, so the two can never
// disagree about which cards are actionable.
func canEnable(a project.Attempt) bool {
	return a.State == project.Running && !a.Enabled
}

// linkVM is a hyperlink surfaced on a served page — a board-card review action or
// a detail-timeline chip. Rel is the opaque, forge-neutral label (pr, mr, diff,
// ci, …) used only to pick a primary and to label a chip; Href is the URL, always
// re-checked against the render-time scheme floor before it becomes a live href.
type linkVM struct {
	Rel  string
	Href string
}

// allowedHREFScheme reports whether raw may be emitted as a live href on the
// board: an absolute http/https URL. This is re-applied at render for every link
// the board serves, independent of the append-time event.ValidateLink check —
// defense in depth, so a link that reached disk by any path (an older event, a
// hand-edited file) still cannot produce a javascript:/data:/file: href. The
// board never fetches or previews raw; this is a scheme test, not an integration.
func allowedHREFScheme(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return true
	}
	return false
}

// primaryLink picks the review link to feature on a card: the first whose rel is
// "pr" or "mr" (a merge/pull request outranks a bare diff/ci link), else the
// first link — considering only links that pass the render-time scheme floor. It
// returns nil when no link survives.
func primaryLink(links []event.Link) *linkVM {
	var first *linkVM
	for _, l := range links {
		if !allowedHREFScheme(l.Href) {
			continue
		}
		vm := linkVM{Rel: l.Rel, Href: l.Href}
		if first == nil {
			first = &vm
		}
		switch strings.ToLower(l.Rel) {
		case "pr", "mr":
			return &vm
		}
	}
	return first
}

// reviewLink returns the primary review link to surface as a board card action,
// or nil when there is none. It is scoped to the Review column: only a Review
// attempt's latest `review` event is consulted, so a reopened (Running) or
// blocked (Stuck) attempt never shows a stale PR button. A direct-merge flow with
// no PR carries no link and the card degrades to no action.
func reviewLink(a project.Attempt) *linkVM {
	if a.State != project.Review {
		return nil
	}
	for i := len(a.Events) - 1; i >= 0; i-- {
		if a.Events[i].Type == "review" {
			return primaryLink(a.Events[i].Links)
		}
	}
	return nil
}

// safeLinks maps an event's links to chip view models, dropping any whose scheme
// fails the render-time floor. The chips render as live hrefs, so the floor is
// re-applied here (defense in depth) rather than trusting the stored value.
func safeLinks(links []event.Link) []linkVM {
	var out []linkVM
	for _, l := range links {
		if allowedHREFScheme(l.Href) {
			out = append(out, linkVM{Rel: l.Rel, Href: l.Href})
		}
	}
	return out
}

// linkPolicy is a goldmark AST transformer enforcing the render-time scheme floor
// on markdown body hyperlinks. goldmark's default already blanks javascript:/
// vbscript:/file: and non-image data: destinations, but it admits data:image/
// {png,gif,jpeg,webp}; this closes that carve-out so a Link/Image destination is
// http/https (or a scheme-less relative URL) only, and a disallowed autolink is
// demoted to plain text. It is defense in depth, independent of the append-time
// event.ValidateLink check.
type linkPolicy struct{}

func (linkPolicy) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	var demote []*ast.AutoLink
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Link:
			if !bodyDestOK(v.Destination) {
				v.Destination = nil
			}
		case *ast.Image:
			if !bodyDestOK(v.Destination) {
				v.Destination = nil
			}
		case *ast.AutoLink:
			if !bodyDestOK(v.URL(src)) {
				demote = append(demote, v)
			}
		}
		return ast.WalkContinue, nil
	})
	// An autolink has no settable destination, so a disallowed one is replaced by
	// its literal label as escaped text — no live href survives. Done after the
	// walk so the tree is not mutated mid-traversal.
	for _, a := range demote {
		if p := a.Parent(); p != nil {
			p.ReplaceChild(p, a, ast.NewString(a.Label(src)))
		}
	}
}

// bodyDestOK reports whether a markdown link/image/autolink destination may render
// as a live href: a scheme-less relative URL (nothing to abuse) or an http/https
// absolute URL. Everything else — javascript:, data:, vbscript:, file: — is
// blanked or demoted by linkPolicy.
func bodyDestOK(raw []byte) bool {
	u, err := url.Parse(strings.TrimSpace(string(raw)))
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "", "http", "https":
		return true
	}
	return false
}

// New builds a Server over the given data root. Options configure the write path
// (see WithActor); with none it is a board with the human:webui default actor.
func New(root store.Root, opts ...Option) (*Server, error) {
	// goldmark's default already blanks javascript:/vbscript:/file: and non-image
	// data: link hrefs, but it admits data:image/{png,gif,jpeg,webp}. linkPolicy
	// closes that carve-out so body links/images/autolinks are http/https (or
	// relative) only — the render-time scheme floor, independent of the
	// append-time event.ValidateLink check (defense in depth).
	md := goldmark.New(goldmark.WithParserOptions(
		parser.WithASTTransformers(util.Prioritized(linkPolicy{}, 100)),
	))
	s := &Server{root: root, md: md, alive: pidAlive, actor: "human:webui"}
	for _, opt := range opts {
		opt(s)
	}
	tmpl, err := template.New("").
		Funcs(template.FuncMap{
			"stateLabel":  stateLabel,
			"badge":       func(s project.State, oob bool) badgeVM { return badgeVM{State: s, OOB: oob} },
			"cardHref":    cardHref,
			"canEnable":   canEnable,
			"reviewLink":  reviewLink,
			"provenance":  func(a project.Attempt) provenanceVM { return provenanceVM{Attempt: a} },
			"cachePanel":  cachePanel,
			"cohortRow":   cohortRow,
			"sessionDot":  s.sessionDot,
			"paletteVars": paletteVars,
		}).
		ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	s.tmpl = tmpl
	return s, nil
}

// paletteVars renders the session-dot colours as a :root custom-property block so
// the browser gets them from the Go palette constants — one origin, no dot hex
// typed into style.css. It is injected into each full page's <head>; style.css
// styles the dots purely through var(--dot-*).
func paletteVars() template.HTML {
	return template.HTML(fmt.Sprintf(
		"<style>:root{--dot-running:%s;--dot-stopped:%s;--dot-disabled:%s;--dot-error:%s;}</style>",
		Eucalypt, Wattle, GhostGum, Waratah))
}

// Handler returns the route mux. Every route is a GET but five writes: POST
// .../log appends a composed log event, POST .../resolve answers an open
// escalation, POST .../enable opts an attempt into daemon supervision, POST
// .../merge-remote closes a Review attempt landed by an external PR and pulls its
// base (via `ctl merge --remote`), and POST .../provenance sets the repo/base that
// gate merge/sync (via `attempt set`).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleBoardPage)
	mux.HandleFunc("GET /board", s.handleBoardPartial)
	mux.HandleFunc("GET /cache", s.handleCacheRollup)
	mux.HandleFunc("GET /favicon-state", s.handleFaviconState)
	mux.HandleFunc("GET /ticket/{id}", s.handleAttemptIndex)
	mux.HandleFunc("GET /ticket/{id}/{attempt}", s.handleAttempt)
	mux.HandleFunc("GET /ticket/{id}/{attempt}/live", s.handleAttemptLive)
	mux.HandleFunc("GET /ticket/{id}/{attempt}/agent-logs", s.handleAgentLogs)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/log", s.handleLogAppend)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/resolve", s.handleResolve)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/enable", s.handleEnable)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/merge-remote", s.handleMergeRemote)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/provenance", s.handleProvenance)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	return mux
}

// Serve starts the HTTP server on addr.
func (s *Server) Serve(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}

// --- view models ---

type boardVM struct {
	Running []project.Attempt
	NeedsMe []project.Attempt
	Review  []project.Attempt
	Done    []project.Attempt
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
	case len(vm.NeedsMe) > 0:
		return faviconStuck
	case len(vm.Review) > 0:
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
	vm, err := s.board()
	if err != nil {
		return faviconPlain
	}
	return vm.FaviconHref()
}

type eventVM struct {
	Seq   int
	Type  string
	Actor string
	// TSRel is the server-rendered relative age ("3 minutes ago"), a fallback
	// that renders without JS and paints before reltime.js takes over. TSISO is
	// the machine-readable RFC3339 stamp reltime.js reads to keep the age live
	// (a Done attempt does not poll, so a frozen server string would go stale).
	// TSFull is the precise UTC timestamp surfaced as the hover tooltip.
	TSRel      string
	TSISO      string
	TSFull     string
	BodyHTML   template.HTML
	Refs       []int
	Artefacts  []string
	Links      []linkVM
	IsEsc      bool
	Resolved   bool
	ResolvedBy int
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
}

// provenanceVM drives the "provenance-panel" partial: the repo/base editor on the
// attempt detail page. Attempt supplies the prefill values (Repo/Base) and the
// gating bits (State/Ticket/ID); Saved is true only in the response to a
// successful save, so the panel can confirm the write after an htmx swap (the
// full-page render leaves it false).
type provenanceVM struct {
	Attempt project.Attempt
	Saved   bool
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
	return cacheVM{
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
	}
}

// groupInt formats a non-negative token count with thousands separators, so a
// six-figure token tally stays readable in the cache panel (1234567 → "1,234,567").
func groupInt(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return s // token counts are non-negative; don't try to group a sign
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

type indexVM struct {
	Ticket   string
	Title    string
	Attempts []project.Attempt
	// FaviconHref is the current board variant, server-rendered into the index
	// page's <link rel="icon"> (see detailVM.FaviconHref).
	FaviconHref string
}

func (s *Server) board() (boardVM, error) {
	attempts, err := project.LoadAll(s.root)
	if err != nil {
		return boardVM{}, err
	}
	var vm boardVM
	for _, a := range attempts {
		switch a.State {
		case project.Running:
			vm.Running = append(vm.Running, a)
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
	vm, err := s.board()
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "board_page.html", vm)
}

func (s *Server) handleBoardPartial(w http.ResponseWriter, r *http.Request) {
	vm, err := s.board()
	if err != nil {
		s.fail(w, err)
		return
	}
	// Keep the favicon in sync without coupling it to the board's data-testid
	// counts: emit the variant as a custom event htmx fires after the swap.
	emitFaviconTrigger(w, vm)
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
	vm, err := s.board()
	if err != nil {
		s.fail(w, err)
		return
	}
	emitFaviconTrigger(w, vm)
	// Empty 200; the client polls with hx-swap="none", so nothing is swapped —
	// only the HX-Trigger favicon event is processed.
	w.WriteHeader(http.StatusOK)
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

	resolutionOf := map[int]int{}
	for _, e := range a.Events {
		if e.Type == "resolution" {
			for _, ref := range e.Refs {
				resolutionOf[ref] = e.Seq
			}
		}
	}

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
		}
		if e.Type == "escalation" {
			ev.IsEsc = true
			if by, ok := resolutionOf[e.Seq]; ok {
				ev.Resolved = true
				ev.ResolvedBy = by
			}
		}
		vm.Events = append(vm.Events, ev)
	}
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

// handleAgentLogs streams an attempt's live session logs as Server-Sent Events
// (GET /ticket/{id}/{attempt}/agent-logs) — the first stream in the web layer,
// which is otherwise all htmx polling. It shells out to `draiver ctl logs -f`
// (the same verb a human runs at a terminal, and the same write-path-reuse
// discipline as the log/resolve/enable POSTs) and relays each rendered stdout
// line as one SSE `data:` event. Over a pipe the CLI's renderer is non-TTY, so it
// emits plain, uncoloured text (newStreamRenderer gates styling on
// term.IsTerminal) — exactly what this read-only panel wants, with the prefix
// column, markdown-as-literal, and ctl.jsonl health interleave reused verbatim.
//
// The child is bound to r.Context() via exec.CommandContext: when the client
// closes the SSE — the <details> panel collapses, the page is left — the process
// is killed and the follow ends. Each line is relayed as text and set client-side
// via textContent; it is never routed through the markdown→HTML path, so the
// untrusted session content (arbitrary tool output + model prose) cannot inject
// markup. A hand-run attempt with no session yet simply streams nothing; the
// client shows a quiet placeholder, not an error.
func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, fmt.Errorf("streaming unsupported"))
		return
	}
	exe, err := draiverExe()
	if err != nil {
		s.fail(w, err)
		return
	}

	cmd := exec.CommandContext(r.Context(), exe, "ctl", "logs", "-f",
		"--data", s.root.Dir, id+"@"+att)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := cmd.Start(); err != nil {
		s.fail(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable proxy buffering so the stream reaches the panel line-by-line.
	w.Header().Set("X-Accel-Buffering", "no")

	// Relay stdout one line per SSE record. bufio.Scanner strips the trailing
	// newline, so each scanned line carries no embedded newline to break the
	// `data:` framing; a stray CR is trimmed for the same reason. Flush after
	// every line so the panel updates live rather than in buffer-sized bursts.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if _, err := fmt.Fprintf(w, "data: %s\n\n", line); err != nil {
			break // client went away; CommandContext kills the child
		}
		flusher.Flush()
	}
	// Reap the child. Context cancellation already signals it on client close;
	// Wait releases its resources whether it exited on its own or was killed.
	_ = cmd.Wait()
}

// handleLogAppend appends a human-composed typed event to an attempt (POST
// /ticket/{id}/{attempt}/log) and returns the re-rendered live fragment, so the
// new entry, the state badge, and the log count all update from one swap. It is
// the webui's single write path: a Decision reopens a Review attempt to Running
// and a Done closes it (both via Derive on the appended lifecycle event), while
// note/gotcha/decision on any other attempt just record context.
func (s *Server) handleLogAppend(w http.ResponseWriter, r *http.Request) {
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
	typ := r.FormValue("type")
	if !composeTypes[typ] {
		http.Error(w, "draiver: unsupported log type "+strconv.Quote(typ), http.StatusBadRequest)
		return
	}
	// The two Review actions (decision/done) may fire without a typed reason, so
	// they fall back to a sensible default body; a plain note/gotcha with no body
	// is a mistake and is rejected.
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		switch typ {
		case "done":
			body = "Ticket closed."
		case "decision":
			body = "Reopened to Running."
		default:
			http.Error(w, "draiver: a "+typ+" needs a message", http.StatusBadRequest)
			return
		}
	}
	if err := s.runDraiverLog(typ, id, att, body); err != nil {
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
	// Always 200: the badge flips in-place via the OOB swap. A Done attempt's log
	// region keeps its poll attribute and self-cancels on its next /live poll (the
	// 286 path), so we do not need to signal termination from this response.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(live)
}

// handleEnable is the board's enable write (POST /ticket/{id}/{attempt}/enable): it
// opts a Running, disabled attempt into daemon supervision by shelling
// `draiver ctl enable` (runDraiverEnable) — the same verb a human runs at a
// terminal, so the enable is not reimplemented in the web layer — then re-renders
// the card so htmx swaps the play button away and the grey session-dot flips. The
// bare enable is purely declarative: no control socket, no reconciler; a running
// `ctl up` brings the attempt up on its next tick, and with no daemon the enable
// simply waits, matching the grey-dot semantics the button sits on.
//
// It is idempotent: an already-enabled (or non-Running) attempt — e.g. a
// double-click racing the 3s board poll — is a no-op that just re-renders the
// current card, so the chain gains exactly one enable per enable. The gate is
// canEnable, checked here before the shell-out (the CLI's `enable` would append
// unconditionally), so the button and the effect can never disagree. The
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
	// Only the disabled→enabled transition writes; canEnable is the same gate the
	// button renders behind, so a POST for a card that shows no button is a no-op.
	if canEnable(a) {
		if err := s.runDraiverEnable(id, att); err != nil {
			s.fail(w, err)
			return
		}
		if a, err = project.LoadAttempt(s.root, id, att); err != nil {
			s.fail(w, err)
			return
		}
	}
	// Re-render just this card; htmx swaps it in place (outerHTML on .card-wrap),
	// dropping the button and recomputing the session-dot off the fresh state.
	s.render(w, "card", a)
}

// runDraiverEnable opts an attempt into supervision by invoking the draiver CLI
// rather than reimplementing the append in the web layer: `draiver ctl enable`
// records the durable `enable` log event (setEnabled, cmd/ctl_enable.go), the
// exact write a human's terminal performs, so there is a single enable code path.
// It passes the server's resolved data root, actor, and target attempt as
// explicit flags (env-independent), and ends with "--" so a ticket id beginning
// with "-" is never parsed as a flag. Bare enable only (no --now): no control
// socket is dragged into the web process. On failure it surfaces the CLI's
// combined output for a legible error.
func (s *Server) runDraiverEnable(id, att string) error {
	exe, err := draiverExe()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "ctl", "enable",
		"--data", s.root.Dir, "--actor", s.actor, "--attempt", att, "--", id)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("draiver ctl enable: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// handleMergeRemote closes a Review attempt whose change landed via a PR merged
// on the forge and refreshes the local base in one click (POST
// /ticket/{id}/{attempt}/merge-remote). It shells `draiver ctl merge --remote`
// (runDraiverMergeRemote — the write path a human runs at a terminal, drvctl-029),
// which owns the fetch + containment check + `done` and the best-effort local
// fast-forward; the web layer never touches git itself (the package's write
// invariant). Unlike the other writes it surfaces the verb's combined output on
// success too: the CLI records `done` and only *best-effort* pulls, so a
// zero-exit-with-warning ("could not fast-forward … pull manually") is a success
// to report, not an error — the message rides back on an OOB banner beside the
// re-rendered (now Done) log region. A nonzero exit (containment/fetch failure) is
// a real error, surfaced via s.fail like the other writes.
func (s *Server) handleMergeRemote(w http.ResponseWriter, r *http.Request) {
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
	msg, err := s.runDraiverMergeRemote(id, att)
	if err != nil {
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
	// One response body carries both swaps: the attempt-live fragment (log region +
	// OOB badge/count, flipped to Done) and the OOB result banner with the verb's
	// combined message. Buffer both before writing so a template error still fails
	// cleanly with a 500 rather than a half-written 200.
	var buf bytes.Buffer
	buf.Write(live)
	if err := s.tmpl.ExecuteTemplate(&buf, "merge-remote-result", msg); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// runDraiverMergeRemote closes an externally-landed Review attempt by invoking
// `draiver ctl merge --remote` rather than reimplementing the fetch/ff in the web
// layer — the same verb a human runs at a terminal, so the board and the CLI share
// one write path (see runDraiverEnable). Bare `--remote` picks the primary remote
// (no NAME field in the UI for v1); the server's data root, actor, and target
// attempt go as explicit flags, and the ticket id after "--" so an id beginning
// with "-" is never parsed as a flag. Unlike the other shell-outs it returns the
// trimmed combined output on success too: the verb prints the close plus the
// best-effort pull result (or warning) there, and the handler reports it. On a
// nonzero exit it wraps that same output as a legible error.
func (s *Server) runDraiverMergeRemote(id, att string) (string, error) {
	exe, err := draiverExe()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(exe, "ctl", "merge", "--remote",
		"--data", s.root.Dir, "--actor", s.actor, "--attempt", att, "--", id)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("draiver ctl merge --remote: %w: %s", err, text)
	}
	return text, nil
}

// handleProvenance sets an attempt's repo/base from the detail page's provenance
// panel (POST /ticket/{id}/{attempt}/provenance) — the one UI affordance for the
// merge/sync-gating fields, so a human can unblock an attempt wedged for want of a
// base without leaving the board. It shells `draiver attempt set`
// (runDraiverAttemptSet — the write path a human runs at a terminal, drvctl-028),
// which owns the attempt.md write and the field validation; the web layer never
// touches attempt.md itself (the package's write invariant). The panel prefills
// the current values, so a blank input means "leave unchanged": a blank field is
// omitted from the shell-out, and an all-blank submit is a 400 here rather than a
// 500 from the verb's "nothing to set". Only repo and base are read from the form
// — the tool/model fields `attempt set` also accepts are deliberately out of the
// UI's scope, the allow-list discipline the compose box uses for event types. On
// success it re-renders the panel in place (htmx outerHTML swap) with the saved
// values and a confirmation.
func (s *Server) handleProvenance(w http.ResponseWriter, r *http.Request) {
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
	// Only repo/base are accepted; a blank field maps to "no change" (the verb
	// leaves an omitted flag untouched), so the shell-out receives only the fields
	// the human actually filled. An all-blank submit changes nothing — reject it as
	// a bad request here rather than let the verb's "nothing to set" surface as a
	// 500.
	repo := strings.TrimSpace(r.FormValue("repo"))
	base := strings.TrimSpace(r.FormValue("base"))
	if repo == "" && base == "" {
		http.Error(w, "draiver: set a repo or a base to save", http.StatusBadRequest)
		return
	}
	if err := s.runDraiverAttemptSet(id, att, repo, base); err != nil {
		s.fail(w, err)
		return
	}
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "provenance-panel", provenanceVM{Attempt: a, Saved: true})
}

// handleResolve answers an open escalation from the attempt timeline (POST
// /ticket/{id}/{attempt}/resolve) and returns the re-rendered live fragment, so
// the escalation flips to "resolved by #N", the state badge leaves Stuck, and the
// count bump all land from one swap. It is the board affordance for `draiver
// resolve`: the write goes through the CLI (runDraiverResolve), not a re-appended
// event here, so the board and a terminal share one resolution path.
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
	if err := s.runDraiverResolve(id, att, seq, answer); err != nil {
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

// sameOrigin guards the state-changing POST routes against cross-site POSTs. The
// webui binds to localhost, so the surface is already small; this keeps a
// cross-origin page in a browser from driving a write (the log append, resolve, or
// enable button). Modern browsers set Sec-Fetch-Site, the primary check; when
// absent (non-browser clients like curl, or the test harness) it falls back to
// comparing the Origin host to Host, treating a missing Origin as trusted — a
// same-origin fetch/form omits it, and a non-browser caller on localhost is
// already inside the trust boundary.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "cross-site", "same-site":
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Host == r.Host
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	http.Error(w, "draiver: "+err.Error(), http.StatusInternalServerError)
}

// relativeAge renders a duration-since as a human "time ago" phrase. It is the
// server-side twin of static/reltime.js (which keeps the age live on the
// client); keep the two bucket boundaries and wording in sync. A future stamp
// (clock skew) clamps to "just now".
func relativeAge(d time.Duration) string {
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return agoPlural(int(d.Seconds()), "second")
	case d < time.Hour:
		return agoPlural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return agoPlural(int(d.Hours()), "hour")
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return agoPlural(int(d.Hours()/24), "day")
	case d < 30*24*time.Hour:
		return agoPlural(int(d.Hours()/(24*7)), "week")
	case d < 365*24*time.Hour:
		return agoPlural(int(d.Hours()/(24*30)), "month")
	default:
		return agoPlural(int(d.Hours()/(24*365)), "year")
	}
}

// agoPlural formats "N unit(s) ago" with singular/plural agreement.
func agoPlural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return strconv.Itoa(n) + " " + unit + "s ago"
}

// toHTML renders event/spec markdown to HTML. goldmark's default config does not
// pass through raw HTML, and linkPolicy (wired in New) blanks any link/image/
// autolink whose scheme is outside the http/https floor, so the result is safe to
// embed even though bodies are agent-supplied, attacker-influenceable content.
func (s *Server) toHTML(md string) template.HTML {
	var buf bytes.Buffer
	if err := s.md.Convert([]byte(md), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(md))
	}
	return template.HTML(buf.String())
}

func (s *Server) renderSpec(id string) template.HTML {
	data, err := readSpecBody(s.root.SpecPath(id))
	if err != nil {
		return template.HTML("<p class=\"muted\">(no spec.md)</p>")
	}
	return s.toHTML(data)
}
