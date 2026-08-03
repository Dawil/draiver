// Package web serves the read-only Draiver board: a self-contained HTMX server
// that renders the four control states from the filesystem log. It never writes
// and never spawns processes.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/yuin/goldmark"

	"draiver/internal/project"
	"draiver/internal/store"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Server renders the read-only board and ticket detail from a data root.
type Server struct {
	root store.Root
	tmpl *template.Template
	md   goldmark.Markdown
}

// New builds a Server over the given data root.
func New(root store.Root) (*Server, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
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
	mux.HandleFunc("GET /ticket/{id}", s.handleTicket)
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
	Running []project.Ticket
	NeedsMe []project.Ticket
	Review  []project.Ticket
	Done    []project.Ticket
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
	Ticket   project.Ticket
	SpecHTML template.HTML
	Events   []eventVM
}

func (s *Server) board() (boardVM, error) {
	tickets, err := project.LoadAll(s.root)
	if err != nil {
		return boardVM{}, err
	}
	var vm boardVM
	for _, t := range tickets {
		switch t.State {
		case project.Running:
			vm.Running = append(vm.Running, t)
		case project.NeedsMe:
			vm.NeedsMe = append(vm.NeedsMe, t)
		case project.Review:
			vm.Review = append(vm.Review, t)
		case project.Done:
			vm.Done = append(vm.Done, t)
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

func (s *Server) handleTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.root.Exists(id) {
		http.NotFound(w, r)
		return
	}
	t, err := project.Load(s.root, id)
	if err != nil {
		s.fail(w, err)
		return
	}

	resolutionOf := map[int]int{}
	for _, e := range t.Events {
		if e.Type == "resolution" {
			for _, ref := range e.Refs {
				resolutionOf[ref] = e.Seq
			}
		}
	}

	vm := detailVM{Ticket: t, SpecHTML: s.renderSpec(id)}
	for _, e := range t.Events {
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
