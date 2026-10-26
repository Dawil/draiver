// Package overlay resolves a test-pyramid overlay (drv-041): a developer-owned git
// repo that supplies a codebase's .test-pyramid.yaml (and the features/, review-env
// scripts, and unit tests it drives) WITHOUT adding any of them to the codebase's own
// version control. It is the settings-only, zero-version-control-footprint path for
// the solo, local-first developer whose team has not adopted draiver: their personal
// test tooling lives beside the code, pointed at only through config.json.
//
// This package owns two things the dependency-free internal/pyramid deliberately does
// not: resolving *which* repo holds the pyramid (Resolve — pure, no side effects), and
// materialising that repo as a per-attempt worktree so overlay edits stay isolated and
// parallel attempts never collide (Cut — the same `git worktree add` isolation the
// codebase and review-env worktrees get). The execution contract — cwd stays the
// codebase worktree, overlay assets are reached via $DRAIVER_OVERLAY_DIR, a reliable
// $DRAIVER_CODEBASE_DIR anchors scripts that `cd` — is expressed by the env-var
// constants below and ScriptEnv.
package overlay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dawil/draiver/internal/worktree"
)

// Env-var names the pyramid runner exports when an overlay is active (drv-041). A
// local, in-repo pyramid references neither — it runs with cwd = the codebase worktree
// and no extra environment, byte-for-byte as before — so these appear ONLY on the
// overlay path, the signal an overlay-authored command uses to reach overlay assets.
const (
	// EnvCodebaseDir is exported unconditionally when an overlay is active → the
	// codebase worktree (the cwd). It is the reliable absolute anchor for an overlay
	// script that needs to `cd` back to the code under test.
	EnvCodebaseDir = "DRAIVER_CODEBASE_DIR"
	// EnvOverlayDir is exported only when an overlay is active → the overlay worktree.
	// Overlay-authored commands reach overlay assets through it explicitly, e.g.
	// `run: go run $DRAIVER_OVERLAY_DIR/features/acceptance`.
	EnvOverlayDir = "DRAIVER_OVERLAY_DIR"
)

// Location identifies a resolved overlay repo and how resolution found it — the latter
// purely for legible messages and provenance ("explicit per-repo setting" vs "the
// <overlay_dir>/<basename> folder lookup").
type Location struct {
	// Repo is the absolute path to the developer-owned overlay repo, operated in place
	// (draiver never clones or mirrors it).
	Repo string
	// Via is how Resolve found it: "explicit" (the per-codebase Overlay setting) or
	// "overlay_dir" (the <overlay_dir>/<basename> folder lookup).
	Via string
}

// Resolve picks the overlay repo for a codebase per drv-041's resolution model, with
// NO side effects: it never cuts a worktree, never reads a pyramid, and never writes
// anything. The caller must already have decided there is no in-repo pyramid
// (step 1, "in-repo wins", is the caller's cheap pyramid.Load check); Resolve covers
// the remaining steps against the settings it is handed:
//
//  2. explicitOverlay (the resolved per-codebase Overlay setting), when non-empty, wins
//     outright → Via "explicit". It is NOT existence-checked here: an explicit mispoint
//     must fail loudly when the worktree is cut, never silently fall through to the
//     folder lookup and mask the misconfiguration.
//  3. else, when overlayDir is set, the candidate <overlayDir>/<basename(codebasePath)>
//     is used IFF it exists and is a git repo → Via "overlay_dir". A missing or
//     non-git candidate cleanly falls through, so an operator can point overlay_dir at
//     a folder that covers only some of their repos.
//  4. else ok=false — no overlay (step 4, "none"): the caller keeps `draiver test` a
//     successful no-op.
//
// codebasePath is the codebase repo's path (the attempt's recorded `repo`); its
// basename is the folder-lookup key, so the explicit per-codebase setting is the escape
// hatch for a basename collision. err is returned only on a genuine I/O fault probing
// the candidate; a candidate that simply is not there is ok=false, not an error.
func Resolve(codebasePath, explicitOverlay, overlayDir string) (Location, bool, error) {
	if explicitOverlay != "" {
		return Location{Repo: explicitOverlay, Via: "explicit"}, true, nil
	}
	if overlayDir == "" {
		return Location{}, false, nil
	}
	candidate := filepath.Join(overlayDir, filepath.Base(codebasePath))
	isRepo, err := IsGitRepo(candidate)
	if err != nil {
		return Location{}, false, err
	}
	if !isRepo {
		return Location{}, false, nil
	}
	return Location{Repo: candidate, Via: "overlay_dir"}, true, nil
}

