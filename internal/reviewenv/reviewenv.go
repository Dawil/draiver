// Package reviewenv owns the lifecycle of a long-lived review environment — a
// real, running instance of the feature under review that a human clicks to drive
// (drv-020). It is the long-lived counterpart to the pyramid's ephemeral `draiver
// test` up/down: where a test rung stands an env up, runs, and tears it down inside
// one process, a review env persists *between* requests (down → starting → up+url →
// unhealthy / teardown-failed), tracked in an out-of-band record so a leaked
// environment is a controlplane-detectable fault rather than a silent orphan.
//
// The lifecycle sits on the three-party seam: the *agent* authors and parameterises
// the environment's up/down/healthcheck scripts and its URL template in
// .test-pyramid.yaml; this package (the *deterministic controlplane*) owns the
// lifecycle — it cuts a dedicated review worktree at the reviewed commit, injects a
// unique instance slug and an allocated port, runs `up`, confirms readiness with the
// environment's healthchecks before revealing the URL, and on teardown runs `down`
// then re-runs the same healthchecks with the inverted expectation (now-red =
// confirmed down; still-green = leak) to close the loop; and the *human* just clicks
// the URL.
//
// It holds no authoritative state of its own: the per-attempt record (Record) is
// rebuildable runtime state persisted at store.ReviewEnvPath, out of the hash chain,
// exactly like the daemon's desired-marker. Every operation re-derives truth from
// that record plus git, so a restarted daemon or a fresh webui process reconciles a
// review env from disk alone.
package reviewenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// Env-var names draiver injects into every up/down/healthcheck execution — the
// parameterisation contract (drv-020). A repo's scripts consume these to namespace
// an instance (ports, container/volume/DB names) so concurrent review envs on
// different attempts never collide. They are also the variables URL and any
// `params` default may reference via `$VAR` / `${VAR}`.
const (
	// EnvInstance is a unique, filesystem/DNS-safe slug for this review env instance
	// (derived from the ticket + attempt), e.g. "drv-020-0001".
	EnvInstance = "DRAIVER_REVIEW_INSTANCE"
	// EnvPort is a port draiver allocated as free on the host at launch, for a local
	// script to bind. It is a candidate the script may use or ignore (a remote deploy
	// picks its own); draiver never assumes the service is reachable at it — the URL
	// the reviewer gets is whatever the declared template resolves to.
	EnvPort = "DRAIVER_REVIEW_PORT"
)

// State is the review env's lifecycle phase — the persistent, out-of-band status the
// controlplane tracks between requests. It is a small closed set the webui panel and
// the reaper both branch on.
type State string

const (
	// Down is the resting state: no review env is running (also the implicit state
	// when no record exists at all). The panel offers an Up button.
	Down State = "down"
	// Starting means `up` is executing or its healthchecks have not yet gone green —
	// the slow phase the async panel shows a spinner for. No URL yet.
	Starting State = "starting"
	// Up means `up` succeeded and every healthcheck is green: the URL is live and
	// clickable. The reviewer drives the feature here.
	Up State = "up"
	// Unhealthy means a launched env that was Up has since failed its healthchecks —
	// the service died under it. The URL is withheld; a human can tear down or retry.
	Unhealthy State = "unhealthy"
	// Failed means the launch itself failed: `up` exited non-zero (infra fault) or a
	// post-up healthcheck was red (never came ready). Message carries the probe/so
	// output; the worktree has been torn down so nothing is left half-live.
	Failed State = "failed"
	// TeardownFailed means `down` ran but the confirm-down healthcheck is still green:
	// the environment did not actually go away — a leak. The controlplane surfaces it
	// rather than silently assuming teardown worked; the worktree is kept so a human
	// (or a retry) can finish the job.
	TeardownFailed State = "teardown-failed"
)

// Active reports whether a state denotes a live-or-leaked env the reaper and the
// done-teardown sweep should still consider (as opposed to a resting Down/Failed).
func (s State) Active() bool {
	switch s {
	case Starting, Up, Unhealthy, TeardownFailed:
		return true
	default:
		return false
	}
}

