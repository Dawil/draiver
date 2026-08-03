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
	"time"

	"github.com/yuin/goldmark"

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

// New builds a Server over the given data root.
func New(root store.Root) (*Server, error) {
	tmpl, err := template.New("").
		Funcs(template.FuncMap{
			"stateLabel": stateLabel,
			"badge":      func(s project.State, oob bool) badgeVM { return badgeVM{State: s, OOB: oob} },
			"cardHref":   cardHref,
		}).
		ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{root: root, tmpl: tmpl, md: goldmark.New()}, nil
}

// Handler returns the read-only route mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleBoardPage)
	mux.HandleFunc("GET /board", s.handleBoardPartial)
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

type eventVM struct {
	Seq        int
	Type       string
	Actor      string
	TS         string
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
}

type indexVM struct {
	Ticket   string
	Title    string
	Attempts []project.Attempt
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
	w.Header().Set("HX-Trigger", faviconTrigger(vm.FaviconHref()))
	s.render(w, "board.html", vm)
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
	vm := indexVM{Ticket: id, Title: id}
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

	vm := detailVM{Attempt: a, SpecHTML: s.renderSpec(id), Polls: a.State != project.Done}
	for _, e := range a.Events {
		ev := eventVM{
			Seq:       e.Seq,
			Type:      e.Type,
			Actor:     e.Actor,
			TS:        e.TS.UTC().Format("2006-01-02 15:04Z"),
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
