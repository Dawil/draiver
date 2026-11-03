// Package bddexec is the in-process controlplane executor the board's "rerun"
// affordance (drv-019) delegates to. It re-executes a repo's bound BDD rung over
// its environment (drv-012: up → healthcheck → run → down) and captures a fresh
// side-by-side artefact set (drv-017 bddartefact), so a human can regenerate the
// evidence a report renders (drv-018) without leaving the board.
//
// It is the BDD counterpart to internal/land's git-over-the-gateway seam: the web
// layer never runs the BDD tooling itself — it calls Rerun, exactly as the git
// controls call land.Push/land.Sync. The healthcheck precondition lives here, not
// in the handler, so a red environment yields StatusBlocked ("pass the ball")
// rather than a bogus report.
//
// Scope note: `draiver test`'s climb (cmd/test.go) captures once, post-climb,
// gated on all-green + --log; a rerun instead executes a single rung and captures
// its output every time. The control flow genuinely differs, so this re-expresses
// the thin up/healthcheck/run/capture orchestration rather than sharing cmd's — the
// reuse is at the building-block level (pyramid, bddartefact). Converging cmd's
// orchestration onto this is still worth a follow-up; what has converged (drv-047) is
// pyramid *resolution*: a rerun now routes through the shared overlay.ResolvePyramid,
// the same seam `draiver test`/`draiver promote` use, so an overlay-only codebase
// reruns its overlay's bound BDD rung with DRAIVER_CODEBASE_DIR / DRAIVER_OVERLAY_DIR
// threaded into every shell step, cwd the codebase worktree.
package bddexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/bddartefact"
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/overlay"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// Status is the verdict of a rerun, mirroring `draiver test`'s exit-code taxonomy
// (cmd/root.go): a red healthcheck blocks (not a code fault), a failed env `up` is
// infrastructure, a failed run is a code fault, and "no BDD rung" means there is
// nothing to rerun.
type Status string

const (
	// StatusOK: the bound rung ran green and a fresh artefact set was captured.
	StatusOK Status = "ok"
	// StatusBlocked: a healthcheck was red — the environment is not ready, so the
	// rerun was refused before running (pass the ball), no report produced.
	StatusBlocked Status = "blocked"
	// StatusEnvFault: the environment's `up` failed — an infrastructure fault.
	StatusEnvFault Status = "env-fault"
	// StatusRunFailed: the rung executed but exited non-zero (a code fault). A report
	// may still have been captured — failing evidence is evidence worth keeping.
	StatusRunFailed Status = "run-failed"
	// StatusNoRung: the pyramid exposes no BDD rung — nothing to rerun.
	StatusNoRung Status = "no-bdd-rung"
)

// Outcome is the structured result the web handler maps to a banner and an optional
// report-panel refresh. Lines are human-readable report lines (joined for display).
// RunKey is the artefacts-relative key of the newly captured run, "" when none was
// captured (blocked / env fault / no rung).
type Outcome struct {
	Status Status
	Rung   string
	Env    string
	RunKey string
	Lines  []string
}

// BoundBDDRung resolves the BDD rung a rerun would re-execute for the codebase repo:
// the highest (closest-to-target) rung declaring a cucumber_json report, plus its
// resolved environment (nil for the ambient context). ok is false — not an error —
// when there is neither an in-repo nor an overlay pyramid, or no BDD rung in it, so
// callers can cheaply gate a "rerun" affordance. A malformed .test-pyramid.yaml is a
// real error.
//
// It is overlay-aware (drv-041/drv-047) but READ-ONLY: it never cuts a worktree. The
// in-repo pyramid wins; when the codebase carries none it resolves the overlay in place
// (overlay.Resolve + pyramid.Load against the developer-owned overlay repo), exactly the
// no-checkout pattern the board badge uses (internal/web/pyramid.go). repo is the
// codebase's recorded base path (a.Repo), the resolution key for the overlay settings —
// not a directory the rung runs in.
func BoundBDDRung(repo string) (pyramid.Level, *pyramid.Environment, bool, error) {
	p, err := pyramid.Load(repo)
	if err != nil {
		return pyramid.Level{}, nil, false, err
	}
	if p == nil {
		// No in-repo pyramid: resolve an overlay in place. A missing/unresolvable overlay
		// is ok=false (nothing to rerun), never an error — the gate just hides the button.
		p, err = overlayPyramid(repo)
		if err != nil || p == nil {
			return pyramid.Level{}, nil, false, err
		}
	}
	lv, ok := lastBDDRung(p)
	if !ok {
		return pyramid.Level{}, nil, false, nil
	}
	env, _ := p.EnvironmentFor(lv)
	return lv, env, true, nil
}

