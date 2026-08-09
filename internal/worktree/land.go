package worktree

// land.go adds the terminal bookend of the branch lifecycle the Manager already
// owns (create → work → **land** → reclaim): merging an attempt's per-attempt
// branch back into its base, and the reverse back-merge that keeps the branch
// current. Both are plain, additive git merges — no rebase ever, because the
// branch is durable, resumable state (a resume re-attaches to it; it is the
// crash-recovery net) and rewriting its SHAs would break that contract
// (drvctl-021, building on drvctl-014).
//
//   - Merge lands feature → base, fast-forward only. ff is possible iff base is an
//     ancestor of the branch, which is exactly what `git merge --ff-only` enforces,
//     so the pre-check and the real merge agree by construction.
//   - Sync back-merges base → feature additively (one merge commit on top; existing
//     commits keep their SHAs), so a later resume just continues on the updated tip.
//     After a sync the branch becomes ff-landable.
//   - Mergeable predicts both without mutating anything (--is-ancestor + merge-tree).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Landing errors are sentinels so the command layer can map each failure to its
// disposition (escalate vs plain error) and a precise message. Every one leaves
// the repository exactly as it found it — no partial merge, no moved ref.
var (
	// ErrBranchNotFound is returned when the attempt's per-attempt branch is absent
	// (never created, or its branch was deleted).
	ErrBranchNotFound = errors.New("worktree: attempt branch does not exist")
	// ErrBaseNotFound is returned when the recorded base branch does not exist as a
	// local ref.
	ErrBaseNotFound = errors.New("worktree: base branch does not exist")
	// ErrDiverged is returned by Merge when the base is not an ancestor of the
	// branch, so a fast-forward land is impossible. The fix is a sync (base →
	// feature) first, which flips the ancestry and makes the branch ff-landable.
	ErrDiverged = errors.New("worktree: base is not an ancestor of the branch — a fast-forward land is impossible; sync first")
	// ErrBaseDirty is returned by Merge when the base branch's own checkout holds
	// uncommitted changes a fast-forward would disturb.
	ErrBaseDirty = errors.New("worktree: the base branch's checkout holds uncommitted changes")
	// ErrCheckoutDirty is returned by Sync when the attempt's own checkout holds
	// uncommitted changes: sync merges into that checkout, so it must be clean for
	// the merge to be cleanly abortable.
	ErrCheckoutDirty = errors.New("worktree: the attempt's checkout holds uncommitted changes")
	// ErrConflict is returned by Sync when merging the base into the branch
	// conflicts. The in-progress merge is aborted before returning, so the checkout
	// is restored to its pre-sync state.
	ErrConflict = errors.New("worktree: merging the base into the branch conflicts")
	// ErrNotContainedUpstream is returned by MergeRemote when the attempt's branch
	// tip is not contained in the fetched remote base — the external PR was not
	// merged (or not into this base), so there is nothing to record done. It is the
	// load-bearing gate for the remote land; the caller records nothing on it.
	ErrNotContainedUpstream = errors.New("worktree: the attempt branch is not contained in the fetched remote base — the external merge did not land it")
)

// Mergeability is the side-effect-free prediction of landing k's branch onto
// base — the data behind `ctl merge --dry-run` and a board "mergeable" signal.
type Mergeability struct {
	Base   string // base branch name
	Branch string // the attempt's per-attempt branch name
	// FastForward reports that base is an ancestor of the branch, so the branch is
	// ff-only landable right now (Merge would succeed).
	FastForward bool
	// Conflicts lists the paths that a merge of the two would conflict on, computed
	// with merge-tree without touching any working tree. Empty means the two
	// integrate cleanly — either already ff-landable, or a sync would apply without
	// conflict. Best-effort file names parsed from merge-tree's conflict report.
	Conflicts []string
}

// Clean reports whether the branch can be landed with no manual conflict
// resolution — either a fast-forward now, or a clean sync-then-ff.
func (mg Mergeability) Clean() bool { return len(mg.Conflicts) == 0 }

// Mergeable predicts whether k's branch can land onto base without mutating
// anything. It uses `git merge-base --is-ancestor` for the fast-forward test and
// `git merge-tree --write-tree` (git 2.38+) for the conflict test; neither
// touches HEAD, the index, or any working tree, so it is a true dry-run for both
// the land and the reverse sync.
func (m *Manager) Mergeable(ctx context.Context, k Key, base string) (Mergeability, error) {
	branch := k.branch()
	if err := m.requireBranches(ctx, base, branch); err != nil {
		return Mergeability{}, err
	}
	ff, err := m.isAncestor(ctx, base, branch)
	if err != nil {
		return Mergeability{}, err
	}
	conflicts, err := m.mergeTreeConflicts(ctx, base, branch)
	if err != nil {
		return Mergeability{}, err
	}
	return Mergeability{Base: base, Branch: branch, FastForward: ff, Conflicts: conflicts}, nil
}

