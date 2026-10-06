package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/reviewenv"
)

// reviewEnvVM drives the attempt page's "review-env-panel" partial (drv-020): the
// launch/teardown controls for a long-lived, clickable instance of the feature under
// review. It is the UI face of the reviewenv lifecycle — the panel reads the
// persisted Record and renders the one of six lifecycle states the env is in, with
// the URL revealed only while Up.
//
// The panel is offered only when the spec's three-part visibility gate holds —
// State==Review, the repo's resolved reviewEnvironment is set, and that environment
// is review-ready (up+down+≥1 healthcheck). A nil *reviewEnvVM renders nothing, the
// gate's "not offered" outcome; a VM with Misconfigured=true renders a diagnostic
// note (no Up button) for the one in-Review-but-misconfigured case, so a typo'd
// reviewEnvironment fails loudly rather than vanishing silently.
type reviewEnvVM struct {
	Attempt project.Attempt
	// EnvName is the repo's resolved reviewEnvironment — the .test-pyramid.yaml
	// environment this panel launches.
	EnvName string
	// Misconfigured is true when the attempt is in Review and reviewEnvironment is
	// set, but names an environment that is absent or not review-ready. Reason carries
	// the human-facing explanation (pyramid.Environment.ReviewReady's message, or
	// "no such environment"). The panel then shows the note and no launch control.
	Misconfigured bool
	Reason        string
	// State is the lifecycle phase read from the persisted Record (Down when none
	// exists). StateLabel is its human-facing summary.
	State      reviewenv.State
	StateLabel string
	// URL is the clickable review link — populated only while Up (the healthcheck is
	// green). Blank in every other state, so a dead link is never shown.
	URL string
	// Message carries the last probe output or failure detail, surfaced on the panel
	// in the Failed / Unhealthy / TeardownFailed states.
	Message string
	// Instance and Port are the parameterisation draiver injected, shown as panel meta
	// once a launch has allocated them.
	Instance string
	Port      int
	// CanUp gates the launch button (shown at rest: Down / Failed / TeardownFailed).
	// CanDown gates the teardown button (shown while there is something live or leaked
	// to tear down). Poll arms the webui status poll while the env is in motion.
	CanUp   bool
	CanDown bool
	Poll    bool
	// Params are the env's repo-declared parameterisation vars, rendered as editable
	// inputs in the launch form so the reviewer can set e.g. ENV_NAME before Up
	// (drv-020, resolution #45). Populated only while the launch form is shown (CanUp).
	Params []reviewEnvParam
}

// reviewEnvParam is one repo-declared parameterisation var surfaced as an editable
// launch-form input (drv-020, resolution #45): the reviewer sets its value before
// spinning the env up, and the launcher injects it beneath the always-winning
// DRAIVER_REVIEW_* vars. Value is pre-filled with the value the last launch used (read
// back from the record's captured ScriptEnv) when one exists, else the repo-declared
// default — so a relaunch after a failure keeps what the operator typed.
type reviewEnvParam struct {
	Name  string
	Value string
}

// stateLabel maps a lifecycle state to its panel summary.
func reviewEnvStateLabel(s reviewenv.State) string {
	switch s {
	case reviewenv.Starting:
		return "starting…"
	case reviewenv.Up:
		return "up"
	case reviewenv.Unhealthy:
		return "unhealthy"
	case reviewenv.Failed:
		return "launch failed"
	case reviewenv.TeardownFailed:
		return "teardown failed — leak"
	default:
		return "down"
	}
}

// projectReviewEnv is the pure gate+projection: it folds the attempt, the resolved
// reviewEnvironment name, the loaded pyramid, and the current Record into the panel
// VM. It is I/O-free so the visibility gate is exhaustively unit-testable off canned
// inputs (mirroring projectPyramid).
//
// It returns nil — render nothing — whenever the panel is not offered: a non-Review
// attempt, or a repo with no reviewEnvironment set. When reviewEnvironment IS set on
// a Review attempt but names a missing or non-review-ready environment, it returns a
// Misconfigured VM (the loud-failure note). Otherwise it returns the live panel VM
// projected from rec (a zero Record / hasRec=false is the resting Down state).
func projectReviewEnv(a project.Attempt, envName string, p *pyramid.Pyramid, rec reviewenv.Record, hasRec bool) *reviewEnvVM {
	if a.State != project.Review || envName == "" {
		return nil
	}
	vm := &reviewEnvVM{Attempt: a, EnvName: envName}

	// The named environment must exist in the pyramid and be review-ready, else the
	// panel is a diagnostic note rather than a launcher — the selection-time validation
	// surfaced where the operator set it.
	if p == nil {
		vm.Misconfigured = true
		vm.Reason = "no .test-pyramid.yaml environment named " + envName + " is reachable"
		return vm
	}
	env, ok := p.EnvironmentByName(envName)
	if !ok {
		vm.Misconfigured = true
		vm.Reason = "no environment named " + envName + " in .test-pyramid.yaml"
		return vm
	}
	if err := env.ReviewReady(); err != nil {
		vm.Misconfigured = true
		vm.Reason = err.Error()
		return vm
	}

	state := reviewenv.Down
	if hasRec && rec.State != "" {
		state = rec.State
	}
	vm.State = state
	vm.StateLabel = reviewEnvStateLabel(state)
	vm.Instance = rec.Instance
	vm.Port = rec.Port
	vm.Message = rec.Message
	if state == reviewenv.Up {
		vm.URL = rec.URL
	}
	switch state {
	case reviewenv.Starting, reviewenv.Up, reviewenv.Unhealthy:
		vm.Poll = true
	}
	switch state {
	case reviewenv.Down, reviewenv.Failed, reviewenv.TeardownFailed:
		vm.CanUp = true
	}
	switch state {
	case reviewenv.Starting, reviewenv.Up, reviewenv.Unhealthy, reviewenv.TeardownFailed:
		vm.CanDown = true
	}
	// The launch form's editable param inputs are only shown at rest (when CanUp), so
	// the reviewer sets e.g. ENV_NAME before spinning the env up.
	if vm.CanUp {
		vm.Params = reviewEnvParams(env, rec, hasRec)
	}
	return vm
}