// IsGitRepo reports whether path is a git repository — it contains a `.git` entry (a
// directory for a normal clone, or a file for a linked worktree). A missing path is a
// clean false (not an error); only an unexpected stat failure is returned as err. It is
// deliberately a cheap filesystem probe rather than a `git` shell-out: the folder
// lookup must stay fast and daemon-free, and a developer-owned overlay always has a
// top-level `.git`.
func IsGitRepo(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	info, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("overlay: stat %s/.git: %w", path, err)
	}
	_ = info
	return true, nil
}

// Active is a materialised overlay for one attempt: the resolved Location plus the cut
// worktree and the exact commit it resolves to. It is what the pyramid runner loads the
// overlay pyramid from and what the attempt's provenance records.
type Active struct {
	Location
	// WorktreePath is the cut overlay worktree, on branch draiver/<ticket>/<attempt>.
	WorktreePath string
	// Commit is the overlay worktree's HEAD — the exact commit the run used, recorded
	// in provenance so a run or review env is explainable after the fact.
	Commit string
}

// ScriptEnv returns the environment additions the pyramid runner layers over
// os.Environ() when this overlay is active: DRAIVER_CODEBASE_DIR → the codebase
// worktree (the cwd), DRAIVER_OVERLAY_DIR → the overlay worktree. The order is
// deterministic for reproducible test assertions.
func (a Active) ScriptEnv(codebaseWorktree string) []string {
	return []string{
		EnvCodebaseDir + "=" + codebaseWorktree,
		EnvOverlayDir + "=" + a.WorktreePath,
	}
}

// Cut materialises an overlay repo as a per-attempt worktree for key, mirroring the
// codebase worktree exactly: it cuts (or, on resume, re-attaches to) the branch
// draiver/<ticket>/<attempt> from the overlay repo's default branch — the overlay repo
// is operated in place, so its HEAD is that default branch — under the managed cache
// base. That keeps overlay edits on the feature branch (never the overlay's main) and
// keeps parallel attempts isolated. It returns the worktree path and the HEAD commit
// the run uses.
//
// Cut is the side-effecting counterpart to the pure Resolve: a read-only consumer (the
// board badge, review surfacing) that only needs the pyramid structure reads the
// overlay repo in place instead of cutting a worktree.
func Cut(ctx context.Context, loc Location, key worktree.Key) (Active, error) {
	wm, err := worktree.NewManager(loc.Repo)
	if err != nil {
		return Active{}, fmt.Errorf("overlay: %s is not a usable overlay repo: %w", loc.Repo, err)
	}
	// Ref empty → cut from the overlay repo's HEAD (its default branch), exactly how the
	// codebase worktree is cut. A resume re-attaches to the existing branch.
	wt, err := wm.Create(ctx, worktree.Spec{Key: key})
	if err != nil {
		return Active{}, fmt.Errorf("overlay: cut worktree for %s/%s: %w", key.Ticket, key.Attempt, err)
	}
	commit, err := worktree.HeadSHA(ctx, wt.Path)
	if err != nil {
		return Active{}, fmt.Errorf("overlay: resolve overlay commit: %w", err)
	}
	return Active{Location: loc, WorktreePath: wt.Path, Commit: commit}, nil
}