// Landed reports a successful ff-only land of k's branch onto base.
type Landed struct {
	Base   string
	Branch string
	Tip    string // the commit base now points at (the branch tip)
	// AlreadyUpToDate is set when base already contained the branch tip, so the
	// land moved nothing (an idempotent re-run).
	AlreadyUpToDate bool
}

// Merge lands k's branch onto base, fast-forward only, without ever checking out
// a branch or forcing anything. It never rebases and never creates a merge
// commit: a non-fast-forward is refused (ErrDiverged) rather than reconciled.
//
// The land goes wherever base lives. If base is checked out in a worktree (the
// common case — base is the branch the bound repo sits on), the land runs there
// as `git merge --ff-only <branch>`, which also advances that working tree
// forward; that checkout must be clean first (ErrBaseDirty) so the fast-forward
// disturbs nothing. If base is checked out nowhere, the land is a compare-and-swap
// `update-ref` fast-forward of the ref alone. Either way the pre-check
// (--is-ancestor) has already proved the move is a pure fast-forward, so the two
// paths agree by construction.
func (m *Manager) Merge(ctx context.Context, k Key, base string) (Landed, error) {
	branch := k.branch()
	if err := m.requireBranches(ctx, base, branch); err != nil {
		return Landed{}, err
	}
	ff, err := m.isAncestor(ctx, base, branch)
	if err != nil {
		return Landed{}, err
	}
	if !ff {
		return Landed{}, ErrDiverged
	}
	branchTip, err := m.revParse(ctx, branch)
	if err != nil {
		return Landed{}, err
	}
	baseTip, err := m.revParse(ctx, base)
	if err != nil {
		return Landed{}, err
	}
	landed := Landed{Base: base, Branch: branch, Tip: branchTip, AlreadyUpToDate: branchTip == baseTip}

	if _, err := m.fastForward(ctx, base, branch, branchTip, baseTip); err != nil {
		return Landed{}, err
	}
	return landed, nil
}

// fastForward advances the local branch base to the commit sourceTip names,
// fast-forward only, using sourceRef as the merge argument when base is checked
// out. The caller must have already proven base is an ancestor of sourceTip (a
// pure fast-forward). If base is checked out in a worktree the move runs there as
// `git merge --ff-only <sourceRef>`, which also advances that working tree; that
// checkout must be clean first (ErrBaseDirty) so the fast-forward disturbs nothing.
// If base is checked out nowhere the ref is moved by a compare-and-swap update-ref,
// so a concurrent change to base loses the race rather than being silently
// overwritten. Returns moved=false when base already points at sourceTip.
func (m *Manager) fastForward(ctx context.Context, base, sourceRef, sourceTip, baseTip string) (bool, error) {
	basePath, checkedOut, err := m.worktreePathForBranch(ctx, base)
	if err != nil {
		return false, err
	}
	if checkedOut {
		// Landing into a live checkout advances its working tree; refuse if that
		// tree is dirty so the fast-forward never clobbers uncommitted work.
		dirty, err := m.dirtyAt(ctx, basePath)
		if err != nil {
			return false, err
		}
		if dirty {
			return false, ErrBaseDirty
		}
		if baseTip == sourceTip {
			return false, nil
		}
		if _, err := m.gitIn(ctx, basePath, "merge", "--ff-only", sourceRef); err != nil {
			return false, fmt.Errorf("worktree: fast-forward %s into %s: %w", sourceRef, base, err)
		}
		return true, nil
	}
	if baseTip == sourceTip {
		return false, nil
	}
	if _, err := m.git(ctx, "update-ref", "refs/heads/"+base, sourceTip, baseTip); err != nil {
		return false, fmt.Errorf("worktree: fast-forward ref %s to %s: %w", base, sourceRef, err)
	}
	return true, nil
}

// Synced reports a successful additive back-merge of base into k's branch.
type Synced struct {
	Base   string
	Branch string
	Tip    string // the branch tip after the merge (a new merge commit, unless already up to date)
	// AlreadyUpToDate is set when base was already contained in the branch, so no
	// merge commit was created.
	AlreadyUpToDate bool
}