// reviewEnvParams renders the env's repo-declared params as launch-form inputs,
// pre-filled with the value the last launch used (parsed from the record's captured
// ScriptEnv) when one exists, else the repo-declared default — so a relaunch after a
// failure keeps the operator's typed value. The injected DRAIVER_REVIEW_* vars are not
// repo params and never appear here.
func reviewEnvParams(env *pyramid.Environment, rec reviewenv.Record, hasRec bool) []reviewEnvParam {
	if env == nil || len(env.Params) == 0 {
		return nil
	}
	last := map[string]string{}
	if hasRec {
		for _, kv := range rec.ScriptEnv {
			if i := strings.IndexByte(kv, '='); i >= 0 {
				last[kv[:i]] = kv[i+1:]
			}
		}
	}
	out := make([]reviewEnvParam, 0, len(env.Params))
	for _, p := range env.Params {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		val := p.Default
		if v, ok := last[name]; ok {
			val = v
		}
		out = append(out, reviewEnvParam{Name: name, Value: val})
	}
	return out
}

// reviewEnvManager builds the lifecycle Manager for a web request. It reads config
// for the review-link host allowlist (the launch-time URL floor) and the reaper
// window; a config read error degrades to an unrestricted-but-still-scheme-floored
// manager rather than failing the page, matching repoSettingsPanel's best-effort
// config read.
func (s *Server) reviewEnvManager() *reviewenv.Manager {
	opt := reviewenv.Options{Root: s.root}
	if cfg, err := config.Load(""); err == nil {
		opt.ReviewLinkHosts = cfg.ReviewLinkHosts
		opt.MaxAge = time.Duration(cfg.ReviewEnvMaxAgeMinutes) * time.Minute
	}
	return reviewenv.New(opt)
}

// resolveReviewEnv gathers the gate inputs for an attempt: the repo's resolved
// reviewEnvironment name, the matching *pyramid.Environment, and the loaded pyramid.
// ok is false when there is no repo, no reviewEnvironment set, or the pyramid cannot
// be read — the same best-effort degradation the pyramid badge has. A set-but-invalid
// reviewEnvironment is NOT an ok=false here: it returns ok=true with env=nil so the
// caller can render the misconfiguration note (projectReviewEnv decides). envName is
// returned even when env is nil, so the note can name it.
func (s *Server) resolveReviewEnv(ctx context.Context, a project.Attempt) (envName string, env *pyramid.Environment, p *pyramid.Pyramid, ok bool) {
	if a.Repo == "" {
		return "", nil, nil, false
	}
	cfg, err := config.Load("")
	if err != nil {
		return "", nil, nil, false
	}
	envName = cfg.RepoSettings(a.Repo).ReviewEnvironment
	if envName == "" {
		return "", nil, nil, false
	}
	st, gotState := s.pyramidGit(ctx, a)
	if !gotState || st.Pyramid == nil {
		// reviewEnvironment is set but we cannot read the pyramid: ok=true with a nil
		// pyramid so projectReviewEnv renders the "unreachable" note rather than nothing.
		return envName, nil, nil, true
	}
	p = st.Pyramid
	if e, found := p.EnvironmentByName(envName); found {
		env = e
	}
	return envName, env, p, true
}

// reviewEnvPanel projects an attempt into the review-env panel VM, gathering the gate
// inputs (config + pyramid) and the current lifecycle Record. It is best-effort
// presentation like pyramidBadge: a nil result renders nothing. ctx bounds the git
// read behind s.pyramidGit.
func (s *Server) reviewEnvPanel(ctx context.Context, a project.Attempt) *reviewEnvVM {
	if a.State != project.Review {
		return nil
	}
	envName, _, p, ok := s.resolveReviewEnv(ctx, a)
	if !ok {
		return nil
	}
	rec, hasRec, err := s.reviewEnvManager().Load(a.Ticket, a.ID)
	if err != nil {
		hasRec = false
	}
	return projectReviewEnv(a, envName, p, rec, hasRec)
}