// overlayPyramid resolves the codebase repo's test-pyramid overlay and loads its pyramid
// from the overlay repo in place (no worktree cut) — the read-only resolution the rerun
// gate needs for an overlay-only codebase. It mirrors internal/web/pyramid.go's badge
// path: a missing/unresolvable overlay is a clean nil (nothing to rerun); only a genuine
// overlay I/O or malformed-pyramid fault is an error.
func overlayPyramid(repo string) (*pyramid.Pyramid, error) {
	if strings.TrimSpace(repo) == "" {
		return nil, nil
	}
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	loc, ok, err := overlay.Resolve(repo, cfg.RepoSettings(repo).Overlay, cfg.OverlayDir)
	if err != nil || !ok {
		return nil, err
	}
	return pyramid.Load(loc.Repo)
}

// lastBDDRung returns the highest-indexed rung declaring a cucumber_json — the BDD
// rung nearest the pyramid's target, which is the one a rerun regenerates.
func lastBDDRung(p *pyramid.Pyramid) (pyramid.Level, bool) {
	for i := len(p.Levels) - 1; i >= 0; i-- {
		if strings.TrimSpace(p.Levels[i].CucumberJSON) != "" {
			return p.Levels[i], true
		}
	}
	return pyramid.Level{}, false
}

// Rerun re-executes the attempt's bound BDD rung over its environment and captures
// a fresh side-by-side artefact set. It never returns an error for an ordinary
// red/failed outcome — those are carried in the Outcome so the board can explain
// them; a non-nil error is reserved for a genuinely malformed pyramid, an attempt
// with nothing to review, a worktree-cut failure, or a capture I/O fault the caller
// should surface as a 500.
//
// repo is the attempt's recorded base repo (a.Repo), NOT the directory the rung runs
// in. A rerun must regenerate the report for the code under review, so — exactly as a
// review environment does (internal/reviewenv) — it resolves the reviewed commit (the
// live checkout HEAD, else the attempt branch tip) and cuts a dedicated *detached*
// worktree at that commit to run in. Running in the shared main checkout (drv-030) ran
// the rung against whatever branch that tree happened to sit on, silently producing a
// report for a different codebase; a detached checkout at the reviewed commit also
// avoids mutating the agent's live coding worktree.
func Rerun(ctx context.Context, root store.Root, repo, ticket, att string) (Outcome, error) {
	wm, err := worktree.NewManager(repo)
	if err != nil {
		return Outcome{}, fmt.Errorf("bddexec: worktree manager for %s: %w", repo, err)
	}
	key := worktree.Key{Ticket: ticket, Attempt: att}
	commit, err := reviewedCommit(ctx, wm, key)
	if err != nil {
		return Outcome{}, err
	}

	// Cut a dedicated detached worktree at the reviewed commit — a clean slate against
	// any checkout a prior rerun left, then torn down unconditionally when we return so
	// a rerun leaves nothing behind.
	wd := rerunWorktreePath(repo, ticket, att)
	removeWorktree(ctx, repo, wd)
	if err := addWorktree(ctx, repo, wd, commit); err != nil {
		return Outcome{}, fmt.Errorf("bddexec: cut rerun worktree at %s: %w", short(commit), err)
	}
	defer removeWorktree(ctx, repo, wd)

	// Resolve the rung through the shared overlay resolver (drv-047), exactly as
	// `draiver test` does — so an overlay-only codebase reruns its overlay's BDD rung,
	// and extraEnv (DRAIVER_CODEBASE_DIR / DRAIVER_OVERLAY_DIR) threads into every shell
	// step. cwd stays the detached codebase worktree wd; overlay assets are reached via
	// $DRAIVER_OVERLAY_DIR. An in-repo pyramid yields a nil overlay and nil extraEnv, so
	// the in-repo rerun is byte-for-byte unchanged.
	p, extraEnv, ok, err := resolveRerunPyramid(ctx, wd, repo, key)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{
			Status: StatusNoRung,
			Lines:  []string{"No BDD rung in `.test-pyramid.yaml` — there is nothing to rerun."},
		}, nil
	}
	lv, _ := lastBDDRung(p)
	env, _ := p.EnvironmentFor(lv)
	out := Outcome{Rung: lv.Name, Env: lv.Environment}

	// Bring the environment up (drv-012). A non-zero `up` is infrastructure, not code.
	if env != nil && strings.TrimSpace(env.Up) != "" {
		if err := runShell(ctx, wd, env.Up, extraEnv); err != nil {
			out.Status = StatusEnvFault
			out.Lines = []string{
				fmt.Sprintf("Environment %q failed to come up: %v.", env.Name, err),
				"This is an infrastructure fault, not a code failure — the rerun was not attempted.",
			}
			return out, nil
		}
		// Tear down unconditionally, best-effort, once we have brought it up.
		if strings.TrimSpace(env.Down) != "" {
			defer runShell(ctx, wd, env.Down, extraEnv)
		}
	}

	// Healthcheck precondition (drv-012): a red probe blocks the rerun — pass the
	// ball back rather than capture a bogus report.
	if env != nil {
		for _, hc := range env.Healthchecks {
			if err := runShell(ctx, wd, hc.Script, extraEnv); err != nil {
				out.Status = StatusBlocked
				out.Lines = []string{
					fmt.Sprintf("Healthcheck %q for environment %q is red — rerun blocked.", hc.Name, env.Name),
					"The bound environment is not ready; this is a dependency not up, not a code failure. Pass the ball back rather than capture a bogus report.",
				}
				return out, nil
			}
		}
	}

	// Execute the rung, then capture its output as a fresh run regardless of the
	// run's own verdict — a failing run's report is evidence worth keeping.
	runErr := runShell(ctx, wd, lv.Run, extraEnv)

	// Stamp with the reviewed commit we cut the worktree at — never "nocommit". The
	// worktree is detached at exactly this commit, so the capture names the real code
	// under review (drv-030).
	sha := commit
	runKey, refs, capErr := capture(root, wd, ticket, att, lv, sha)
	out.RunKey = runKey

	switch {
	case capErr != nil:
		// The run produced no capturable report (e.g. it never wrote cucumber_json).
		out.Status = StatusRunFailed
		out.Lines = []string{"The BDD rung ran but no report could be captured: " + capErr.Error() + "."}
		if runErr != nil {
			out.Lines = append(out.Lines, "The run also exited non-zero: "+runErr.Error()+".")
		}
		out.RunKey = ""
	case runErr != nil:
		out.Status = StatusRunFailed
		out.Lines = []string{
			fmt.Sprintf("Reran `%s` on %s @ %s — the run failed (%v).", lv.Name, envLabel(env), short(sha), runErr),
			fmt.Sprintf("Captured the failing run (%d artefact(s)) under `%s`; the refreshed report shows the failures.", len(refs), runKey),
		}
	default:
		out.Status = StatusOK
		out.Lines = []string{
			fmt.Sprintf("Reran `%s` on %s @ %s — green.", lv.Name, envLabel(env), short(sha)),
			fmt.Sprintf("Captured %d artefact(s) under `%s`; the embedded report has been refreshed.", len(refs), runKey),
		}
	}
	return out, nil
}

