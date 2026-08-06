// Package web serves the read-only Draiver board: a self-contained HTMX server
// that renders the four control states per ATTEMPT from the filesystem log. Each
// attempt is its own card; one ticket can appear several times. It never writes
// and never spawns processes.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
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

// Server renders the read-only board and attempt detail from a data root.
type Server struct {
	root store.Root
	tmpl *template.Template
	md   goldmark.Markdown
	// alive probes whether a recorded session pid is still running. It defaults to
	// a signal-0 OS probe (pidAlive); tests inject a deterministic stub.
	alive func(pid int) bool
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

// New builds a Server over the given data root.
func New(root store.Root) (*Server, error) {
	// goldmark's default already blanks javascript:/vbscript:/file: and non-image
	// data: link hrefs, but it admits data:image/{png,gif,jpeg,webp}. linkPolicy
	// closes that carve-out so body links/images/autolinks are http/https (or
	// relative) only — the render-time scheme floor, independent of the
	// append-time event.ValidateLink check (defense in depth).
	md := goldmark.New(goldmark.WithParserOptions(
		parser.WithASTTransformers(util.Prioritized(linkPolicy{}, 100)),
	))
	s := &Server{root: root, md: md, alive: pidAlive}
	tmpl, err := template.New("").
		Funcs(template.FuncMap{
			"stateLabel":  stateLabel,
			"badge":       func(s project.State, oob bool) badgeVM { return badgeVM{State: s, OOB: oob} },
			"cardHref":    cardHref,
			"reviewLink":  reviewLink,
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
		"<style>:root{--dot-running:%s;--dot-stopped:%s;--dot-disabled:%s;}</style>",
		Eucalypt, Wattle, GhostGum))
}

// Handler returns the read-only route mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleBoardPage)
	mux.HandleFunc("GET /board", s.handleBoardPartial)
	mux.HandleFunc("GET /favicon-state", s.handleFaviconState)
	mux.HandleFunc("GET /ticket/{id}", s.handleAttemptIndex)
	mux.HandleFunc("GET /ticket/{id}/{attempt}", s.handleAttempt)
	mux.HandleFunc("GET /ticket/{id}/{attempt}/live", s.handleAttemptLive)
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
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "attempt-live", vm); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !vm.Polls {
		// 286 tells htmx to cancel the polling trigger on a terminal attempt.
		w.WriteHeader(286)
	}
	buf.WriteTo(w)
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
