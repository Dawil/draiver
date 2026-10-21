package web

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/reviewenv"
)

// reviewEnvsVM is the attempt page's whole review-environment surface (drv-034): one
// independent panel per review-ready environment the pyramid declares, plus an
// optional loud-misconfiguration note when the per-repo reviewEnvironment setting names
// a declared-but-unusable env. It replaces the single-valued drv-020 panel: an attempt
// in Review now lists EVERY review-ready env and launches/opens/tears-down each one
// concurrently and independently.
//
// A nil *reviewEnvsVM renders nothing (the gate's "not offered" outcome: not in Review,
// no pyramid, and nothing to flag). Envs is the ordered (by name) set of per-env
// panels; Misconfigured, when non-nil, is the single note for a bad reviewEnvironment
// setting so a typo still fails loudly rather than vanishing.
type reviewEnvsVM struct {
	Attempt       project.Attempt
	Envs          []*reviewEnvVM
	Misconfigured *reviewEnvVM
}

// Panels is the full render list: every review-ready env panel, then the
// misconfiguration note (if any), so the template ranges one sequence.
func (v *reviewEnvsVM) Panels() []*reviewEnvVM {
	if v == nil {
		return nil
	}
	out := append([]*reviewEnvVM(nil), v.Envs...)
	if v.Misconfigured != nil {
		out = append(out, v.Misconfigured)
	}
	return out
}

// reviewEnvVM drives one "review-env-panel" partial (drv-020, drv-034): the
// launch/teardown controls for a single long-lived, clickable instance of the feature
// under review. It is the UI face of one env's reviewenv lifecycle — the panel reads
// that env's persisted Record and renders the one of six lifecycle states it is in,
// with the URL revealed only while Up. A VM with Misconfigured=true renders a
// diagnostic note (no Up button) for the per-repo-setting-names-a-bad-env case.
type reviewEnvVM struct {
	Attempt project.Attempt
	// EnvName is the .test-pyramid.yaml environment name this panel launches — used
	// both as the display name and as the {env} path segment its POST/poll routes carry.
	EnvName string
	// EnvSlug is a DOM/CSS-safe stem of EnvName (drv-034), making each concurrent
	// panel's element id and htmx target unique (#review-env-<slug>).
	EnvSlug string
	// IsDefault marks the env the per-repo reviewEnvironment setting names — surfaced as
	// an optional highlight only; it no longer gates which envs are offered (drv-034).
	IsDefault bool
	// Misconfigured is true when the per-repo reviewEnvironment is set but names an
	// environment that is absent or not review-ready. Reason carries the human-facing
	// explanation (pyramid.Environment.ReviewReady's message, or "no such environment").
	// The panel then shows the note and no launch control.
	Misconfigured bool
	Reason        string
	// Missing marks a placeholder VM for a per-env re-render whose env no longer
	// resolves (gate dropped, env removed): the panel renders as the hidden placeholder
	// keyed to EnvSlug so the htmx swap clears just that panel.
	Missing bool
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

// reviewEnvSlug is a DOM/CSS-safe stem of an environment name: lowercased, every run
// of non-[a-z0-9] collapsed to a single '-', trimmed. It gives each concurrent panel a
// unique element id (#review-env-<slug>) and htmx target without assuming env names are
// already identifier-safe.
func reviewEnvSlug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "env"
	}
	return s
}

// projectReviewEnvs is the pure gate+projection for the whole surface (drv-034): it
// folds the attempt, the per-repo default name, the loaded pyramid, and a per-env
// record loader into the set of panels. It is I/O-free beyond the injected loadRec so
// the enumeration is exhaustively unit-testable off canned inputs (mirroring
// projectPyramid).
//
// It returns nil — render nothing — for a non-Review attempt, or a Review attempt with
// no review-ready env to offer and no misconfigured default to flag. Otherwise it lists
// EVERY review-ready env (ordered by name), marks the one the default names, and
// appends the loud-misconfiguration note when the default names a missing or
// non-review-ready env. loadRec returns the persisted Record for one env name.
func projectReviewEnvs(a project.Attempt, defaultName string, p *pyramid.Pyramid, loadRec func(env string) (reviewenv.Record, bool)) *reviewEnvsVM {
	if a.State != project.Review {
		return nil
	}
	vm := &reviewEnvsVM{Attempt: a}

	// No readable pyramid: the only thing to show is the misconfiguration note, and only
	// when a default is set (so a repo that names an env we cannot resolve fails loudly
	// rather than silently offering nothing).
	if p == nil {
		if defaultName != "" {
			vm.Misconfigured = newReviewEnvMisconfig(a, defaultName,
				"no .test-pyramid.yaml environment named "+defaultName+" is reachable")
			return vm
		}
		return nil
	}

	// Enumerate every review-ready env the pyramid declares, ordered by name for a
	// deterministic render.
	names := make([]string, 0, len(p.Environments))
	ready := map[string]*pyramid.Environment{}
	for i := range p.Environments {
		env := &p.Environments[i]
		if env.ReviewReady() == nil {
			names = append(names, env.Name)
			ready[env.Name] = env
		}
	}
	sort.Strings(names)
	for _, name := range names {
		rec, hasRec := loadRec(name)
		panel := projectReviewEnvPanel(a, ready[name], rec, hasRec)
		panel.IsDefault = name == defaultName
		vm.Envs = append(vm.Envs, panel)
	}

	// The per-repo default, when set, still drives the loud-misconfiguration note if it
	// names an env that is missing or not review-ready (preserving drv-020's gate as a
	// setting-validation surface). A default that resolves to a review-ready env is
	// already in the list and merely highlighted.
	if defaultName != "" {
		if _, isReady := ready[defaultName]; !isReady {
			reason := "no environment named " + defaultName + " in .test-pyramid.yaml"
			if env, ok := p.EnvironmentByName(defaultName); ok {
				if err := env.ReviewReady(); err != nil {
					reason = err.Error()
				}
			}
			vm.Misconfigured = newReviewEnvMisconfig(a, defaultName, reason)
		}
	}

	if len(vm.Envs) == 0 && vm.Misconfigured == nil {
		return nil
	}
	return vm
}

