// Package promote is the in-process executor that VERIFIES an environment promotion
// (drv-043) — it never performs one. It runs a repo-declared `promotions:` entry's
// env-wrapped `qa` check (drv-012: up → healthcheck → qa → down) and reports whether
// the promotion succeeded, exactly as a repo declares how it wants to be tested
// rather than draiver hardcoding a toolchain. exit 0 == promoted-and-healthy.
//
// It is the promotion twin of internal/bddexec: both `draiver promote` (cmd/promote.go,
// running in the attempt's own worktree) and the board's promote button
// (internal/web/promote.go, running in a detached worktree at the reviewed commit) go
// through RunQA, so the up→healthcheck→qa→down orchestration and its fault taxonomy
// live once. The web entrypoint Run mirrors bddexec.Rerun's worktree handling. The
// reuse with cmd/test.go is at the building-block level (pyramid, the env-wrapped
// shape); the control flow genuinely differs (a promotion runs one env-wrapped check,
// binds a commit, and never gates), so this re-expresses it rather than sharing cmd's.
package promote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/overlay"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// Status is the verdict of a promotion run, mirroring `draiver test`'s exit-code
// taxonomy (cmd/root.go): a failed env `up` is infrastructure, a red healthcheck is a
// dependency-not-ready block, a non-zero `qa` is a code/promotion fault, and
// "no promotion" means the named environment declares none.
type Status string

const (
	// StatusOK: the env-wrapped qa check ran green — the change is promoted-and-healthy.
	StatusOK Status = "ok"
	// StatusBlocked: a healthcheck was red — the environment is not ready, so qa was not
	// attempted (pass the ball). Maps to ExitBlocked.
	StatusBlocked Status = "blocked"
	// StatusEnvFault: the environment's `up` failed — an infrastructure fault, not a
	// promotion failure. Maps to ExitEnvUp.
	StatusEnvFault Status = "env-fault"
	// StatusQAFailed: qa executed but exited non-zero — the change is not promoted. Maps
	// to exit 1.
	StatusQAFailed Status = "qa-failed"
	// StatusNoPromotion: the environment declares no promotion (or no pyramid) — nothing
	// to run.
	StatusNoPromotion Status = "no-promotion"
)

// Pass reports whether the run verified a successful promotion — the single bit the
// `--log` gate and the board chip key on.
func (s Status) Pass() bool { return s == StatusOK }

// Outcome is the structured result both callers map to an exit code (CLI) or a banner
// + chip (web). Lines are human-readable report lines (joined for display). Commit is
// the exact commit the qa verified, set by Run (the web path); the CLI stamps it from
// the worktree HEAD after a green+clean run.
type Outcome struct {
	Status      Status
	Environment string
	Base        string
	Commit      string
	Lines       []string
	// Overlay is the test-pyramid overlay (drv-041) that supplied this promotion, set by
	// Run when the pyramid was resolved from an overlay rather than an in-repo file; nil
	// for an in-repo promotion. The web caller records its repo + commit as provenance,
	// mirroring the CLI's recordOverlayProvenance.
	Overlay *overlay.Active
}

