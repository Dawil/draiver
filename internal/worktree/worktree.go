// Package worktree manages the per-session git worktree that isolates one
// draiverctld session, so parallel sessions on the same repo never collide and
// competing attempts stay comparable. It is the isolation primitive the
// supervisor's Admit (spawn) and Retire steps depend on.
//
// A session's worktree checkout lives under a per-user, per-repo managed base —
// <user-cache-dir>/draiver/worktrees/<repo-label>-<hash>/<ticket>/<attempt> — on
// a branch named draiver/<ticket>/<attempt>. The base sits *outside* the
// repository: that keeps checkouts out of the tracked working tree (no
// .gitignore, no accidental commits) while also keeping them out of .git, which a
// coding agent's auto-mode permission classifier treats as protected and refuses
// to write into (drvctl-010). The base is derived deterministically from the
// repo's git-common-dir, so the stateless Manager re-computes the same location
// after a restart; the per-attempt branch lives in the repo's refs, so a checkout
// swept from the cache is recreated by re-attaching to it.
//
// The three verbs mirror the reconcile loop: Create on Admit, Remove on Retire,
// and Reconcile on a daemon restart to clean worktrees left behind by a crash.
// The Manager holds no state of its own — every method re-derives the truth from
// `git worktree list`, so a restarted daemon reconciles correctly from disk
// alone.
package worktree

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// branchPrefix namespaces every draiver-managed branch, so managed worktrees are
// distinguishable from any a developer created by hand.
const branchPrefix = "draiver/"

// ErrCreate wraps every failure of `git worktree add` in Create, so a caller can
// recognize "the checkout could not be cut" by cause rather than by string-matching
// git's stderr. It deliberately spans *any* reason the add is refused — a branch
// already checked out in another worktree (the founding drvctl-027 incident), a
// corrupt or empty object at the branch tip (an unclean shutdown, gotcha #2), a
// missing base ref, a pre-occupied path — because to the health log they are one
// class: draiverctld cannot bring the attempt up. The reconciler's admit-health
// classifier keys the worktree-clash class on errors.Is(err, ErrCreate).
var ErrCreate = errors.New("git worktree add failed")

// Key identifies a worktree by the attempt it isolates. It is the stable handle
// the supervisor reconciles against: which attempts should have a live worktree.
type Key struct {
	Ticket  string
	Attempt string
}

func (k Key) branch() string { return branchPrefix + k.Ticket + "/" + k.Attempt }

// valid reports whether the key is safe to turn into a branch and a path. Empty
// components, path separators, and any "." / ".." traversal are rejected so a
// crafted ticket id can never escape the managed base or the refs/heads/draiver
// namespace.
func (k Key) valid() error {
	for label, v := range map[string]string{"ticket": k.Ticket, "attempt": k.Attempt} {
		switch {
		case v == "":
			return fmt.Errorf("worktree: %s is empty", label)
		case v == "." || v == "..":
			return fmt.Errorf("worktree: %s %q is a path traversal", label, v)
		case strings.ContainsAny(v, "/\\"):
			return fmt.Errorf("worktree: %s %q contains a path separator", label, v)
		}
	}
	return nil
}

// Spec describes a worktree to create.
type Spec struct {
	Key
	// Ref is the commit-ish the attempt's branch starts from when it is first
	// created. Empty means HEAD. Ignored once the branch already exists (a resume
	// re-attaches to the existing branch rather than re-pointing it).
	Ref string
}

// Worktree is a managed worktree as git currently reports it.
type Worktree struct {
	Key
	Path   string // absolute checkout path
	Branch string // short branch name, e.g. draiver/PROJ-1/0001
	// Prunable is set when git considers the admin entry stale — typically the
	// checkout directory vanished under it (a crash). Such an entry cannot be
	// resumed and is swept by Reconcile.
	Prunable bool
	Locked   bool
}

// Manager creates, removes, and reconciles the worktrees under one repo's
// managed base. It is safe to construct many; it keeps no cached state.
type Manager struct {
	repo string // absolute path to a working tree of the target repo
	base string // absolute base dir holding managed checkouts
}

// Option customizes a Manager.
type Option func(*Manager)

