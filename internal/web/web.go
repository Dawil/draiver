// Package web serves the Draiver board: a self-contained HTMX server that renders
// the four control states per ATTEMPT from the filesystem log. Each attempt is
// its own card; one ticket can appear several times. It is read-mostly but for one
// affordance — the board's green play button POSTs to opt a parked attempt into
// daemon supervision. That write is not reimplemented here: handleEnable shells
// `draiver ctl enable` (runDraiverEnable), the same verb a human runs at a
// terminal, so there is a single enable code path.
package web

import (
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

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Server renders the board and attempt detail from a data root, and serves the
// board's one write (the enable button).
type Server struct {
	root store.Root
	tmpl *template.Template
	md   goldmark.Markdown
	// alive probes whether a recorded session pid is still running. It defaults to
	// a signal-0 OS probe (pidAlive); tests inject a deterministic stub.
	alive func(pid int) bool
	// actor is the identity stamped on board-originated writes (the enable event).
	// The board has no CLI actor context, so it is configured at construction (see
	// WithActor); it defaults to human:webui.
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

// New builds a Server over the given data root. Options configure the write path
// (see WithActor); with none it is the read-only board with a human:webui actor.
func New(root store.Root, opts ...Option) (*Server, error) {
	s := &Server{root: root, md: goldmark.New(), alive: pidAlive, actor: "human:webui"}
	for _, opt := range opts {
		opt(s)
	}
	tmpl, err := template.New("").
		Funcs(template.FuncMap{
			"stateLabel":  stateLabel,
			"badge":       func(s project.State, oob bool) badgeVM { return badgeVM{State: s, OOB: oob} },
			"cardHref":    cardHref,
			"canEnable":   canEnable,
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

// Handler returns the route mux. Every route is a GET but one: the board's single
// write, POST .../enable, opts an attempt into daemon supervision.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleBoardPage)
	mux.HandleFunc("GET /board", s.handleBoardPartial)
	mux.HandleFunc("GET /favicon-state", s.handleFaviconState)
	mux.HandleFunc("GET /ticket/{id}", s.handleAttemptIndex)
	mux.HandleFunc("GET /ticket/{id}/{attempt}", s.handleAttempt)
	mux.HandleFunc("GET /ticket/{id}/{attempt}/live", s.handleAttemptLive)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/enable", s.handleEnable)
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

// handleEnable is the board's one write (POST /ticket/{id}/{attempt}/enable): it
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

// draiverBinOverride points the enable shell-out at a specific draiver binary. It
// is empty in production, where runDraiverEnable resolves the running executable
// via os.Executable() (under `draiver webui`, that is draiver itself). Tests set
// it to a freshly built binary, since under `go test` os.Executable() is the test
// binary, not draiver.
var draiverBinOverride string

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
	exe := draiverBinOverride
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return fmt.Errorf("locate draiver binary: %w", err)
		}
	}
	cmd := exec.Command(exe, "ctl", "enable",
		"--data", s.root.Dir, "--actor", s.actor, "--attempt", att, "--", id)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("draiver ctl enable: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sameOrigin guards the write route against cross-site POSTs. The webui binds to
// localhost, so the surface is already small; this keeps a cross-origin page in a
// browser from driving the enable button. Modern browsers set Sec-Fetch-Site,
// the primary check; when absent (non-browser clients like curl, or the test
// harness) it falls back to comparing the Origin host to Host, treating a missing
// Origin as trusted — a same-origin fetch/form omits it, and a non-browser caller
// on localhost is already inside the trust boundary.
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

// toHTML renders trusted local markdown to HTML. goldmark's default config does
// not pass through raw HTML, so this is safe to embed.
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