// RunQA runs one promotion's env-wrapped qa check in dir and returns its verdict. It
// is the shared executor: dir is the attempt worktree for the CLI, or a detached
// worktree at the reviewed commit for the board button. It streams every step's
// output to stdout/stderr and never returns — an ordinary red/blocked/fault outcome
// is carried in the Status, not raised as an error.
//
// extraEnv is layered over os.Environ() for every step (the overlay execution
// contract when one is active, empty otherwise). On top of it this always injects the
// DRAIVER_PROMOTE_* contract — the environment name, the tracked base branch, and the
// reachable URL — so a repo's qa/healthcheck scripts can read them without draiver
// hardcoding anything. Order mirrors runRung: down is deferred before up so a partial
// up still tears down; up → healthchecks → qa; down runs unconditionally, best-effort.
func RunQA(ctx context.Context, stdout, stderr io.Writer, dir string, env *pyramid.Environment, promo pyramid.Promotion, extraEnv []string) Outcome {
	out := Outcome{Status: StatusOK, Environment: promo.Environment, Base: promo.Base}

	runEnv := append([]string(nil), extraEnv...)
	runEnv = append(runEnv,
		"DRAIVER_PROMOTE_ENV="+promo.Environment,
		"DRAIVER_PROMOTE_BASE="+promo.Base,
	)
	if env != nil {
		runEnv = append(runEnv, "DRAIVER_PROMOTE_URL="+env.URL)
	}

	if env != nil && strings.TrimSpace(env.Down) != "" {
		// Guaranteed teardown, scheduled before `up` so a failed/partial `up` still tears
		// down. Best-effort: a `down` failure never changes the verdict.
		defer func() {
			fmt.Fprintf(stdout, "== promote %s: down: %s\n", env.Name, env.Down)
			if err := runShell(ctx, stdout, stderr, dir, env.Down, runEnv); err != nil {
				fmt.Fprintf(stderr, "warning: environment %q down failed (ignored): %v\n", env.Name, err)
			}
		}()
	}

	if env != nil && strings.TrimSpace(env.Up) != "" {
		fmt.Fprintf(stdout, "== promote %s: up: %s\n", env.Name, env.Up)
		if err := runShell(ctx, stdout, stderr, dir, env.Up, runEnv); err != nil {
			out.Status = StatusEnvFault
			out.Lines = []string{
				fmt.Sprintf("Environment %q failed to come up: %v.", env.Name, err),
				"This is an infrastructure fault, not a promotion failure — qa was not attempted; escalate.",
			}
			return out
		}
	}

	if env != nil {
		for _, hc := range env.Healthchecks {
			fmt.Fprintf(stdout, "== promote %s: healthcheck %s: %s\n", env.Name, hc.Name, hc.Script)
			if err := runShell(ctx, stdout, stderr, dir, hc.Script, runEnv); err != nil {
				out.Status = StatusBlocked
				out.Lines = []string{
					fmt.Sprintf("Healthcheck %q for environment %q is red — promotion blocked.", hc.Name, env.Name),
					"The environment is not ready; this is a dependency not up, not a promotion failure. Pass the ball back.",
				}
				return out
			}
		}
	}

	fmt.Fprintf(stdout, "== promote %s: qa: %s\n", promo.Environment, promo.QA)
	if err := runShell(ctx, stdout, stderr, dir, promo.QA, runEnv); err != nil {
		out.Status = StatusQAFailed
		out.Lines = []string{
			fmt.Sprintf("Promotion qa for environment %q failed (%v) — not promoted.", promo.Environment, err),
		}
		return out
	}
	out.Lines = []string{
		fmt.Sprintf("Promotion qa for environment %q is green — promoted-and-healthy.", promo.Environment),
	}
	return out
}

// Run is the board button's entrypoint: it verifies the promotion for envName against
// the exact code under review and returns the Outcome stamped with the verified
// commit. Like bddexec.Rerun it resolves the reviewed commit (live checkout HEAD, else
// the attempt branch tip) and cuts a dedicated *detached* worktree at it, so the qa
// runs against the reviewed code rather than whatever the coding worktree sits on —
// and without mutating that worktree. The worktree is torn down unconditionally.
//
// A non-nil error is reserved for a genuinely malformed pyramid, an attempt with
// nothing to review, a worktree-cut failure, or a configured-but-unusable overlay the
// caller should surface as a 500; an ordinary blocked/failed/no-promotion outcome is
// carried in the returned Outcome.
//
// The pyramid is resolved through overlay.ResolvePyramid — the same shared resolution
// the CLI `draiver promote` performs — so a promotion declared only in a test-pyramid
// overlay (drv-041) runs here exactly as it does off the CLI: the overlay is cut at the
// overlay repo's current HEAD, its script-env (DRAIVER_CODEBASE_DIR/DRAIVER_OVERLAY_DIR)
// is threaded into RunQA, and the sourcing overlay is surfaced on Outcome.Overlay for the
// caller to record as provenance. Resolution stays commit-bound on the CODEBASE side (the
// reviewed-commit detached worktree below, unchanged). Only when there is neither an
// in-repo pyramid NOR a resolvable overlay does this yield StatusNoPromotion — the
// terminal case is preserved, it just no longer fires on every overlay case.
func Run(ctx context.Context, root store.Root, repo, ticket, att, envName string, stdout, stderr io.Writer) (Outcome, error) {
	wm, err := worktree.NewManager(repo)
	if err != nil {
		return Outcome{}, fmt.Errorf("promote: worktree manager for %s: %w", repo, err)
	}
	key := worktree.Key{Ticket: ticket, Attempt: att}
	commit, err := reviewedCommit(ctx, wm, key)
	if err != nil {
		return Outcome{}, err
	}

	wd := worktreePath(repo, ticket, att)
	removeWorktree(ctx, repo, wd)
	if err := addWorktree(ctx, repo, wd, commit); err != nil {
		return Outcome{}, fmt.Errorf("promote: cut worktree at %s: %w", short(commit), err)
	}
	defer removeWorktree(ctx, repo, wd)

	// Resolve the pyramid the way the CLI does: in-repo wins, else the configured overlay.
	// `repo` is the codebase path (the overlay resolution key); the per-repo explicit
	// overlay + global overlay_dir come from the operator config.
	cfg, err := config.Load("")
	if err != nil {
		return Outcome{}, fmt.Errorf("promote: load config: %w", err)
	}
	res := cfg.RepoSettings(repo)
	p, active, extraEnv, err := overlay.ResolvePyramid(ctx, wd, repo, res.Overlay, cfg.OverlayDir, key)
	if err != nil {
		return Outcome{}, err
	}
	if p == nil {
		return Outcome{
			Status:      StatusNoPromotion,
			Environment: envName,
			Commit:      commit,
			Lines:       []string{"No in-repo `.test-pyramid.yaml` at the reviewed commit and no resolvable overlay — nothing to promote."},
		}, nil
	}
	promo, ok := p.Promotion(envName)
	if !ok {
		return Outcome{
			Status:      StatusNoPromotion,
			Environment: envName,
			Commit:      commit,
			Lines:       []string{fmt.Sprintf("No promotion declares environment %q in `%s`.", envName, pyramid.FileName)},
		}, nil
	}
	env, _ := p.EnvironmentByName(promo.Environment)

	out := RunQA(ctx, stdout, stderr, wd, env, promo, extraEnv)
	out.Commit = commit
	out.Overlay = active
	return out, nil
}