// WithBase overrides the managed base directory (absolute or relative to the
// repo). The default is the per-repo directory defaultBase derives under the user
// cache dir.
func WithBase(dir string) Option { return func(m *Manager) { m.base = dir } }

// NewManager resolves the managed base for repo (any path inside the target
// working tree) and returns a Manager. It errors if repo is not a git
// repository.
func NewManager(repo string, opts ...Option) (*Manager, error) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		return nil, fmt.Errorf("worktree: resolve repo: %w", err)
	}
	m := &Manager{repo: abs}
	for _, o := range opts {
		o(m)
	}
	// The common dir is shared by the main worktree and every linked one, so the
	// base is identical no matter which worktree the Manager is pointed at.
	commonDir, err := m.git(context.Background(), "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("worktree: %s is not a git repository: %w", repo, err)
	}
	if m.base == "" {
		base, err := defaultBase(strings.TrimSpace(commonDir))
		if err != nil {
			return nil, err
		}
		m.base = base
	} else if !filepath.IsAbs(m.base) {
		m.base = filepath.Join(abs, m.base)
	}
	m.base = filepath.Clean(m.base)
	return m, nil
}

// defaultBase derives the managed base for a repo whose (absolute) git-common-dir
// is commonDir. It lives under the user cache dir, keyed by a hash of commonDir,
// so it is deterministic per repo yet unique across repos and clones that share
// the cache. Being outside the repository keeps checkouts out of both the tracked
// working tree and .git — the latter matters because a coding agent's auto-mode
// permission classifier refuses to write under .git (drvctl-010). A human-legible
// repo label is prepended for debuggability; the hash is what guarantees
// uniqueness.
func defaultBase(commonDir string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("worktree: locate user cache dir: %w", err)
	}
	sum := sha256.Sum256([]byte(commonDir))
	// filepath.Dir of a standard ".../<repo>/.git" is the repo root; its base is a
	// friendly label. It is only cosmetic — the hash disambiguates.
	label := filepath.Base(filepath.Dir(commonDir))
	id := label + "-" + hex.EncodeToString(sum[:])[:12]
	return filepath.Join(cache, "draiver", "worktrees", id), nil
}

// Base is the managed base directory under which every checkout lives.
func (m *Manager) Base() string { return m.base }

func (m *Manager) pathFor(k Key) string { return filepath.Join(m.base, k.Ticket, k.Attempt) }

// Create returns the worktree for spec's attempt, creating it if absent. It is
// idempotent and crash-safe:
//   - a healthy managed worktree already at the target path is returned as-is
//     (an Admit that ran before a restart is not disturbed);
//   - a stale (prunable) entry left by a crash is pruned first, then recreated;
//   - if the attempt's branch already exists (a resume after Remove kept it) the
//     checkout re-attaches to it rather than re-pointing it at Ref.
func (m *Manager) Create(ctx context.Context, spec Spec) (Worktree, error) {
	if err := spec.valid(); err != nil {
		return Worktree{}, err
	}
	path := m.pathFor(spec.Key)

	if existing, ok, err := m.find(ctx, spec.Key); err != nil {
		return Worktree{}, err
	} else if ok && !existing.Prunable {
		return existing, nil
	} else if ok {
		// Stale admin entry from a crash — clear it before recreating.
		if err := m.prune(ctx); err != nil {
			return Worktree{}, err
		}
	}

	branch := spec.branch()
	branchExists, err := m.branchExists(ctx, branch)
	if err != nil {
		return Worktree{}, err
	}

	args := []string{"worktree", "add"}
	if branchExists {
		args = append(args, path, branch)
	} else {
		ref := spec.Ref
		if ref == "" {
			ref = "HEAD"
		}
		args = append(args, "-b", branch, path, ref)
	}
	if _, err := m.git(ctx, args...); err != nil {
		return Worktree{}, fmt.Errorf("worktree: create %s/%s: %w: %w", spec.Ticket, spec.Attempt, ErrCreate, err)
	}

	wt, ok, err := m.find(ctx, spec.Key)
	if err != nil {
		return Worktree{}, err
	}
	if !ok {
		return Worktree{}, fmt.Errorf("worktree: created %s but it is not listed", path)
	}
	return wt, nil
}

