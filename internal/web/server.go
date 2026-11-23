// Package web serves the Draiver board: a self-contained HTMX server that
// renders the four control states per ATTEMPT from the filesystem log. Each
// attempt is its own card; one ticket can appear several times. It is
// read-mostly but for four write affordances, none reimplemented here: the
// attempt detail page appends a typed log event (POST /ticket/{id}/{attempt}/log
// — a note/gotcha/decision, or a Review action: Decision to reopen, Done to
// close), answers an open escalation (POST /ticket/{id}/{attempt}/resolve — the
// board affordance for `draiver resolve`), and drives the attempt's local git
// folder from the board (POST /ticket/{id}/{attempt}/git/push and .../git/sync —
// the drv-013 Git controls: push the branch to its remote, or pull trunk down and
// back-merge it, via `draiver ctl` over internal/land); the board's
// green play button (POST /ticket/{id}/{attempt}/enable) opts a parked attempt
// into daemon supervision; and the attempt's provenance panel (POST
// /ticket/{id}/{attempt}/provenance) sets the repo/base that gate merge/sync, so a
// human can unblock a base-less Review attempt from the UI; and the board card's
// archive affordance (POST /ticket/{id}/{attempt}/archive) takes an attempt off the
// board — a green tick to accept a Done card, a grey cross to abandon an active one.
// Every write reaches the data folder through the same in-process gateway the CLI
// verbs use (drv-008), not a subprocess: log/done/resolve/enable/archive and the
// provenance/edges writes go through internal/repo, and the git controls through
// internal/land (which records its notes through repo too). So there is exactly
// one code path per write — a function shared by the board and the terminal, not a
// shelled binary — and the same live-session render (`ctl logs -f`) streams here
// via internal/streamlog. The handlers own only transport concerns
// (same-origin/CSRF, idempotency, HTML shaping); the write semantics live once in
// the libraries.
//
// # Files
//
// The package is split by handler family (drvweb-028). Route → file:
//
//	server.go       Server, options, New, Handler (the route table), Serve, render/fail
//	funcmap.go      template funcs and view-model helpers (labels, links, markdown, ages)
//	board.go        GET / · GET /board · POST /board/sort · GET /favicon-state ·
//	                GET /cache · POST …/star
//	attempt.go      GET /ticket/{id} · GET …/{attempt} · GET …/{attempt}/live
//	panels.go       detail-page panel view models: capability, repo settings, edges
//	settings.go     POST …/provenance · POST …/repo-settings · POST …/supervision ·
//	                POST /ticket/{id}/edges
//	controls.go     POST …/enable · POST …/archive · POST …/resolve ·
//	                POST …/restart-context
//	gitcontrols.go  POST …/git/push · POST …/git/sync
//	logs.go         POST …/log · GET …/agent-logs · GET …/system-logs (SSE)
//	report.go       GET …/report[/asset|/download] · POST …/report/rerun
//	reviewenv.go    POST …/review-env/{env}/up|down · GET …/review-env/{env}/status
//	promote.go      POST …/promote/{env} · POST …/done (mark done)
//
// where … is /ticket/{id}/{attempt}.
package web

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/util"

	"github.com/Dawil/draiver/internal/bddexec"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/repo"
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
	// pyramidGit gathers the live git state a pyramid badge projects over — the
	// loaded pyramid, HEAD, and worktree dirtiness — from an attempt's worktree or
	// branch ref. It defaults to gatherPyramidState (the real git reads); tests
	// inject a deterministic stub so the badge render can be exercised off canned
	// state without a real repo (drvweb-021 #32). ok is false when there is nothing
	// to project (no repo, no checkout/branch, no pyramid, or a git error).
	pyramidGit func(ctx context.Context, a project.Attempt) (pyramidState, bool)
	// pyCache is the memoizing layer New puts in front of the real gatherer
	// (drvweb-030); handlers that change git state drop an attempt's entry through
	// invalidateGit. Nil-safe: a Server built without New has no cache to drop.
	pyCache *pyramidCache
	// gitPos gathers the live git position the Git-controls panel shows (drv-022): the
	// attempt's current local branch and its ahead/behind counts versus the primary
	// remote. It defaults to gatherGitPosition (the real git reads) and, exactly like
	// pyramidGit, is swapped for a deterministic stub in tests so the panel render can
	// be exercised off canned state without a real repo. Every field is best-effort —
	// an unset one degrades to "unknown" / an omitted count.
	gitPos func(ctx context.Context, a project.Attempt) gitPositionState
	// actor is the identity stamped on both web writes — a composed log event and
	// a board enable. The board has no CLI actor context, so it is configured at
	// construction (see WithActor); it defaults to human:webui.
	actor string
	// gw is the in-process data-access gateway (drv-008): the webui's writes go
	// through the same library functions the CLI verbs call, not a subprocess. It is
	// bound to the server's root and actor once both are resolved (see New).
	gw *repo.Repo
	// supDefault is the global/project default coordinator supervision mode
	// (drvctl-042) the capability panel folds a per-ticket override over to show the
	// *effective* mode (project.Effective). The web package stays config-free — the
	// value is threaded in from cmd/webui.go (see WithSupervisionDefault); it
	// defaults to the passthrough floor so a zero-config New still resolves a
	// concrete mode.
	supDefault project.Supervision
	// rerun delegates a BDD report rerun to the in-process controlplane executor
	// (drv-019): it brings the attempt's bound environment up, runs the healthcheck
	// guard, re-executes the BDD rung, and captures a fresh side-by-side artefact
	// set. It defaults to internal/bddexec.Rerun (the real executor); tests inject a
	// deterministic stub so the handler's outcome-mapping and report refresh can be
	// exercised without a real repo, exactly as they do for alive/pyramidGit.
	rerun func(ctx context.Context, a project.Attempt) (bddexec.Outcome, error)
	// restart reaps the attempt's live session and hands it back to a running
	// `ctl up` to bring up on a clean context window, cold-starting from brief — the
	// `ctl restart --new-session` path (FlushSession). It backs the board's "Restart
	// context window" button on a context-window auto-stop escalation (drv-028). The
	// web package stays free of a reconcile dependency: the real implementation is
	// injected from cmd/webui.go (WithRestart), which can build a client reconciler;
	// a nil field means no controller was wired, so the handler reports the restart
	// could not be scheduled while the resolution it already wrote stays durable. A
	// non-nil error (e.g. reconcile.ErrNoController when no `ctl up` is running) is
	// surfaced verbatim in the restart-result banner.
	restart func(ctx context.Context, ticket, att string) error
	// now is the clock the board's flow metrics are computed against (drvweb-027). It
	// defaults to time.Now; threading it through a seam (rather than calling time.Now()
	// deep in the metric math) keeps the metrics unit-testable with a fixed clock, the
	// same discipline the replay path uses to avoid render-time now (web.go handleReplay).
	now func() time.Time
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

