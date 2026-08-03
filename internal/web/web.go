// Package web serves the read-only Draiver board: a self-contained HTMX server
// that renders the four control states per ATTEMPT from the filesystem log. Each
// attempt is its own card; one ticket can appear several times. It never writes
// and never spawns processes.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"time"

	"github.com/yuin/goldmark"

	"draiver/internal/project"
	"draiver/internal/store"
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

// New builds a Server over the given data root.
func New(root store.Root) (*Server, error) {
	tmpl, err := template.New("").
		Funcs(template.FuncMap{"stateLabel": stateLabel}).
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

// handleAttempt renders one attempt's detail (GET /ticket/{id}/{attempt}).
func (s *Server) handleAttempt(w http.ResponseWriter, r *http.Request) {
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

	resolutionOf := map[int]int{}
	for _, e := range a.Events {
		if e.Type == "resolution" {
			for _, ref := range e.Refs {
				resolutionOf[ref] = e.Seq
			}
		}
	}

	vm := detailVM{Attempt: a, SpecHTML: s.renderSpec(id)}
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
	// Present newest-first by default; the detail view offers a client-side
	// toggle to flip back to oldest-first.
	slices.Reverse(vm.Events)
	s.render(w, "ticket.html", vm)
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