// RemoveOptions tunes Remove.
type RemoveOptions struct {
	// Force removes the worktree even when its checkout has uncommitted changes
	// or is locked. Retire uses this: the attempt's durable record is the ticket
	// log and any PR, not the checkout.
	Force bool
	// DeleteBranch also deletes the per-attempt branch (git branch -D). Off by
	// default: the branch is a cheap crash-recovery net and lets a later Create
	// resume the attempt.
	DeleteBranch bool
}

// Remove deletes the worktree checkout for k. A checkout that already vanished
// (a crash) is tolerated: its stale admin entry is pruned instead. Removing a
// worktree that was never created is a no-op.
func (m *Manager) Remove(ctx context.Context, k Key, opts RemoveOptions) error {
	if err := k.valid(); err != nil {
		return err
	}
	path := m.pathFor(k)

	args := []string{"worktree", "remove"}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, path)

	if _, err := m.git(ctx, args...); err != nil {
		// If the checkout is already gone the remove fails; fall back to pruning
		// the orphaned admin entry so the result is the same either way.
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			if perr := m.prune(ctx); perr != nil {
				return perr
			}
		} else {
			return fmt.Errorf("worktree: remove %s/%s: %w", k.Ticket, k.Attempt, err)
		}
	}

	if opts.DeleteBranch {
		if err := m.deleteBranch(ctx, k.branch()); err != nil {
			return err
		}
	}
	return nil
}

// Dirty reports whether the worktree for k holds changes that exist *only* in the
// checkout — uncommitted modifications to tracked files, or untracked files — the
// work a force-remove would silently destroy. It is the guard the reconciler
// consults before reclaiming a worktree on retire: a Review/Done attempt with a
// dirty checkout must be kept, not force-removed (drvctl-014).
//
// A worktree that was never created, or whose checkout vanished under a crash, is
// reported clean: there is nothing in a working tree to lose, and any committed
// work is safe on the branch. It shells out to `git status --porcelain` (which
// lists untracked files by default) in the checkout itself.
func (m *Manager) Dirty(ctx context.Context, k Key) (bool, error) {
	if err := k.valid(); err != nil {
		return false, err
	}
	path := m.pathFor(k)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil // no checkout — nothing uncommitted to lose
		}
		return false, fmt.Errorf("worktree: stat %s/%s: %w", k.Ticket, k.Attempt, err)
	}
	out, err := m.gitIn(ctx, path, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("worktree: status %s/%s: %w", k.Ticket, k.Attempt, err)
	}
	return strings.TrimSpace(out) != "", nil
}

// Reconcile brings the managed worktrees on disk in line with the set that
// should still exist (keep). It is the crash-recovery entry point a restarted
// daemon calls before it admits anything:
//   - every stale (prunable) managed entry is swept, even if its key is in keep,
//     because its checkout is gone and Admit will recreate it from the surviving
//     branch;
//   - every healthy managed worktree whose key is not in keep is force-removed
//     (its session died and nothing wants it back).
//
// Branches are never deleted here. Reconcile returns the worktrees it removed,
// for the caller to log.
func (m *Manager) Reconcile(ctx context.Context, keep []Key) ([]Worktree, error) {
	wts, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	keepSet := make(map[Key]bool, len(keep))
	for _, k := range keep {
		keepSet[k] = true
	}

	var removed []Worktree
	for _, wt := range wts {
		switch {
		case wt.Prunable:
			// Dead checkout: sweep regardless of keep so Admit recreates it fresh.
		case keepSet[wt.Key]:
			continue
		}
		if err := m.Remove(ctx, wt.Key, RemoveOptions{Force: true}); err != nil {
			return removed, err
		}
		removed = append(removed, wt)
	}
	// Final sweep for any admin entries left stale by the removals above.
	if err := m.prune(ctx); err != nil {
		return removed, err
	}
	return removed, nil
}

// List returns the managed worktrees git currently tracks — those whose checkout
// lives under the managed base — sorted by path. Worktrees a developer created
// elsewhere in the repo are ignored.
func (m *Manager) List(ctx context.Context) ([]Worktree, error) {
	out, err := m.git(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("worktree: list: %w", err)
	}
	var wts []Worktree
	for _, rec := range parsePorcelain(out) {
		if !m.managed(rec.path) {
			continue
		}
		wt := Worktree{Path: rec.path, Branch: rec.branch, Prunable: rec.prunable, Locked: rec.locked}
		wt.Key = keyFromBranch(rec.branch)
		wts = append(wts, wt)
	}
	return wts, nil
}