// reviewedCommit resolves the commit under review: the live checkout's HEAD when a
// coding worktree still exists, else the attempt branch tip — the same two-tier
// fallback bddexec/reviewenv/the pyramid badge use, so a promotion verifies the exact
// code a reviewer is judging. An attempt with neither is a real error the caller
// surfaces rather than running against the wrong tree.
func reviewedCommit(ctx context.Context, wm *worktree.Manager, key worktree.Key) (string, error) {
	if wt, ok, err := wm.Locate(ctx, key); err != nil {
		return "", fmt.Errorf("promote: locate worktree %s/%s: %w", key.Ticket, key.Attempt, err)
	} else if ok {
		sha, err := worktree.HeadSHA(ctx, wt.Path)
		if err != nil {
			return "", fmt.Errorf("promote: head of %s: %w", wt.Path, err)
		}
		return strings.TrimSpace(sha), nil
	}
	sha, ok, err := wm.BranchSHA(ctx, key)
	if err != nil {
		return "", fmt.Errorf("promote: branch sha %s/%s: %w", key.Ticket, key.Attempt, err)
	}
	if !ok {
		return "", fmt.Errorf("promote: attempt %s/%s has neither a live checkout nor a branch to promote", key.Ticket, key.Attempt)
	}
	return strings.TrimSpace(sha), nil
}

// worktreePath is where the promote run's detached checkout lives: a dedicated base,
// distinct from the coding, review-env, and bdd-rerun bases, keyed by a hash of the
// repo so promotions for same-named tickets across repos never collide. Mirrors
// bddexec.rerunWorktreePath.
func worktreePath(repo, ticket, att string) string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	sum := sha256.Sum256([]byte(repo))
	repoHash := hex.EncodeToString(sum[:])[:12]
	return filepath.Join(base, "draiver", "promotions", repoHash, ticket, att)
}

// addWorktree cuts a DETACHED worktree at commit — detached so it does not claim the
// attempt's coding branch, which the coding worktree may already hold. Mirrors
// bddexec.addWorktree.
func addWorktree(ctx context.Context, repo, path, commit string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return gitRun(ctx, repo, "worktree", "add", "--detach", path, commit)
}

// removeWorktree force-removes a promote checkout, pruning the admin entry if the
// directory already vanished. Best-effort; an empty path is a no-op. Mirrors
// bddexec.removeWorktree.
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

// runShell runs a `sh -c` command string in dir, streaming stdio to the given writers
// and layering env over os.Environ() — the single exec seam every promotion step
// (up/healthcheck/qa/down) goes through, so output ordering and interpretation are
// identical across them, matching cmd/test.go's runShell.
func runShell(ctx context.Context, stdout, stderr io.Writer, dir, script string, env []string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.Run()
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