// newReviewEnvMisconfig builds the loud-failure note panel for a reviewEnvironment that
// names a missing or non-review-ready env.
func newReviewEnvMisconfig(a project.Attempt, envName, reason string) *reviewEnvVM {
	return &reviewEnvVM{
		Attempt:       a,
		EnvName:       envName,
		EnvSlug:       reviewEnvSlug(envName),
		Misconfigured: true,
		Reason:        reason,
	}
}

// projectReviewEnvPanel projects one resolved, review-ready environment plus its
// current Record into a panel VM (a zero Record / hasRec=false is the resting Down
// state). The caller has already confirmed the env is review-ready.
func projectReviewEnvPanel(a project.Attempt, env *pyramid.Environment, rec reviewenv.Record, hasRec bool) *reviewEnvVM {
	vm := &reviewEnvVM{Attempt: a, EnvName: env.Name, EnvSlug: reviewEnvSlug(env.Name)}
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

// reviewEnvGate gathers the enumeration inputs for an attempt: the repo's optional
// reviewEnvironment default (an optional highlight since drv-034, no longer a gate) and
// the loaded pyramid. A config read error or no repo yields a blank default; a pyramid
// that cannot be read yields p=nil — the same best-effort degradation the pyramid badge
// has.
func (s *Server) reviewEnvGate(ctx context.Context, a project.Attempt) (defaultName string, p *pyramid.Pyramid) {
	if a.Repo == "" {
		return "", nil
	}
	if cfg, err := config.Load(""); err == nil {
		defaultName = cfg.RepoSettings(a.Repo).ReviewEnvironment
	}
	if st, ok := s.pyramidGit(ctx, a); ok {
		p = st.Pyramid
	}
	return defaultName, p
}

// reviewEnvSurface projects an attempt into the whole review-env surface VM (drv-034):
// one panel per review-ready env plus an optional misconfiguration note. It is
// best-effort presentation like pyramidBadge — a nil result renders nothing. ctx bounds
// the git read behind s.pyramidGit.
func (s *Server) reviewEnvSurface(ctx context.Context, a project.Attempt) *reviewEnvsVM {
	if a.State != project.Review {
		return nil
	}
	defaultName, p := s.reviewEnvGate(ctx, a)
	mgr := s.reviewEnvManager()
	load := func(env string) (reviewenv.Record, bool) {
		rec, ok, err := mgr.Load(a.Ticket, a.ID, env)
		if err != nil {
			return reviewenv.Record{}, false
		}
		return rec, ok
	}
	return projectReviewEnvs(a, defaultName, p, load)
}

// resolveReviewEnvReady resolves one named environment for a handler and confirms it is
// a declared, review-ready env on the attempt's pyramid. ok is false (the loud-
// misconfiguration refusal, now per-env) when there is no repo, the pyramid cannot be
// read, the name is not declared, or the env is not review-ready.
func (s *Server) resolveReviewEnvReady(ctx context.Context, a project.Attempt, name string) (*pyramid.Environment, bool) {
	if a.Repo == "" || strings.TrimSpace(name) == "" {
		return nil, false
	}
	_, p := s.reviewEnvGate(ctx, a)
	if p == nil {
		return nil, false
	}
	env, found := p.EnvironmentByName(name)
	if !found || env.ReviewReady() != nil {
		return nil, false
	}
	return env, true
}

// renderReviewEnvPanel re-renders ONE env's panel fragment — the one-element outerHTML
// swap every review-env POST/poll returns (drv-034 scopes the swap to the named env, so
// one panel updating never disturbs a sibling). A nil VM (the env no longer resolves or
// the gate dropped) renders the empty placeholder keyed to that env's id so the htmx
// swap clears just that panel rather than leaving a stale one.
func (s *Server) renderReviewEnvPanel(w http.ResponseWriter, ctx context.Context, a project.Attempt, envName string) {
	s.render(w, "review-env-panel", s.reviewEnvPanelFor(ctx, a, envName))
}

// reviewEnvPanelFor builds the single-env panel VM used by the per-env POST/poll
// re-render. It resolves the named review-ready env and projects its current record; a
// name that no longer resolves yields a placeholder VM (right id, Missing=true) so the
// swap clears the panel without erroring.
func (s *Server) reviewEnvPanelFor(ctx context.Context, a project.Attempt, envName string) *reviewEnvVM {
	if a.State != project.Review {
		return &reviewEnvVM{Attempt: a, EnvSlug: reviewEnvSlug(envName), Missing: true}
	}
	env, ok := s.resolveReviewEnvReady(ctx, a, envName)
	if !ok {
		return &reviewEnvVM{Attempt: a, EnvName: envName, EnvSlug: reviewEnvSlug(envName), Missing: true}
	}
	rec, hasRec, err := s.reviewEnvManager().Load(a.Ticket, a.ID, envName)
	if err != nil {
		hasRec = false
	}
	return projectReviewEnvPanel(a, env, rec, hasRec)
}

// handleReviewEnvUp launches a named review environment (POST
// /ticket/{id}/{attempt}/review-env/{env}/up). Launch is slow (cut a worktree, run
// `up`, confirm healthchecks), so this is async: it writes Starting synchronously,
// spawns Launch on a background context that outlives the request, and returns that
// env's panel in the starting state — which then self-polls the status route until the
// env goes up (URL revealed) or failed. A click on an already-active env is an
// idempotent no-op. The {env} segment names which of the attempt's review-ready envs to
// launch; it is validated against the pyramid, so a bogus name is a 400, not a 500.
func (s *Server) handleReviewEnvUp(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	envName := r.PathValue("env")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Re-check the gate server-side, now per-env: the affordance only renders behind it,
	// so a POST naming an env that is not a declared, review-ready env on this attempt's
	// pyramid is a bad request (writing nothing), not a 500 from Launch.
	env, ok := s.resolveReviewEnvReady(r.Context(), a, envName)
	if !ok || a.State != project.Review {
		http.Error(w, "draiver: not a declared, review-ready environment for this attempt in Review", http.StatusBadRequest)
		return
	}
	mgr := s.reviewEnvManager()
	// Idempotent: a launch of THIS env already in motion or up is left alone — re-render
	// and return (a sibling env's state is untouched).
	if rec, has, lerr := mgr.Load(id, att, envName); lerr == nil && has && (rec.State == reviewenv.Starting || rec.State == reviewenv.Up) {
		s.renderReviewEnvPanel(w, r.Context(), a, envName)
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
	s.renderReviewEnvPanel(w, r.Context(), a, envName)
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

// handleReviewEnvDown tears a named review environment down (POST
// /ticket/{id}/{attempt}/review-env/{env}/down). Unlike up this is synchronous:
// Teardown runs `down` then the confirm-down healthchecks for THAT env and returns its
// terminal state — down (confirmed gone) or teardown-failed (a leak) — surfaced in the
// one swapped fragment, leaving any sibling env untouched. The {env} segment is
// validated so a bogus name is a 400 that writes nothing (the per-env refusal).
func (s *Server) handleReviewEnvDown(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "draiver: cross-origin request refused", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	envName := r.PathValue("env")
	if !s.root.AttemptExists(id, att) {
		http.NotFound(w, r)
		return
	}
	a, err := project.LoadAttempt(s.root, id, att)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, ok := s.resolveReviewEnvReady(r.Context(), a, envName); !ok {
		http.Error(w, "draiver: not a declared, review-ready environment for this attempt", http.StatusBadRequest)
		return
	}
	if _, err := s.reviewEnvManager().Teardown(r.Context(), a, envName, "button"); err != nil {
		s.fail(w, err)
		return
	}
	s.renderReviewEnvPanel(w, r.Context(), a, envName)
}

// handleReviewEnvStatus is the webui status poll (GET
// /ticket/{id}/{attempt}/review-env/{env}/status): a panel self-polls it while its env
// is in motion. It runs Probe for THAT env — which re-checks its healthchecks and bumps
// LastActive, so a reviewer watching the panel holds the idle reaper off — then
// re-renders that env's panel from the refreshed record. A bogus env name renders the
// placeholder (the swap clears the panel) rather than erroring the poll loop.
func (s *Server) handleReviewEnvStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	att := r.PathValue("attempt")
	envName := r.PathValue("env")
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
	s.reviewEnvManager().Probe(r.Context(), a, envName)
	s.renderReviewEnvPanel(w, r.Context(), a, envName)
}