// capture stores the rung's cucumber_json (and any declared extra artefacts) as a
// fresh, newly-stamped side-by-side run under the attempt's artefact store (drv-017),
// then prunes to the configured retention — mirroring `draiver test`'s capture.
func capture(root store.Root, wd, ticket, att string, lv pyramid.Level, sha string) (string, []string, error) {
	var sources []bddartefact.Source
	if c := strings.TrimSpace(lv.CucumberJSON); c != "" {
		sources = append(sources, bddartefact.Source{Path: c})
	}
	for _, a := range lv.Artifacts {
		sources = append(sources, bddartefact.Source{Path: a})
	}
	rc := bddartefact.RunContext{
		Rung:        lv.Name,
		Environment: lv.Environment,
		Commit:      sha,
		Runstamp:    time.Now().UTC().Format("20060102T150405Z"),
	}
	artDir := root.ArtefactsDir(ticket, att)
	runKey, refs, err := bddartefact.Capture(artDir, wd, rc, sources)
	if err != nil {
		return "", nil, err
	}
	// Retention: keep the newest N per group, mirroring cmd/test.go. Best-effort —
	// a prune fault must not fail a successful capture.
	if cfg, err := config.Load(""); err == nil {
		bddartefact.Prune(artDir, rc.Rung, rc.Env(), cfg.BDDArtefactKeep)
	}
	return runKey, refs, nil
}