// Record is the persisted review-env lifecycle state for one attempt
// (store.ReviewEnvPath → reviewenv.json). It is self-contained by design: it stores
// the resolved `down` command, the healthcheck probes, and the exact parameterisation
// environment captured at launch, so teardown and confirm-down run the *same* scripts
// with the *same* parameterisation the env was brought up with — without re-reading
// (and risking drift from) the .test-pyramid.yaml. It is rebuildable runtime state,
// out of the hash chain.
type Record struct {
	State State `json:"state"`
	// Environment is the .test-pyramid.yaml environment name that was launched (the
	// repo's resolved `reviewEnvironment`).
	Environment string `json:"environment"`
	// Instance is the unique slug injected as DRAIVER_REVIEW_INSTANCE.
	Instance string `json:"instance"`
	// Port is the host port injected as DRAIVER_REVIEW_PORT.
	Port int `json:"port"`
	// Commit is the reviewed commit the review worktree was cut at.
	Commit string `json:"commit"`
	// Worktree is the absolute path of the dedicated review checkout (removed on a
	// confirmed teardown; kept on a leak or a failed launch's debugging window).
	Worktree string `json:"worktree,omitempty"`
	// URL is the expanded, host-floor-validated review link — populated only while Up.
	URL string `json:"url,omitempty"`
	// Down is the resolved teardown command captured at launch.
	Down string `json:"down,omitempty"`
	// Healthchecks are the readiness probes captured at launch — run green-expected on
	// confirm-up and red-expected on confirm-down.
	Healthchecks []pyramid.Healthcheck `json:"healthchecks,omitempty"`
	// ScriptEnv is the exact KEY=VALUE parameterisation injected at launch (the
	// DRAIVER_REVIEW_* vars plus any declared params), replayed verbatim into down and
	// the healthchecks so teardown parameterisation matches bring-up.
	ScriptEnv []string `json:"script_env,omitempty"`
	// Message carries the last probe output or failure detail, surfaced on the panel.
	Message string `json:"message,omitempty"`
	// Started is when the current launch began; LastActive is bumped on launch and on
	// every status poll, so the idle reaper tears down an env no one is watching while
	// leaving an actively-driven one alone. Updated stamps the last write.
	Started    time.Time `json:"started"`
	LastActive time.Time `json:"last_active"`
	Updated    time.Time `json:"updated"`
}

// Options configure a Manager.
type Options struct {
	// Root is the data root whose ReviewEnvPath holds each attempt's record.
	Root store.Root
	// ReviewLinkHosts is config.Config.ReviewLinkHosts — the optional host allowlist a
	// revealed URL must satisfy on top of the hardcoded http/https scheme floor.
	ReviewLinkHosts []string
	// MaxAge is the idle/max-age reaper window (config.ReviewEnvMaxAgeMinutes). An env
	// untouched (no launch/poll) for longer is torn down by Sweep. Zero disables the
	// reaper (teardown then only on the button or ticket-Done).
	MaxAge time.Duration
	// Base overrides the review-worktree base directory. Empty derives it under the
	// user cache dir. Tests set it to a temp dir.
	Base string
	// Now is an injectable clock (nil → time.Now), so the reaper is unit-testable.
	Now func() time.Time
	// Logf is an optional operational logger for best-effort warnings (a down failure,
	// a leak). Nil discards.
	Logf func(string, ...any)
}

// Manager runs the review-env lifecycle. It is stateless beyond its Options — every
// method re-derives truth from the on-disk record plus git — so it is safe to
// construct one per request (the webui) or one per daemon (the reconciler).
type Manager struct {
	opt  Options
	now  func() time.Time
	base string
}

// New returns a Manager. It resolves the review-worktree base once (deferring a
// missing user-cache-dir error to first use is avoided by resolving eagerly here only
// when Base is set; otherwise defaultBase is resolved lazily per-op so construction
// never fails).
func New(opt Options) *Manager {
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{opt: opt, now: now, base: opt.Base}
}

// Logf logs a best-effort operational message, if a logger is configured.
func (m *Manager) logf(format string, args ...any) {
	if m.opt.Logf != nil {
		m.opt.Logf(format, args...)
	}
}

// Load returns the attempt's current record and whether one exists. A missing file
// is the resting Down state, reported as ok=false with a zero Record, so callers can
// treat "never launched" and an explicit Down record alike.
func (m *Manager) Load(id, attempt string) (Record, bool, error) {
	path := m.opt.Root.ReviewEnvPath(id, attempt)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Record{}, false, nil
		}
		return Record{}, false, fmt.Errorf("reviewenv: read %s: %w", path, err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, false, fmt.Errorf("reviewenv: parse %s: %w", path, err)
	}
	return rec, true, nil
}