// renderReviewEnvPanel re-renders the review-env panel fragment for an attempt — the
// one-element outerHTML swap every review-env POST/poll returns. A nil VM (the gate
// no longer holds) renders the empty placeholder so the htmx swap clears the panel
// rather than leaving a stale one.
func (s *Server) renderReviewEnvPanel(w http.ResponseWriter, ctx context.Context, a project.Attempt) {
	s.render(w, "review-env-panel", s.reviewEnvPanel(ctx, a))
}

// handleReviewEnvUp launches a review environment (POST
// /ticket/{id}/{attempt}/review-env/up). Launch is slow (cut a worktree, run `up`,
// confirm healthchecks), so this is async: it writes Starting synchronously, spawns
// Launch on a background context that outlives the request, and returns the panel in
// the starting state — which then self-polls the status route until the env goes up
// (URL revealed) or failed. A click on an already-active env is an idempotent no-op.
func (s *Server) handleReviewEnvUp(w http.ResponseWriter, r *http.Request) {
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
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Re-check the full visibility gate server-side: the affordance only renders
	// behind it, so a POST that bypasses it (not Review, no/invalid reviewEnvironment)
	// is a bad request, not a 500 from Launch.
	_, env, _, ok := s.resolveReviewEnv(r.Context(), a)
	if !ok || env == nil || env.ReviewReady() != nil || a.State != project.Review {
		http.Error(w, "draiver: no review-ready environment is configured for this attempt in Review", http.StatusBadRequest)
		return
	}
	mgr := s.reviewEnvManager()
	// Idempotent: a launch already in motion or up is left alone — re-render and return.
	if rec, has, lerr := mgr.Load(id, att); lerr == nil && has && (rec.State == reviewenv.Starting || rec.State == reviewenv.Up) {
		s.renderReviewEnvPanel(w, r.Context(), a)
		return
	}
	// Read the launch form's per-param values BEFORE spawning the goroutine — the
	// request (and its parsed form) must not be touched once the handler returns.
	overrides := reviewEnvOverrides(r, env)
	if _, err := mgr.MarkStarting(a, env); err != nil {
		s.fail(w, err)
		return
	}
	// Launch on a background context so tearing down the request does not cancel the
	// bring-up mid-`up`. The goroutine persists the outcome (up+url / failed); the
	// panel's status poll surfaces it.
	go mgr.Launch(context.Background(), a, env, overrides)
	s.renderReviewEnvPanel(w, r.Context(), a)
}

// reviewEnvOverrides reads the launch form's per-param inputs (field name
// "param:<NAME>") for the env's DECLARED params only, returning the operator-supplied
// values (drv-020, resolution #45). A param with no field submitted is omitted (Launch
// falls back to its declared default); a submitted field for an undeclared name is
// ignored, so the web form can never inject an arbitrary env var into the scripts.
func reviewEnvOverrides(r *http.Request, env *pyramid.Environment) map[string]string {
	if env == nil || len(env.Params) == 0 {
		return nil
	}
	if err := r.ParseForm(); err != nil {
		return nil
	}
	out := map[string]string{}
	for _, p := range env.Params {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		if vals, ok := r.PostForm["param:"+name]; ok && len(vals) > 0 {
			out[name] = vals[0]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// handleReviewEnvDown tears a review environment down (POST
// /ticket/{id}/{attempt}/review-env/down). Unlike up this is synchronous: Teardown
// runs `down` then the confirm-down healthchecks and returns the terminal state —
// down (confirmed gone) or teardown-failed (a leak) — which is exactly the result the
// reviewer needs to see, surfaced in the one swapped fragment.
func (s *Server) handleReviewEnvDown(w http.ResponseWriter, r *http.Request) {
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
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err := s.reviewEnvManager().Teardown(r.Context(), a, "button"); err != nil {
		s.fail(w, err)
		return
	}
	s.renderReviewEnvPanel(w, r.Context(), a)
}

// handleReviewEnvStatus is the webui status poll (GET
// /ticket/{id}/{attempt}/review-env/status): the panel self-polls it while the env is
// in motion. It runs Probe — which re-checks the healthchecks of an up/unhealthy env
// and bumps LastActive, so a reviewer watching the panel holds the idle reaper off —
// then re-renders the panel from the refreshed record.
func (s *Server) handleReviewEnvStatus(w http.ResponseWriter, r *http.Request) {
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
	// Probe is a no-op for a record that is not up/unhealthy, so a Starting env (driven
	// by the launch goroutine) is simply re-read, and a Down/Failed one is unchanged.
	s.reviewEnvManager().Probe(r.Context(), a)
	s.renderReviewEnvPanel(w, r.Context(), a)
}