// WithSupervisionDefault sets the global/project default supervision mode the
// capability panel folds a per-ticket override over (drvctl-042). It takes the raw
// config string so the web package need not import config; an unparseable or empty
// value is ignored, leaving the passthrough floor. This is the only place the
// board learns the config default, so a capability with no per-ticket override
// still shows the operator's true effective mode.
func WithSupervisionDefault(mode string) Option {
	return func(s *Server) {
		if m, err := project.ParseSupervision(mode); err == nil {
			s.supDefault = m
		}
	}
}

// WithRestart injects the attempt-restart implementation the "Restart context
// window" button drives (drv-028). The web package cannot build a reconciler
// without pulling in the daemon's wiring, so cmd/webui.go — which can — passes a
// closure over a client reconciler's Restart(FlushSession). An unset restart (the
// zero-config New) leaves the handler reporting "restart unavailable"; the
// resolution it writes first is unaffected.
func WithRestart(fn func(ctx context.Context, ticket, att string) error) Option {
	return func(s *Server) {
		if fn != nil {
			s.restart = fn
		}
	}
}

// New builds a Server over the given data root. Options configure the write path
// (see WithActor); with none it is a board with the human:webui default actor.
func New(root store.Root, opts ...Option) (*Server, error) {
	// goldmark's default already blanks javascript:/vbscript:/file: and non-image
	// data: link hrefs, but it admits data:image/{png,gif,jpeg,webp}. linkPolicy
	// closes that carve-out so body links/images/autolinks are http/https (or
	// relative) only — the render-time scheme floor, independent of the
	// append-time event.ValidateLink check (defense in depth).
	md := goldmark.New(
		goldmark.WithExtensions(extension.Table),
		goldmark.WithParserOptions(
			parser.WithASTTransformers(util.Prioritized(linkPolicy{}, 100)),
		),
	)
	s := &Server{root: root, md: md, alive: pidAlive, actor: "human:webui", supDefault: project.SupervisionPassthrough, now: time.Now}
	// The real gatherer is a method, so bind it once s exists — behind the
	// transparent in-memory cache (drvweb-030), which only changes how often git runs,
	// never what renders. Tests overwrite the field with a stub, exactly as they do
	// s.alive, which bypasses the cache entirely.
	s.pyCache = newPyramidCache(s.gatherPyramidStateErr)
	s.pyramidGit = s.pyCache.get
	// gatherGitPosition is a method too; bind it once s exists, mirroring pyramidGit.
	// Tests overwrite the field with a stub returning canned position state.
	s.gitPos = s.gatherGitPosition
	// Default the rerun executor to the real in-process controlplane; tests override.
	s.rerun = func(ctx context.Context, a project.Attempt) (bddexec.Outcome, error) {
		return bddexec.Rerun(ctx, s.root, a.Repo, a.Ticket, a.ID)
	}
	// Default restart to "unavailable": the web package cannot build a reconciler, so
	// the real path is injected by cmd/webui.go (WithRestart). Left unset, the handler
	// still resolves the escalation durably and reports the restart could not be
	// scheduled — the same degraded behaviour as a missing `ctl up`.
	s.restart = func(ctx context.Context, ticket, att string) error {
		return errRestartUnavailable
	}
	for _, opt := range opts {
		opt(s)
	}
	// Bind the data-access gateway once the actor is resolved (an Option may have
	// overridden the human:webui default): every web write goes through it, so the
	// board and a terminal share one in-process write path per event.
	s.gw = repo.New(s.root, s.actor)
	tmpl, err := template.New("").
		Funcs(template.FuncMap{
			"stateLabel":     stateLabel,
			"badge":          func(s project.State, oob bool) badgeVM { return badgeVM{State: s, OOB: oob} },
			"cardHref":       cardHref,
			"canEnable":      canEnable,
			"canArchive":     canArchive,
			"canMarkDone":    s.canMarkDone,
			"restartContext": restartContext,
			"archiveAction":  archiveAction,
			"reviewLink":     reviewLink,
			"shortSHA":       shortSHA,
			"provenance":     func(a project.Attempt) provenanceVM { return provenanceVM{Attempt: a} },
			"repoSettings":   s.repoSettingsPanel,
			"cachePanel":     cachePanel,
			"cohortRow":      cohortRow,
			"sessionDot":     s.sessionDot,
			"pyramidBadge":   s.pyramidBadge,
			"promoChips":     s.promoChips,
			"gitPosition":    s.gitPosition,
			"paletteVars":    paletteVars,
			"inlineMD":       s.inlineMD,
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

// Handler returns the route mux. Every route is a GET but these writes: POST
// .../log appends a composed log event, POST .../resolve answers an open
// escalation, POST .../enable opts an attempt into daemon supervision, POST
// .../archive takes an attempt off the board (an `archive` event, accepted or
// abandoned), POST .../git/push and .../git/sync drive the attempt's local git
// folder (push the branch to its remote; pull trunk down and back-merge it — the
// drv-013 Git controls), POST .../provenance sets the repo/base that gate
// merge/sync (via `attempt set`), and POST .../supervision sets a Capability's
// coordinator supervision mode (via `ctl supervision`).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleBoardPage)
	mux.HandleFunc("GET /board", s.handleBoardPartial)
	mux.HandleFunc("POST /board/sort", s.handleSort)
	mux.HandleFunc("GET /cache", s.handleCacheRollup)
	mux.HandleFunc("GET /favicon-state", s.handleFaviconState)
	mux.HandleFunc("GET /ticket/{id}", s.handleAttemptIndex)
	mux.HandleFunc("POST /ticket/{id}/edges", s.handleEdges)
	mux.HandleFunc("GET /ticket/{id}/{attempt}", s.handleAttempt)
	mux.HandleFunc("GET /ticket/{id}/{attempt}/live", s.handleAttemptLive)
	mux.HandleFunc("GET /ticket/{id}/{attempt}/agent-logs", s.handleAgentLogs)
	mux.HandleFunc("GET /ticket/{id}/{attempt}/system-logs", s.handleSystemLogs)
	mux.HandleFunc("GET /ticket/{id}/{attempt}/report", s.handleReport)
	mux.HandleFunc("GET /ticket/{id}/{attempt}/report/asset", s.handleReportAsset)
	mux.HandleFunc("GET /ticket/{id}/{attempt}/report/download", s.handleReportDownload)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/report/rerun", s.handleReportRerun)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/log", s.handleLogAppend)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/resolve", s.handleResolve)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/restart-context", s.handleRestartContext)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/enable", s.handleEnable)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/archive", s.handleArchive)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/star", s.handleStar)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/git/push", s.handleGitPush)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/git/sync", s.handleGitSync)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/review-env/{env}/up", s.handleReviewEnvUp)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/review-env/{env}/down", s.handleReviewEnvDown)
	mux.HandleFunc("GET /ticket/{id}/{attempt}/review-env/{env}/status", s.handleReviewEnvStatus)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/promote/{env}", s.handlePromote)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/done", s.handleMarkDone)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/provenance", s.handleProvenance)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/repo-settings", s.handleRepoSettings)
	mux.HandleFunc("POST /ticket/{id}/{attempt}/supervision", s.handleSupervision)
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