// find returns the managed worktree for k, matched by its branch ref.
func (m *Manager) find(ctx context.Context, k Key) (Worktree, bool, error) {
	wts, err := m.List(ctx)
	if err != nil {
		return Worktree{}, false, err
	}
	for _, wt := range wts {
		if wt.Key == k {
			return wt, true, nil
		}
	}
	return Worktree{}, false, nil
}

// managed reports whether path is the base itself or sits under it.
func (m *Manager) managed(path string) bool {
	p := filepath.Clean(path)
	if p == m.base {
		return true
	}
	return strings.HasPrefix(p, m.base+string(filepath.Separator))
}

func (m *Manager) branchExists(ctx context.Context, branch string) (bool, error) {
	err := m.gitQuiet(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	// show-ref exits 1 (a clean ExitError) when the ref is absent; any other
	// failure — git missing, a broken repo — is a real error worth surfacing.
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return false, nil
	}
	return false, fmt.Errorf("worktree: check branch %s: %w", branch, err)
}

func (m *Manager) deleteBranch(ctx context.Context, branch string) error {
	// A branch checked out elsewhere, or already gone, must not fail Remove; only
	// surface an unexpected error.
	if _, err := m.git(ctx, "branch", "-D", branch); err != nil {
		if exists, cerr := m.branchExists(ctx, branch); cerr == nil && !exists {
			return nil
		}
		return fmt.Errorf("worktree: delete branch %s: %w", branch, err)
	}
	return nil
}

func (m *Manager) prune(ctx context.Context) error {
	if _, err := m.git(ctx, "worktree", "prune"); err != nil {
		return fmt.Errorf("worktree: prune: %w", err)
	}
	return nil
}

// git runs a git command in the main repo and returns trimmed stdout, wrapping
// any failure with the command's stderr for a legible error.
func (m *Manager) git(ctx context.Context, args ...string) (string, error) {
	return m.gitIn(ctx, m.repo, args...)
}

// gitIn runs a git command with its working directory set to dir — a specific
// checkout path — rather than the main repo, so a caller can inspect one
// worktree in isolation (e.g. its dirty state). Output and error handling match
// git.
func (m *Manager) gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return stdout.String(), nil
}

// gitQuiet runs a git command purely for its exit status.
func (m *Manager) gitQuiet(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", m.repo}, args...)...)
	return cmd.Run()
}

// keyFromBranch recovers the attempt key from a draiver/<ticket>/<attempt>
// branch. A non-managed or malformed branch yields the zero Key.
func keyFromBranch(branch string) Key {
	rest, ok := strings.CutPrefix(branch, branchPrefix)
	if !ok {
		return Key{}
	}
	ticket, attempt, ok := strings.Cut(rest, "/")
	if !ok || ticket == "" || attempt == "" || strings.Contains(attempt, "/") {
		return Key{}
	}
	return Key{Ticket: ticket, Attempt: attempt}
}

// porcelainRecord is one block of `git worktree list --porcelain`.
type porcelainRecord struct {
	path     string
	branch   string // short name; empty when detached
	prunable bool
	locked   bool
}

// parsePorcelain splits the porcelain listing into records. Blocks are separated
// by blank lines; each starts with a `worktree <path>` line.
func parsePorcelain(out string) []porcelainRecord {
	var recs []porcelainRecord
	var cur *porcelainRecord
	flush := func() {
		if cur != nil && cur.path != "" {
			recs = append(recs, *cur)
		}
		cur = nil
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		field, val, _ := strings.Cut(line, " ")
		switch field {
		case "worktree":
			flush()
			cur = &porcelainRecord{path: filepath.Clean(val)}
		case "branch":
			if cur != nil {
				cur.branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "prunable":
			if cur != nil {
				cur.prunable = true
			}
		case "locked":
			if cur != nil {
				cur.locked = true
			}
		}
	}
	flush()
	return recs
}