// save writes the record atomically (temp file + rename), stamping Updated.
func (m *Manager) save(id, attempt string, rec Record) (Record, error) {
	rec.Updated = m.now()
	path := m.opt.Root.ReviewEnvPath(id, attempt)
	if err := m.opt.Root.EnsureAttemptDirs(id, attempt); err != nil {
		return rec, err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return rec, fmt.Errorf("reviewenv: encode record: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".reviewenv-*.json.tmp")
	if err != nil {
		return rec, fmt.Errorf("reviewenv: temp record: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return rec, fmt.Errorf("reviewenv: write temp record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return rec, fmt.Errorf("reviewenv: close temp record: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return rec, fmt.Errorf("reviewenv: replace record: %w", err)
	}
	return rec, nil
}

// Launch stands a review environment up for an attempt: resolve the reviewed commit,
// allocate a port and a unique instance slug, cut a dedicated detached review
// worktree at that commit, run `up` with the parameterisation injected, confirm the
// healthchecks are green, and reveal the host-floor-validated URL. The returned
// Record carries the outcome: Up with a URL on success, Failed with the probe/so
// output on a red `up` or healthcheck — in which case the worktree is torn down so a
// failed launch leaves nothing half-live.
//
// Launch is self-healing against a stale prior attempt: any existing review worktree
// for this attempt is force-removed first, so a relaunch after a Failed/TeardownFailed
// record starts from a clean slate.
func (m *Manager) Launch(ctx context.Context, a project.Attempt, env *pyramid.Environment) (Record, error) {
	if a.Repo == "" {
		return Record{}, fmt.Errorf("reviewenv: attempt %s/%s records no repo", a.Ticket, a.ID)
	}
	if err := env.ReviewReady(); err != nil {
		return Record{}, fmt.Errorf("reviewenv: %w", err)
	}
	commit, err := m.reviewedCommit(ctx, a)
	if err != nil {
		return Record{}, err
	}

	instance := slug(a.Ticket, a.ID)
	port, err := allocatePort()
	if err != nil {
		return Record{}, fmt.Errorf("reviewenv: allocate port: %w", err)
	}
	vars := map[string]string{
		EnvInstance: instance,
		EnvPort:     fmt.Sprintf("%d", port),
	}
	// Repo-declared params fill in UNDER the injected DRAIVER_REVIEW_* vars: a param
	// may not shadow an injected one (the injected values always win).
	for _, p := range env.Params {
		if name := strings.TrimSpace(p.Name); name != "" {
			if _, injected := vars[name]; !injected {
				vars[name] = p.Default
			}
		}
	}
	scriptEnv := envSlice(vars)
	wtPath := m.worktreePath(a)

	rec := Record{
		State:        Starting,
		Environment:  env.Name,
		Instance:     instance,
		Port:         port,
		Commit:       commit,
		Worktree:     wtPath,
		Down:         env.Down,
		Healthchecks: env.Healthchecks,
		ScriptEnv:    scriptEnv,
		Started:      m.now(),
		LastActive:   m.now(),
	}
	if rec, err = m.save(a.Ticket, a.ID, rec); err != nil {
		return rec, err
	}

	// Clean slate: drop any worktree a prior (failed/leaked) launch left at this path.
	m.removeWorktree(ctx, a.Repo, wtPath)
	if err := m.addWorktree(ctx, a.Repo, wtPath, commit); err != nil {
		return m.fail(a, rec, fmt.Sprintf("could not cut review worktree: %v", err))
	}

	// up — a non-zero exit is an infrastructure fault; tear the worktree back down so
	// a failed launch leaks nothing.
	if out, err := m.runScript(ctx, wtPath, env.Up, scriptEnv); err != nil {
		m.removeWorktree(ctx, a.Repo, wtPath)
		return m.fail(a, rec, fmt.Sprintf("up failed: %v\n%s", err, out))
	}

	// confirm up — every healthcheck must be green before the URL is revealed.
	for _, hc := range env.Healthchecks {
		if out, err := m.runScript(ctx, wtPath, hc.Script, scriptEnv); err != nil {
			m.runScript(ctx, wtPath, env.Down, scriptEnv) // best-effort down
			m.removeWorktree(ctx, a.Repo, wtPath)
			return m.fail(a, rec, fmt.Sprintf("healthcheck %q red after up — environment never came ready:\n%s", hc.Name, strings.TrimSpace(out)))
		}
	}

	// Reveal the URL only now, re-validated through the same scheme floor + optional
	// host allowlist the review-link mechanism applies everywhere.
	url := expandURL(env.URL, vars)
	if strings.TrimSpace(url) != "" {
		if err := event.ValidateLink(event.Link{Rel: "review", Href: url}, m.opt.ReviewLinkHosts); err != nil {
			m.runScript(ctx, wtPath, env.Down, scriptEnv)
			m.removeWorktree(ctx, a.Repo, wtPath)
			return m.fail(a, rec, fmt.Sprintf("review url %q rejected: %v", url, err))
		}
	}

	rec.State = Up
	rec.URL = url
	rec.Message = ""
	rec.LastActive = m.now()
	return m.save(a.Ticket, a.ID, rec)
}

// fail records a launch failure, preserving the already-persisted identity fields.
func (m *Manager) fail(a project.Attempt, rec Record, msg string) (Record, error) {
	rec.State = Failed
	rec.URL = ""
	rec.Worktree = ""
	rec.Message = msg
	return m.save(a.Ticket, a.ID, rec)
}

// Teardown tears a review environment down: run the stored `down`, then re-run the
// stored healthchecks with the inverted expectation to confirm it actually went away.
// All-red confirms teardown — the worktree is removed and the record returns to Down.
// Any still-green probe is a leak: the record becomes TeardownFailed (the worktree is
// kept) and the controlplane surfaces it rather than silently assuming teardown
// worked. reason is recorded for the operator (button / ticket done / idle reaper).
//
// Teardown is idempotent: a missing record or one already Down/Failed is a no-op.
func (m *Manager) Teardown(ctx context.Context, a project.Attempt, reason string) (Record, error) {
	rec, ok, err := m.Load(a.Ticket, a.ID)
	if err != nil {
		return Record{}, err
	}
	if !ok || (rec.State == Down || rec.State == Failed) {
		return rec, nil
	}

	// down — best-effort; a down failure never blocks the confirm-down probe (the
	// probe, not down's exit code, is the authority on whether the env is gone).
	if strings.TrimSpace(rec.Down) != "" {
		if out, derr := m.runScript(ctx, rec.Worktree, rec.Down, rec.ScriptEnv); derr != nil {
			m.logf("reviewenv: %s/%s down failed (continuing to confirm): %v\n%s", a.Ticket, a.ID, derr, out)
		}
	}

	// confirm down — the same probes, red-expected. Any that stays green means the
	// service is still answering: a leak.
	var stillGreen []string
	for _, hc := range rec.Healthchecks {
		if _, err := m.runScript(ctx, rec.Worktree, hc.Script, rec.ScriptEnv); err == nil {
			stillGreen = append(stillGreen, hc.Name)
		}
	}
	if len(stillGreen) > 0 {
		rec.State = TeardownFailed
		rec.URL = ""
		rec.Message = fmt.Sprintf("teardown (%s): down ran but healthcheck(s) %s still green — environment did not go away (leak)", reason, strings.Join(stillGreen, ", "))
		m.logf("reviewenv: %s/%s LEAK — %s", a.Ticket, a.ID, rec.Message)
		return m.save(a.Ticket, a.ID, rec)
	}

	// Confirmed down — remove the review worktree and return to rest.
	m.removeWorktree(ctx, a.Repo, rec.Worktree)
	rec.State = Down
	rec.URL = ""
	rec.Worktree = ""
	rec.Message = fmt.Sprintf("torn down (%s); confirm-down healthchecks red", reason)
	return m.save(a.Ticket, a.ID, rec)
}

// Probe re-runs the healthchecks of an Up (or Unhealthy) env and updates the record:
// all-green keeps it Up and bumps LastActive (so a reviewer actively polling the panel
// holds the idle reaper off); any red flips it to Unhealthy with the probe output. A
// record in any other state is returned unchanged — there is nothing live to probe.
// It is the read the webui status poll calls.
func (m *Manager) Probe(ctx context.Context, a project.Attempt) (Record, error) {
	rec, ok, err := m.Load(a.Ticket, a.ID)
	if err != nil || !ok {
		return rec, err
	}
	if rec.State != Up && rec.State != Unhealthy {
		return rec, nil
	}
	var red []string
	for _, hc := range rec.Healthchecks {
		if _, err := m.runScript(ctx, rec.Worktree, hc.Script, rec.ScriptEnv); err != nil {
			red = append(red, hc.Name)
		}
	}
	if len(red) > 0 {
		rec.State = Unhealthy
		rec.Message = fmt.Sprintf("healthcheck(s) %s red", strings.Join(red, ", "))
	} else {
		rec.State = Up
		rec.Message = ""
		rec.LastActive = m.now()
	}
	return m.save(a.Ticket, a.ID, rec)
}

// Sweep is the controlplane's per-tick review-env move: it tears down every active
// review env whose attempt is Done (the ticket closed) or that has gone idle past the
// reaper window. It is driven by the injectable clock, so a test can trip the reaper
// deterministically. Errors on an individual attempt are logged and skipped so one bad
// record never stalls the sweep. It returns the attempts it tore down, for the caller
// to log/observe.
func (m *Manager) Sweep(ctx context.Context) ([]worktree.Key, error) {
	tickets, err := m.opt.Root.ListTickets()
	if err != nil {
		return nil, err
	}
	var reaped []worktree.Key
	for _, id := range tickets {
		attempts, err := m.opt.Root.ListAttempts(id)
		if err != nil {
			m.logf("reviewenv: sweep list attempts %s: %v", id, err)
			continue
		}
		for _, att := range attempts {
			rec, ok, err := m.Load(id, att)
			if err != nil {
				m.logf("reviewenv: sweep load %s/%s: %v", id, att, err)
				continue
			}
			if !ok || !rec.State.Active() {
				continue
			}
			a, err := project.LoadAttempt(m.opt.Root, id, att)
			if err != nil {
				m.logf("reviewenv: sweep attempt %s/%s: %v", id, att, err)
				continue
			}
			reason := m.reapReason(a, rec)
			if reason == "" {
				continue
			}
			if _, err := m.Teardown(ctx, a, reason); err != nil {
				m.logf("reviewenv: sweep teardown %s/%s: %v", id, att, err)
				continue
			}
			reaped = append(reaped, worktree.Key{Ticket: id, Attempt: att})
		}
	}
	return reaped, nil
}

// reapReason decides whether an active review env should be torn down this sweep, and
// why: the ticket going Done always reaps (the feature is no longer under review); an
// idle env past MaxAge reaps as the forgotten-env safety net. A TeardownFailed (leak)
// record is left alone — re-running down on a confirmed leak is the operator's call,
// not the reaper's. Empty string means "leave it running".
func (m *Manager) reapReason(a project.Attempt, rec Record) string {
	if rec.State == TeardownFailed {
		return ""
	}
	if a.State == project.Done {
		return "ticket marked done"
	}
	if m.opt.MaxAge > 0 && m.now().Sub(rec.LastActive) > m.opt.MaxAge {
		return fmt.Sprintf("idle past %s", m.opt.MaxAge)
	}
	return ""
}

// reviewedCommit resolves the commit under review for an attempt: the live checkout's
// HEAD when one exists, else the attempt branch tip — the exact two-tier fallback the
// pyramid badge uses (internal/web/pyramid.go:gatherPyramidState), so the review env
// runs the same code the reviewer is judging even after the coding worktree is
// reclaimed.
func (m *Manager) reviewedCommit(ctx context.Context, a project.Attempt) (string, error) {
	wm, err := worktree.NewManager(a.Repo)
	if err != nil {
		return "", fmt.Errorf("reviewenv: worktree manager: %w", err)
	}
	key := worktree.Key{Ticket: a.Ticket, Attempt: a.ID}
	if wt, ok, err := wm.Locate(ctx, key); err != nil {
		return "", fmt.Errorf("reviewenv: locate worktree: %w", err)
	} else if ok {
		sha, err := worktree.HeadSHA(ctx, wt.Path)
		if err != nil {
			return "", fmt.Errorf("reviewenv: head of %s: %w", wt.Path, err)
		}
		return sha, nil
	}
	sha, ok, err := wm.BranchSHA(ctx, key)
	if err != nil {
		return "", fmt.Errorf("reviewenv: branch sha: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("reviewenv: attempt %s/%s has neither a live checkout nor a branch to review", a.Ticket, a.ID)
	}
	return sha, nil
}

// worktreePath is where this attempt's review checkout lives: a dedicated base,
// distinct from the coding worktree, keyed by a hash of the repo so review envs for
// same-named tickets across repos never collide.
func (m *Manager) worktreePath(a project.Attempt) string {
	base := m.base
	if base == "" {
		if cache, err := os.UserCacheDir(); err == nil {
			base = filepath.Join(cache, "draiver", "review-envs")
		} else {
			base = filepath.Join(os.TempDir(), "draiver-review-envs")
		}
	}
	sum := sha256.Sum256([]byte(a.Repo))
	repoHash := hex.EncodeToString(sum[:])[:12]
	return filepath.Join(base, repoHash, a.Ticket, a.ID)
}

// addWorktree cuts a dedicated DETACHED worktree at commit — detached precisely so it
// does not claim the attempt's coding branch (draiver/<ticket>/<attempt>), which the
// coding worktree already holds. It is the review env's isolation primitive: a fresh
// checkout of the reviewed commit, distinct from any coding checkout, so review/test
// data mutations never touch the coding tree and concurrent review envs each get their
// own checkout.
func (m *Manager) addWorktree(ctx context.Context, repo, path, commit string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("reviewenv: mkdir %s: %w", filepath.Dir(path), err)
	}
	if _, err := gitRun(ctx, repo, "worktree", "add", "--detach", path, commit); err != nil {
		return err
	}
	return nil
}

// removeWorktree force-removes a review checkout, pruning the admin entry if the
// directory already vanished. Best-effort: a missing/never-created worktree is not an
// error. An empty path is a no-op.
func (m *Manager) removeWorktree(ctx context.Context, repo, path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if _, err := gitRun(ctx, repo, "worktree", "remove", "--force", path); err != nil {
		// The checkout may already be gone (a crash, a prior partial teardown): prune
		// the stale admin entry so a later add at the same path succeeds.
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			gitRun(ctx, repo, "worktree", "prune")
			return
		}
		m.logf("reviewenv: remove worktree %s: %v", path, err)
	}
}

// runScript runs one up/down/healthcheck command via `sh -c` in the review worktree
// with the parameterisation injected — the same `sh -c`, working-directory, and
// os.Environ()+extra env layering the pyramid runner and the agent adapter use, so a
// repo's scripts behave identically whether draiver runs them for `draiver test` or a
// review env. It returns combined stdout+stderr for the panel/message.
func (m *Manager) runScript(ctx context.Context, dir, script string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// gitRun runs `git -C repo args...`, folding stderr into the error for legibility.
func gitRun(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// allocatePort asks the OS for a free TCP port by binding an ephemeral listener and
// reading the assigned port, then releasing it. The number is what matters — a
// location-agnostic candidate injected as DRAIVER_REVIEW_PORT for the script to bind;
// draiver never assumes the service is reachable there (a remote deploy ignores it).
// Binding on 127.0.0.1 only probes for a locally-free number; it makes no claim about
// where the env ultimately listens.
func allocatePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// slug builds a filesystem/DNS-safe instance id from the ticket and attempt:
// lowercased, every run of non-[a-z0-9] collapsed to a single '-', trimmed. It is
// deterministic per attempt, so one attempt's review env has one stable namespace and
// two different attempts' never collide.
func slug(ticket, attempt string) string {
	raw := strings.ToLower(ticket + "-" + attempt)
	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// envSlice renders a var map as a sorted KEY=VALUE slice (sorted for a deterministic
// record and reproducible test assertions).
func envSlice(vars map[string]string) []string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(vars))
	for _, k := range keys {
		out = append(out, k+"="+vars[k])
	}
	return out
}

// expandURL expands a URL template's $VAR / ${VAR} references against the injected
// parameterisation vars, leaving an unknown reference empty. A blank template yields a
// blank URL (an env that exposes nothing to click).
func expandURL(tmpl string, vars map[string]string) string {
	if strings.TrimSpace(tmpl) == "" {
		return ""
	}
	return os.Expand(tmpl, func(k string) string { return vars[k] })
}