// attemptLive reports whether an attempt has a live agent process right now — the
// Live bit the Pending projection folds over the log state. It reads session.json
// and probes the recorded pid through the injectable s.alive, the same path the
// session dot's "running" check uses (see sessionDot), so the board's Pending
// tier and the green liveness dot can never disagree about whether an agent is up.
// It is the web twin of session.Alive, going through s.alive so tests stub it.
func (s *Server) attemptLive(a project.Attempt) bool {
	id, ok := s.readSessionIdentity(a.Ticket, a.ID)
	return ok && id.PID != 0 && s.alive(id.PID)
}

// fillRuntime populates the two runtime bits (Live/Desired) on an attempt that
// project.LoadAttempt leaves at their false defaults — the same fleet-wide
// DeriveDesired + live probe board() folds over each card. A write handler that
// gates on canEnable must call it first, so its check sees the *effective* state
// the card render did, not just the durable log bits: a wants:/after:-activated
// attempt is Desired+Live but not Enabled, so without this its enable POST would
// still fire for a card that shows no button (drvweb-022). Desiredness is
// fleet-wide (a via-parent child is pulled in down a wants: edge under another
// ticket), so it is derived over every attempt, exactly as board() does.
func (s *Server) fillRuntime(a *project.Attempt) error {
	all, err := project.LoadAll(s.root)
	if err != nil {
		return err
	}
	edges, err := project.LoadAllEdges(s.root)
	if err != nil {
		return err
	}
	desired := project.DeriveDesired(all, edges)
	a.Desired = desired[project.Ref{Ticket: a.Ticket, Attempt: a.ID}]
	a.Live = s.attemptLive(*a)
	return nil
}

// renderFragment executes a named template into a byte slice (the OOB banner the
// restart handler appends after the attempt-live fragment), so one response body
// carries both swaps — mirroring how the git verbs pair the live fragment with the
// git-result banner.
func (s *Server) renderFragment(name string, data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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

// invalidateGit drops an attempt's cached git-derived state (drvweb-030) so the next
// read — typically the response the calling write handler is about to render — goes
// back to git. Every handler that changes git state calls it.
func (s *Server) invalidateGit(ticket, att string) {
	if s.pyCache != nil {
		s.pyCache.invalidate(ticket, att)
	}
}