// reviewedCommit resolves the commit under review for the attempt: the live checkout's
// HEAD when a coding worktree still exists, else the attempt branch tip — the same
// two-tier fallback the review env (reviewenv.reviewedCommit) and the pyramid badge
// (web.gatherPyramidState) use, so a rerun regenerates the report for the exact code a
// reviewer is judging, whether or not the coding worktree has been reclaimed. An
// attempt with neither a live checkout nor a branch has nothing to rerun against — a
// real error the caller surfaces, not a silent run against the wrong tree.
func reviewedCommit(ctx context.Context, wm *worktree.Manager, key worktree.Key) (string, error) {
	if wt, ok, err := wm.Locate(ctx, key); err != nil {
		return "", fmt.Errorf("bddexec: locate worktree %s/%s: %w", key.Ticket, key.Attempt, err)
	} else if ok {
		sha, err := worktree.HeadSHA(ctx, wt.Path)
		if err != nil {
			return "", fmt.Errorf("bddexec: head of %s: %w", wt.Path, err)
		}
		return strings.TrimSpace(sha), nil
	}
	sha, ok, err := wm.BranchSHA(ctx, key)
	if err != nil {
		return "", fmt.Errorf("bddexec: branch sha %s/%s: %w", key.Ticket, key.Attempt, err)
	}
	if !ok {
		return "", fmt.Errorf("bddexec: attempt %s/%s has neither a live checkout nor a branch to rerun", key.Ticket, key.Attempt)
	}
	return strings.TrimSpace(sha), nil
}

// rerunWorktreePath is where a rerun's detached checkout lives: a dedicated base,
// distinct from both the coding worktree (worktree.Manager's managed base) and the
// review-env base, keyed by a hash of the repo so reruns for same-named tickets across
// repos never collide. It falls back to the OS temp dir when the user cache dir is
// unavailable, mirroring reviewenv.worktreePath.
func rerunWorktreePath(repo, ticket, att string) string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	sum := sha256.Sum256([]byte(repo))
	repoHash := hex.EncodeToString(sum[:])[:12]
	return filepath.Join(base, "draiver", "bdd-reruns", repoHash, ticket, att)
}

// addWorktree cuts a DETACHED worktree at commit — detached precisely so it does not
// claim the attempt's coding branch (draiver/<ticket>/<attempt>), which the coding
// worktree may already hold. Mirrors reviewenv.addWorktree.
func addWorktree(ctx context.Context, repo, path, commit string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return gitRun(ctx, repo, "worktree", "add", "--detach", path, commit)
}

// removeWorktree force-removes a rerun checkout, pruning the admin entry if the
// directory already vanished. Best-effort: a missing/never-created worktree is not an
// error; an empty path is a no-op. Mirrors reviewenv.removeWorktree.
func removeWorktree(ctx context.Context, repo, path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := gitRun(ctx, repo, "worktree", "remove", "--force", path); err != nil {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			gitRun(ctx, repo, "worktree", "prune")
		}
	}
}

// gitRun runs `git -C repo args...`, folding stderr into the error for legibility.
func gitRun(ctx context.Context, repo string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// runShell runs a `sh -c` command string in wd and returns its run error (nil on
// exit 0) — the same exec seam `draiver test` uses for up/down/healthcheck/run.
//
// extraEnv is the overlay execution contract (drv-041/drv-047): when an overlay is
// active it carries DRAIVER_CODEBASE_DIR / DRAIVER_OVERLAY_DIR, layered over the
// inherited environment. For an in-repo pyramid it is empty and rc.Env is left unset,
// so the command inherits os.Environ() exactly as before — byte-for-byte unchanged.
func runShell(ctx context.Context, wd, script string, extraEnv []string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = wd
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	return cmd.Run()
}

// resolveRerunPyramid resolves the pyramid a rerun re-executes for the attempt, folding
// drv-041's overlay model exactly as cmd/test.go's resolvePyramid does: the in-repo
// pyramid at the detached codebase worktree wd wins; otherwise the attempt's configured
// overlay is cut and loaded, returning its ScriptEnv(wd) as extraEnv. repo is the
// codebase's recorded base path, the resolution key for the overlay settings. ok is
// false when there is neither an in-repo nor an overlay pyramid with a BDD rung.
func resolveRerunPyramid(ctx context.Context, wd, repo string, key worktree.Key) (*pyramid.Pyramid, []string, bool, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, nil, false, fmt.Errorf("bddexec: load config: %w", err)
	}
	res := cfg.RepoSettings(repo)
	p, _, extraEnv, err := overlay.ResolvePyramid(ctx, wd, repo, res.Overlay, cfg.OverlayDir, key)
	if err != nil {
		return nil, nil, false, err
	}
	if p == nil {
		return nil, nil, false, nil
	}
	if _, ok := lastBDDRung(p); !ok {
		return nil, nil, false, nil
	}
	return p, extraEnv, true, nil
}

func envLabel(env *pyramid.Environment) string {
	if env == nil {
		return "the ambient environment"
	}
	return "environment `" + env.Name + "`"
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