// Sync back-merges base into k's branch additively: `git merge base` on the
// branch. Existing commits keep their SHAs and at most one merge commit lands on
// top, so a resume re-attaches to the same durable branch and just continues on
// the updated tip. After a sync the branch is ff-landable (base becomes an
// ancestor).
//
// Sync merges into the attempt's checkout, so it materializes one first if the
// daemon already reclaimed it (Create re-attaches to the surviving branch), and
// requires it clean (ErrCheckoutDirty) so a conflicting merge can be cleanly
// aborted. On conflict the in-progress merge is aborted and ErrConflict returned,
// leaving the checkout exactly as it was.
func (m *Manager) Sync(ctx context.Context, k Key, base string) (Synced, error) {
	if err := k.valid(); err != nil {
		return Synced{}, err
	}
	branch := k.branch()
	if exists, err := m.branchExists(ctx, base); err != nil {
		return Synced{}, err
	} else if !exists {
		return Synced{}, ErrBaseNotFound
	}
	if exists, err := m.branchExists(ctx, branch); err != nil {
		return Synced{}, err
	} else if !exists {
		return Synced{}, ErrBranchNotFound
	}
	// Materialize the checkout if it was reclaimed; a live one is returned as-is.
	if _, err := m.Create(ctx, Spec{Key: k}); err != nil {
		return Synced{}, err
	}
	path := m.pathFor(k)
	dirty, err := m.dirtyAt(ctx, path)
	if err != nil {
		return Synced{}, err
	}
	if dirty {
		return Synced{}, ErrCheckoutDirty
	}
	before, err := m.revParse(ctx, branch)
	if err != nil {
		return Synced{}, err
	}
	if _, err := m.gitIn(ctx, path, "merge", "--no-edit", base); err != nil {
		// Restore the pre-sync state: abort the half-done merge (a no-op, tolerated,
		// if the failure was before any merge state was written).
		_, _ = m.gitIn(ctx, path, "merge", "--abort")
		return Synced{}, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	after, err := m.revParse(ctx, branch)
	if err != nil {
		return Synced{}, err
	}
	// The merge made no new commit when the tip is unchanged — base was already
	// contained in the branch, so there was nothing to pull in.
	return Synced{Base: base, Branch: branch, Tip: after, AlreadyUpToDate: after == before}, nil
}

// requireBranches verifies both refs exist, returning the precise sentinel so the
// caller can distinguish a missing base from a missing branch.
func (m *Manager) requireBranches(ctx context.Context, base, branch string) error {
	if exists, err := m.branchExists(ctx, branch); err != nil {
		return err
	} else if !exists {
		return ErrBranchNotFound
	}
	if exists, err := m.branchExists(ctx, base); err != nil {
		return err
	} else if !exists {
		return ErrBaseNotFound
	}
	return nil
}

// isAncestor reports whether ancestor is an ancestor of descendant (so a
// fast-forward from ancestor to descendant is possible). `git merge-base
// --is-ancestor` exits 0 for yes, 1 for no, and non-1 for a real error.
func (m *Manager) isAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	err := m.gitStatus(ctx, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("worktree: is-ancestor %s..%s: %w", ancestor, descendant, err)
}

// mergeTreeConflicts computes the paths a merge of base and branch would conflict
// on, without writing to any working tree. `git merge-tree --write-tree` exits 0
// for a clean merge (stdout is just the merged tree oid) and 1 for conflicts
// (stdout is the tree oid followed by `<mode> <object> <stage>\t<path>` lines);
// any other exit is a real error.
func (m *Manager) mergeTreeConflicts(ctx context.Context, base, branch string) ([]string, error) {
	// A clean merge exits 0 with just the tree oid on stdout; conflicts exit 1 with
	// the conflicted-path lines. Both are the "answer" and are parsed the same way,
	// so exit 1 is not treated as an error here.
	out, err := m.gitStdoutAllowExit1(ctx, "merge-tree", "--write-tree", "--no-messages", base, branch)
	if err != nil {
		return nil, err
	}
	return parseMergeTreeConflicts(out), nil
}

// parseMergeTreeConflicts pulls the unique conflicted paths out of merge-tree's
// conflict report. The first line is the merged tree oid; each following
// non-blank line is `<mode> <object> <stage>\t<path>`, one per (path, stage), so
// paths repeat across stages and are de-duplicated here.
func parseMergeTreeConflicts(out string) []string {
	var paths []string
	seen := map[string]bool{}
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if i == 0 || line == "" {
			continue // tree oid line, or the blank before any messages
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		p := line[tab+1:]
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}

// worktreePathForBranch returns the checkout path of the worktree that currently
// has branch checked out, across *all* worktrees of the repo (not just managed
// ones — the base typically lives in the main working tree). ok is false when no
// worktree has it checked out.
func (m *Manager) worktreePathForBranch(ctx context.Context, branch string) (string, bool, error) {
	out, err := m.git(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return "", false, fmt.Errorf("worktree: list: %w", err)
	}
	for _, rec := range parsePorcelain(out) {
		if rec.branch == branch {
			return rec.path, true, nil
		}
	}
	return "", false, nil
}

// dirtyAt reports whether the checkout at path holds uncommitted or untracked
// changes. A path that does not exist is reported clean.
func (m *Manager) dirtyAt(ctx context.Context, path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("worktree: stat %s: %w", path, err)
	}
	out, err := m.gitIn(ctx, path, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("worktree: status %s: %w", path, err)
	}
	return strings.TrimSpace(out) != "", nil
}

// revParse resolves a ref to its full commit oid.
func (m *Manager) revParse(ctx context.Context, ref string) (string, error) {
	out, err := m.git(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("worktree: rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(out), nil
}

// gitStatus runs a git command in the main repo purely for its exit status,
// preserving the *exec.ExitError so a caller can inspect the exit code (git uses
// distinct codes for is-ancestor / merge-tree "no" answers versus real errors).
func (m *Manager) gitStatus(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", m.repo}, args...)...)
	return cmd.Run()
}

// gitStdoutAllowExit1 runs a git command in the main repo and returns its stdout,
// treating exit 1 as success (the "no / conflicts" answer of predicate commands
// like merge-tree) while still surfacing any deeper failure.
func (m *Manager) gitStdoutAllowExit1(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", m.repo}, args...)...)
	out, err := cmd.Output()
	if err == nil {
		return string(out), nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return string(out), nil
	}
	return "", fmt.Errorf("worktree: %s: %w", strings.Join(args, " "), err)
}

// Remotes lists the repo's configured git remotes, in git's own order. It is the
// input to the command layer's remote disambiguation (sole remote → use it;
// several + a configured primary → use that; several + none → refuse).
func (m *Manager) Remotes(ctx context.Context) ([]string, error) {
	out, err := m.git(ctx, "remote")
	if err != nil {
		return nil, fmt.Errorf("worktree: list remotes: %w", err)
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// Fetched records a completed fetch of a remote base: the remote-tracking ref the
// fetch updated and the commit it now points at.
type Fetched struct {
	Remote string
	Base   string
	Ref    string // refs/remotes/<remote>/<base> — the ref the fetch updated
	Tip    string // commit oid the fetched remote base points at
}

// Fetch updates the remote-tracking ref for base from remote — read-only, and the
// only place the Manager ever reaches a git remote (it never pushes). It fetches
// with an explicit refspec into refs/remotes/<remote>/<base> so the tracking ref
// updates even for a remote without the conventional fetch refspec, and returns
// that ref (more legible in log bodies than FETCH_HEAD) with its resolved tip.
func (m *Manager) Fetch(ctx context.Context, remote, base string) (Fetched, error) {
	if strings.TrimSpace(remote) == "" {
		return Fetched{}, fmt.Errorf("worktree: fetch: no remote given")
	}
	ref := "refs/remotes/" + remote + "/" + base
	refspec := "+refs/heads/" + base + ":" + ref
	if _, err := m.git(ctx, "fetch", remote, refspec); err != nil {
		return Fetched{}, fmt.Errorf("worktree: fetch %s %s: %w", remote, base, err)
	}
	tip, err := m.revParse(ctx, ref)
	if err != nil {
		return Fetched{}, fmt.Errorf("worktree: resolve fetched %s: %w", ref, err)
	}
	return Fetched{Remote: remote, Base: base, Ref: ref, Tip: tip}, nil
}

// Pulled reports the best-effort local fast-forward of base toward a fetched remote
// tip — hygiene after an external land, never load-bearing.
type Pulled struct {
	Base    string
	Tip     string // the remote tip the local base was reconciled toward
	Moved   bool   // the local base ref advanced to Tip
	Already bool   // the local base already contained Tip (nothing to pull)
	Skipped string // non-empty: why the fast-forward was skipped (absent / diverged / dirty base)
}

// PullBase best-effort fast-forwards the local base branch to the fetched remote
// tip. It is deliberately unfailing: a missing, already-current, diverged, or dirty
// local base yields a Skipped reason (or Already) rather than an error, so a failed
// pull can never un-close a ticket the containment gate already proved landed. It
// never forces. A genuine git fault is still returned for the caller to surface as
// a warning — but the caller must treat it as non-fatal all the same.
func (m *Manager) PullBase(ctx context.Context, f Fetched) (Pulled, error) {
	res := Pulled{Base: f.Base, Tip: f.Tip}
	exists, err := m.branchExists(ctx, f.Base)
	if err != nil {
		return res, err
	}
	if !exists {
		res.Skipped = fmt.Sprintf("the local base %q does not exist", f.Base)
		return res, nil
	}
	baseTip, err := m.revParse(ctx, f.Base)
	if err != nil {
		return res, err
	}
	// The local base already contains the remote tip (equal, or already pulled, or
	// the remote is behind): nothing to fast-forward.
	if baseTip == f.Tip {
		res.Already = true
		return res, nil
	}
	if contained, err := m.isAncestor(ctx, f.Ref, f.Base); err != nil {
		return res, err
	} else if contained {
		res.Already = true
		return res, nil
	}
	// A fast-forward is possible only if the local base is an ancestor of the remote
	// tip; otherwise the local base has its own commits and pulling would need a
	// merge — out of scope for this best-effort hygiene step.
	if anc, err := m.isAncestor(ctx, f.Base, f.Ref); err != nil {
		return res, err
	} else if !anc {
		res.Skipped = fmt.Sprintf("the local base %q has diverged from %s; fast-forward is impossible (sync or reconcile it manually)", f.Base, f.Ref)
		return res, nil
	}
	moved, err := m.fastForward(ctx, f.Base, f.Ref, f.Tip, baseTip)
	if errors.Is(err, ErrBaseDirty) {
		res.Skipped = fmt.Sprintf("the %q checkout holds uncommitted changes", f.Base)
		return res, nil
	}
	if err != nil {
		return res, err
	}
	res.Moved = moved
	return res, nil
}

// RemoteMerge reports reconciling k's branch from a fetched remote base: the proof
// the branch landed upstream and the outcome of the best-effort local pull.
type RemoteMerge struct {
	Remote    string
	Base      string
	Branch    string
	Ref       string // refs/remotes/<remote>/<base>
	RemoteTip string // the fetched remote base tip
	BranchTip string // the attempt branch tip proven contained in RemoteTip
	Pull      Pulled // best-effort local fast-forward outcome (never load-bearing)
}

// MergeRemote is the external twin of Merge: instead of landing the local branch it
// reconciles from the remote. It fetches base from remote (read-only, never a
// push), verifies the branch tip is contained in the fetched remote base — the
// proof the external PR really landed it — and, only when it is, best-effort
// fast-forwards the local base toward the remote tip. It moves nothing when the
// branch is not contained: ErrNotContainedUpstream is returned and the caller
// records nothing. The local pull is hygiene, so its failure is captured in
// Pull.Skipped rather than raised, and never blocks the caller's `done`.
func (m *Manager) MergeRemote(ctx context.Context, k Key, remote, base string) (RemoteMerge, error) {
	if err := k.valid(); err != nil {
		return RemoteMerge{}, err
	}
	branch := k.branch()
	if exists, err := m.branchExists(ctx, branch); err != nil {
		return RemoteMerge{}, err
	} else if !exists {
		return RemoteMerge{}, ErrBranchNotFound
	}
	fetched, err := m.Fetch(ctx, remote, base)
	if err != nil {
		return RemoteMerge{}, err
	}
	branchTip, err := m.revParse(ctx, branch)
	if err != nil {
		return RemoteMerge{}, err
	}
	rm := RemoteMerge{Remote: remote, Base: base, Branch: branch, Ref: fetched.Ref, RemoteTip: fetched.Tip, BranchTip: branchTip}
	contained, err := m.isAncestor(ctx, branch, fetched.Ref)
	if err != nil {
		return rm, err
	}
	if !contained {
		return rm, ErrNotContainedUpstream
	}
	pull, err := m.PullBase(ctx, fetched)
	if err != nil {
		// Even a genuine git fault in the best-effort pull is non-fatal — the branch
		// is proven landed, so the caller records done regardless. Fold it into the
		// warning channel rather than failing the reconcile.
		pull.Skipped = err.Error()
	}
	rm.Pull = pull
	return rm, nil
}

// HeadBranch returns the short name of the branch currently checked out in repo —
// the default base for a new attempt (the branch the bound repo sits on at create
// time). A detached HEAD has no branch to record and yields ok=false, so the
// caller can require an explicit --base rather than invent one.
func HeadBranch(ctx context.Context, repo string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "symbolic-ref", "--quiet", "--short", "HEAD")
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), true, nil
	}
	// symbolic-ref exits 1 on a detached HEAD (a clean ExitError); anything else —
	// git missing, not a repo — is a real error worth surfacing.
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return "", false, nil
	}
	return "", false, fmt.Errorf("worktree: head branch of %s: %w", repo, err)
}
